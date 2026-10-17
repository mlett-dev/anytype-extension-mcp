package main

// pdf-inspect: what is in a PDF, without handing the PDF over. The answer is
// built from page ranges, so its size does not grow with the page count;
// per-page detail exists only for pages the caller names.
//
// PDFium runs as WebAssembly (go-pdfium), which needs no C library and so
// works in the scratch image. It is started on first use and stopped again
// after pdfiumIdleTimeout, because a loaded instance holds about 300 MB.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

const (
	pdfiumIdleTimeout = 10 * time.Minute

	// A page counts as having a text layer from this many non-space
	// characters on, so a scan with a stamped page number is still a scan.
	minTextLayerChars = 20

	maxInspectPages     = 20
	maxInspectTextRunes = 20000
	maxInspectSnippets  = 20
	snippetContext      = 70
	maxInspectRanges    = 20
	maxSizeGroups       = 10
	maxQueryLen         = 200

	maxCachedPDFs     = 4
	maxCachedPDFRunes = 32 << 20
)

// pdfReadDeadline bounds one PDFium session; a test shortens it.
var pdfReadDeadline = time.Minute

func (s *mcpServer) pdfToolDefs() []any {
	return []any{map[string]any{
		"name": "pdf-inspect",
		"description": fmt.Sprintf("Inspect a PDF stored in Anytype without transferring it: page count, page sizes and which pages have a text layer, given as page ranges. "+
			"With pages, per-page details for up to %d pages per call; with text, also their text layer (at most %d characters per call, the rest is named in next_pages). "+
			"With query, a case-insensitive search of the text layer, giving the pages and short snippets. Pages without a text layer, such as scans, cannot be searched; file-export with pages hands them over to be viewed.",
			maxInspectPages, maxInspectTextRunes),
		"inputSchema": restSchema([]string{"object_id"}, map[string]any{
			"object_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024, "description": "Anytype file object ID of the PDF."},
			"pages":     map[string]any{"type": "string", "minLength": 1, "maxLength": maxPagesSpecLen, "description": "Pages to detail, e.g. \"37-38\". Also limits query to these pages."},
			"text":      map[string]any{"type": "boolean", "default": false, "description": "With pages: include the text layer of those pages."},
			"query":     map[string]any{"type": "string", "minLength": 1, "maxLength": maxQueryLen, "description": "Text to search for in the text layer."},
		}),
	}}
}

type pdfPage struct {
	widthPt, heightPt float64
	text              string // normalised text layer
	chars             int    // non-space characters in text
}

type inspectRequest struct {
	objectID string
	pages    []pageRange
	text     bool
	query    string
}

func parseInspectArgs(args map[string]any) (inspectRequest, error) {
	for key := range args {
		switch key {
		case "object_id", "pages", "text", "query":
		default:
			return inspectRequest{}, fmt.Errorf("unknown argument: %s", key)
		}
	}
	objectID, _ := args["object_id"].(string)
	req := inspectRequest{objectID: strings.TrimSpace(objectID)}
	if req.objectID == "" || len(req.objectID) > 1024 {
		return inspectRequest{}, errors.New("object_id must be a non-empty string of at most 1024 bytes")
	}
	if value, present := args["pages"]; present {
		spec, ok := value.(string)
		if !ok {
			return inspectRequest{}, errors.New("pages must be a string such as \"1-5,9\"")
		}
		pages, err := parsePageSelection(spec)
		if err != nil {
			return inspectRequest{}, err
		}
		req.pages = pages
	}
	if value, present := args["text"]; present {
		text, ok := value.(bool)
		if !ok {
			return inspectRequest{}, errors.New("text must be true or false")
		}
		req.text = text
	}
	if req.text && req.pages == nil {
		return inspectRequest{}, errors.New("text needs pages, e.g. \"1-3\"")
	}
	if value, present := args["query"]; present {
		query, ok := value.(string)
		query = strings.Join(strings.Fields(query), " ")
		if !ok || query == "" || utf8.RuneCountInString(query) > maxQueryLen {
			return inspectRequest{}, fmt.Errorf("query must be non-empty text of at most %d characters", maxQueryLen)
		}
		req.query = query
	}
	return req, nil
}

func (s *mcpServer) toolPDFInspect(args map[string]any) (map[string]any, error) {
	req, err := parseInspectArgs(args)
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	staged, err := s.stageAnytypeFile(client, req.objectID)
	if err != nil {
		return nil, err
	}
	defer staged.Close()
	if !isPDF(staged.root, staged.rel) {
		return nil, errors.New("this file is not a PDF")
	}
	info, err := staged.root.Stat(staged.rel)
	if err != nil {
		return nil, err
	}
	pages, err := readPDFPages(staged.root, staged.rel)
	if err != nil {
		return nil, err
	}
	out := pdfOverview(pages)
	out["object_id"], out["filename"] = req.objectID, staged.name
	out["size_bytes"], out["fits_file_export"] = info.Size(), info.Size() <= maxExportBytes

	var scope []int
	if req.pages != nil {
		for _, r := range req.pages {
			if r.last > len(pages) {
				return nil, fmt.Errorf("pages %s: this PDF has %d pages", formatPageSelection(req.pages), len(pages))
			}
			for page := r.first; page <= r.last; page++ {
				scope = append(scope, page)
			}
		}
		details, next := pageDetails(pages, scope, req.text)
		out["pages"] = details
		out["truncated"] = next != nil
		if next != nil {
			out["next_pages"] = formatPageSelection(rangesOf(next))
		}
	}
	if req.query != "" {
		if scope == nil {
			scope = make([]int, len(pages))
			for i := range scope {
				scope[i] = i + 1
			}
		}
		out["search"] = searchPDFPages(pages, scope, req.query)
	}
	return out, nil
}

// pdfOverview is the part of every answer that describes the whole document.
// Its size is bounded by maxSizeGroups and maxInspectRanges, not by the page
// count.
func pdfOverview(pages []pdfPage) map[string]any {
	truncated := false
	capped := func(nums []int) string {
		ranges := rangesOf(nums)
		if len(ranges) > maxInspectRanges {
			ranges, truncated = ranges[:maxInspectRanges], true
		}
		return formatPageSelection(ranges)
	}

	type size struct{ w, h int }
	var order []size
	bySize := map[size][]int{}
	var withText, withoutText []int
	for i, page := range pages {
		key := size{mm(page.widthPt), mm(page.heightPt)}
		if _, seen := bySize[key]; !seen {
			order = append(order, key)
		}
		bySize[key] = append(bySize[key], i+1)
		if page.chars >= minTextLayerChars {
			withText = append(withText, i+1)
		} else {
			withoutText = append(withoutText, i+1)
		}
	}
	groups := []map[string]any{}
	for i, key := range order {
		if i == maxSizeGroups {
			truncated = true
			break
		}
		group := map[string]any{"pages": capped(bySize[key]), "width_mm": key.w, "height_mm": key.h}
		if name := paperFormat(key.w, key.h); name != "" {
			group["format"] = name
		}
		groups = append(groups, group)
	}
	out := map[string]any{
		"page_count": len(pages),
		"page_sizes": groups,
		"text_layer": map[string]any{
			"with_text": capped(withText), "without_text": capped(withoutText),
			"count_with_text": len(withText), "count_without_text": len(withoutText),
		},
	}
	if truncated {
		out["ranges_truncated"] = true
	}
	return out
}

// pageDetails describes the pages in scope, up to maxInspectPages and, with
// text, maxInspectTextRunes. It returns the pages left for a next call.
func pageDetails(pages []pdfPage, scope []int, withText bool) ([]map[string]any, []int) {
	details := []map[string]any{}
	budget := maxInspectTextRunes
	for i, number := range scope {
		if i == maxInspectPages {
			return details, scope[i:]
		}
		page := pages[number-1]
		entry := map[string]any{"page": number, "chars": page.chars, "width_mm": mm(page.widthPt), "height_mm": mm(page.heightPt)}
		if withText {
			text := page.text
			if runes := utf8.RuneCountInString(text); runes > budget {
				if len(details) > 0 {
					return details, scope[i:]
				}
				// A single page over the budget is cut rather than skipped,
				// so every call makes progress.
				text = string([]rune(text)[:budget])
				entry["text_truncated"] = true
			}
			budget -= utf8.RuneCountInString(text)
			entry["text"] = text
			if page.chars < minTextLayerChars {
				entry["note"] = "no text layer; view the page instead"
			}
		}
		details = append(details, entry)
	}
	return details, nil
}

func searchPDFPages(pages []pdfPage, scope []int, query string) map[string]any {
	needle := strings.ToLower(query)
	matches := []map[string]any{}
	var matched, unsearchable []int
	count := 0
	for _, number := range scope {
		page := pages[number-1]
		if page.chars < minTextLayerChars {
			unsearchable = append(unsearchable, number)
		}
		flat := strings.Join(strings.Fields(page.text), " ")
		lower := strings.ToLower(flat)
		if len(lower) != len(flat) {
			// Lowercasing changed byte lengths (rare letters such as İ), so
			// offsets into lower do not fit flat: quote from lower instead.
			flat = lower
		}
		hits := 0
		for from := 0; ; {
			at := strings.Index(lower[from:], needle)
			if at < 0 {
				break
			}
			at += from
			hits++
			if len(matches) < maxInspectSnippets {
				matches = append(matches, map[string]any{"page": number, "snippet": snippetAround(flat, at, at+len(needle))})
			}
			from = at + len(needle)
		}
		if hits > 0 {
			matched = append(matched, number)
			count += hits
		}
	}
	truncated := false
	capped := func(nums []int) string {
		ranges := rangesOf(nums)
		if len(ranges) > maxInspectRanges {
			ranges, truncated = ranges[:maxInspectRanges], true
		}
		return formatPageSelection(ranges)
	}
	out := map[string]any{
		"query": query, "match_count": count, "matches": matches,
		"pages_with_matches": capped(matched), "not_searchable": capped(unsearchable),
	}
	if truncated {
		out["ranges_truncated"] = true
	}
	return out
}

func snippetAround(text string, start, end int) string {
	from, to := max(0, start-snippetContext), min(len(text), end+snippetContext)
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	snippet := text[from:to]
	if from > 0 {
		snippet = "…" + snippet
	}
	if to < len(text) {
		snippet += "…"
	}
	return snippet
}

// rangesOf turns ascending page numbers into ranges.
func rangesOf(nums []int) []pageRange {
	var ranges []pageRange
	for _, n := range nums {
		if len(ranges) > 0 && ranges[len(ranges)-1].last == n-1 {
			ranges[len(ranges)-1].last = n
			continue
		}
		ranges = append(ranges, pageRange{n, n})
	}
	return ranges
}

func mm(points float64) int { return int(math.Round(points * 25.4 / 72)) }

// paperFormat names common paper sizes within 3 mm.
func paperFormat(w, h int) string {
	short, long := min(w, h), max(w, h)
	for _, f := range []struct {
		name        string
		short, long int
	}{{"A3", 297, 420}, {"A4", 210, 297}, {"A5", 148, 210}, {"A6", 105, 148}, {"Letter", 216, 279}, {"Legal", 216, 356}} {
		if abs(short-f.short) <= 3 && abs(long-f.long) <= 3 {
			if w > h {
				return f.name + " landscape"
			}
			return f.name + " portrait"
		}
	}
	return ""
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// normalizePageText turns PDFium's page text into plain lines: \n line ends,
// no control characters, no trailing spaces, at most one blank line in a row.
func normalizePageText(text string) string {
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text)
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || r == '￾' || r == '￿' {
			return -1
		}
		return r
	}, text)
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			if blank++; blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func nonSpaceRunes(text string) int {
	n := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// pdfCache keeps the pages of recently inspected PDFs by content hash, so the
// usual sequence of overview, text and search reads a PDF once.
var pdfCache struct {
	sync.Mutex
	entries []pdfCacheEntry // most recent last
}

type pdfCacheEntry struct {
	sha256 string
	pages  []pdfPage
	runes  int
}

func cachedPDFPages(sum string) ([]pdfPage, bool) {
	pdfCache.Lock()
	defer pdfCache.Unlock()
	for i, entry := range pdfCache.entries {
		if entry.sha256 == sum {
			pdfCache.entries = append(append(pdfCache.entries[:i:i], pdfCache.entries[i+1:]...), entry)
			return entry.pages, true
		}
	}
	return nil, false
}

func cachePDFPages(sum string, pages []pdfPage) {
	runes := 0
	for _, page := range pages {
		runes += utf8.RuneCountInString(page.text)
	}
	if runes > maxCachedPDFRunes {
		return
	}
	pdfCache.Lock()
	defer pdfCache.Unlock()
	pdfCache.entries = append(pdfCache.entries, pdfCacheEntry{sum, pages, runes})
	total := 0
	for _, entry := range pdfCache.entries {
		total += entry.runes
	}
	for len(pdfCache.entries) > maxCachedPDFs || total > maxCachedPDFRunes {
		total -= pdfCache.entries[0].runes
		pdfCache.entries = pdfCache.entries[1:]
	}
}

// readPDFPages reads size and text layer of every page.
func readPDFPages(root *os.Root, rel string) ([]pdfPage, error) {
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("only regular files can be inspected")
	}
	if info.Size() > maxPDFSourceBytes {
		return nil, fmt.Errorf("the PDF is %d bytes; PDFs of at most %d MiB can be inspected", info.Size(), maxPDFSourceBytes>>20)
	}
	sum, err := fileSHA256(root, rel)
	if err != nil {
		return nil, err
	}
	if pages, ok := cachedPDFPages(sum); ok {
		return pages, nil
	}

	var pages []pdfPage
	err = pdfEngine.with(pdfReadDeadline, func(instance pdfium.Pdfium) error {
		doc, err := instance.OpenDocument(&requests.OpenDocument{FileReader: file, FileReaderSize: info.Size()})
		if err != nil {
			return describePDFiumError(err)
		}
		defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
		if err != nil {
			return describePDFiumError(err)
		}
		pages = make([]pdfPage, count.PageCount)
		for i := range pages {
			page := requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}
			size, err := instance.GetPageSize(&requests.GetPageSize{Page: page})
			if err != nil {
				return fmt.Errorf("page %d: %w", i+1, describePDFiumError(err))
			}
			text, err := instance.GetPageText(&requests.GetPageText{Page: page})
			if err != nil {
				return fmt.Errorf("page %d: %w", i+1, describePDFiumError(err))
			}
			normalized := normalizePageText(text.Text)
			pages[i] = pdfPage{widthPt: size.Width, heightPt: size.Height, text: normalized, chars: nonSpaceRunes(normalized)}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	cachePDFPages(sum, pages)
	return pages, nil
}

func describePDFiumError(err error) error {
	switch {
	case errors.Is(err, pdfiumerrors.ErrPassword):
		return errors.New("the PDF is password-protected")
	case errors.Is(err, pdfiumerrors.ErrFormat), errors.Is(err, pdfiumerrors.ErrFile):
		return fmt.Errorf("the PDF could not be read: %w", err)
	}
	return err
}

// pdfEngine owns the one PDFium instance. Requests are serialised: PDFium is
// not thread-safe, and one instance is enough for a single client.
var pdfEngine pdfiumEngine

type pdfiumEngine struct {
	mu   sync.Mutex
	pool pdfium.Pool
	idle *time.Timer
}

// with runs fn on an instance. A session that outlives deadline is killed
// from outside, since a stuck PDFium call cannot be interrupted from within,
// and would otherwise block every later tool call of this server.
func (e *pdfiumEngine) with(deadline time.Duration, fn func(pdfium.Pdfium) error) (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idle != nil {
		e.idle.Stop()
	}
	defer func() { e.idle = time.AfterFunc(pdfiumIdleTimeout, e.closeIdle) }()
	if e.pool == nil {
		pool, err := webassembly.Init(webassembly.Config{
			MinIdle: 0, MaxIdle: 1, MaxTotal: 1,
			// The bundled module needs exception handling; closing on a done
			// context is what makes Kill interrupt a running call.
			RuntimeConfig: wazero.NewRuntimeConfig().
				WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
				WithCloseOnContextDone(true),
		})
		if err != nil {
			return fmt.Errorf("the PDF engine could not start: %w", err)
		}
		e.pool = pool
	}
	instance, err := e.pool.GetInstance(30 * time.Second)
	if err != nil {
		return fmt.Errorf("the PDF engine is not available: %w", err)
	}
	var killed atomic.Bool
	watchdog := time.AfterFunc(deadline, func() {
		killed.Store(true)
		_ = instance.Kill()
	})
	defer func() {
		stopped := watchdog.Stop()
		if r := recover(); r != nil {
			err = fmt.Errorf("the PDF could not be read: %v", r)
		}
		if killed.Load() || !stopped {
			err = fmt.Errorf("reading the PDF took longer than %s and was stopped", deadline)
			return
		}
		if err != nil {
			// After a failure the instance may hold half-open state; a fresh
			// one is cheap.
			_ = instance.Kill()
			return
		}
		_ = instance.Close()
	}()
	return fn(instance)
}

func (e *pdfiumEngine) closeIdle() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pool != nil {
		_ = e.pool.Close()
		e.pool = nil
	}
}
