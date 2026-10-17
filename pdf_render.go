package main

// pdf-render-pages: pages of a PDF as images, so a client can look at them
// without rendering the PDF itself. Every image is its own resource_link and
// its own resources/read, so the 6 MiB export limit applies to each page on
// its own; a call is bounded by maxRenderPages and the export cache, not by a
// total size.

import (
	"errors"
	"fmt"
	"image/png"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
)

const (
	maxRenderPages    = maxInspectPages
	defaultRenderDPI  = 150
	minRenderDPI      = 50
	maxRenderDPI      = 300
	renderJPEGQuality = 85

	// Far below the ~536 megapixel bitmap ceiling of 32-bit WebAssembly,
	// which the document's own memory shares; A0 at 150 DPI is 26 MP.
	maxRenderPixels = 40_000_000
)

// renderPageDeadline bounds the PDFium session of one page; a test under the
// race detector, which slows WebAssembly many times over, extends it.
var renderPageDeadline = 30 * time.Second

// maxRenderImageBytes is the ceiling for one image. It is maxExportBytes,
// which createFileExport enforces anyway; tests lower it, nothing raises it.
var maxRenderImageBytes = int64(maxExportBytes)

func pdfRenderToolDef() map[string]any {
	return map[string]any{
		"name": "pdf-render-pages",
		"description": fmt.Sprintf("Render pages of a PDF stored in Anytype as images: one resource_link per page, in page order, as JPEG (default) or PNG at dpi (default %d, %d-%d). "+
			"Up to %d pages per call; the rest is named in next_pages. Each image counts separately against the 6 MiB export limit; a page that would be larger is rendered at a lower DPI, and each result gives the DPI used. "+
			"With include_text, each page also carries its text layer (at most %d characters per call; pages beyond that move to next_pages). Rendering the same pages again returns the same links.",
			defaultRenderDPI, minRenderDPI, maxRenderDPI, maxRenderPages, maxInspectTextRunes),
		"inputSchema": restSchema([]string{"object_id", "pages"}, map[string]any{
			"object_id":    map[string]any{"type": "string", "minLength": 1, "maxLength": 1024, "description": "Anytype file object ID of the PDF."},
			"pages":        map[string]any{"type": "string", "minLength": 1, "maxLength": maxPagesSpecLen, "description": "Pages to render, e.g. \"1-3,7\"."},
			"format":       map[string]any{"type": "string", "enum": []string{"jpeg", "png"}, "default": "jpeg", "description": "jpeg suits scans and photos, png sharp text and drawings."},
			"dpi":          map[string]any{"type": "integer", "minimum": minRenderDPI, "maximum": maxRenderDPI, "default": defaultRenderDPI, "description": "Resolution; 150 reads well, 300 for fine print."},
			"include_text": map[string]any{"type": "boolean", "default": false, "description": "Also return the text layer of each rendered page."},
		}),
	}
}

type renderRequest struct {
	objectID    string
	pages       []pageRange
	format      string
	dpi         int
	includeText bool
}

func parseRenderArgs(args map[string]any) (renderRequest, error) {
	for key := range args {
		switch key {
		case "object_id", "pages", "format", "dpi", "include_text":
		default:
			return renderRequest{}, fmt.Errorf("unknown argument: %s", key)
		}
	}
	objectID, _ := args["object_id"].(string)
	req := renderRequest{objectID: strings.TrimSpace(objectID), format: "jpeg", dpi: defaultRenderDPI}
	if req.objectID == "" || len(req.objectID) > 1024 {
		return renderRequest{}, errors.New("object_id must be a non-empty string of at most 1024 bytes")
	}
	spec, ok := args["pages"].(string)
	if !ok {
		return renderRequest{}, errors.New("pages is required, e.g. \"1-3\"")
	}
	pages, err := parsePageSelection(spec)
	if err != nil {
		return renderRequest{}, err
	}
	req.pages = pages
	if value, present := args["format"]; present {
		format, _ := value.(string)
		switch format = strings.ToLower(strings.TrimSpace(format)); format {
		case "jpeg", "jpg":
			req.format = "jpeg"
		case "png":
			req.format = "png"
		default:
			return renderRequest{}, errors.New("format must be jpeg or png")
		}
	}
	if value, present := args["dpi"]; present {
		var dpi float64
		switch v := value.(type) {
		case float64:
			dpi = v
		case int:
			dpi = float64(v)
		default:
			dpi = -1
		}
		if dpi != math.Trunc(dpi) || dpi < minRenderDPI || dpi > maxRenderDPI {
			return renderRequest{}, fmt.Errorf("dpi must be a whole number from %d to %d", minRenderDPI, maxRenderDPI)
		}
		req.dpi = int(dpi)
	}
	if value, present := args["include_text"]; present {
		if req.includeText, ok = value.(bool); !ok {
			return renderRequest{}, errors.New("include_text must be true or false")
		}
	}
	return req, nil
}

func (s *mcpServer) toolPDFRender(args map[string]any) (nativeToolResult, error) {
	req, err := parseRenderArgs(args)
	if err != nil {
		return nativeToolResult{}, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nativeToolResult{}, err
	}
	defer client.Close()
	staged, err := s.stageAnytypeFile(client, req.objectID)
	if err != nil {
		return nativeToolResult{}, err
	}
	defer staged.Close()
	if !isPDF(staged.root, staged.rel) {
		return nativeToolResult{}, errors.New("this file is not a PDF")
	}
	// Page count, sizes and text come from the inspect cache when the PDF was
	// read before, which is what ChatGPT's repeated call finds.
	pages, err := readPDFPages(staged.root, staged.rel)
	if err != nil {
		return nativeToolResult{}, err
	}
	sum, err := fileSHA256(staged.root, staged.rel)
	if err != nil {
		return nativeToolResult{}, err
	}
	var scope []int
	for _, r := range req.pages {
		if r.last > len(pages) {
			return nativeToolResult{}, fmt.Errorf("pages %s: this PDF has %d pages", formatPageSelection(req.pages), len(pages))
		}
		for page := r.first; page <= r.last; page++ {
			scope = append(scope, page)
		}
	}
	var next []int
	var texts map[int]map[string]any
	if req.includeText {
		details, rest := pageDetails(pages, scope, true)
		scope, next = scope[:len(details)], rest
		texts = map[int]map[string]any{}
		for _, d := range details {
			texts[d["page"].(int)] = d
		}
	} else if len(scope) > maxRenderPages {
		scope, next = scope[:maxRenderPages], scope[maxRenderPages:]
	}

	ext, render := "jpg", fmt.Sprintf("%s@%d", req.format, req.dpi)
	if req.format == "png" {
		ext = "png"
	}
	base := strings.TrimSuffix(staged.name, filepath.Ext(staged.name))
	results := make([]map[string]any, 0, len(scope))
	var links []map[string]any
	var failures []string
	for _, number := range scope {
		filename := renderFilename(base, number, ext)
		source := &pageSource{sha256: sum, pages: strconv.Itoa(number), pageCount: len(pages), render: render}
		exported, err := s.renderPage(staged, pages[number-1], number, req, filename, source)
		if err != nil {
			results = append(results, map[string]any{"page": number, "error": err.Error()})
			failures = append(failures, fmt.Sprintf("page %d: %v", number, err))
			continue
		}
		entry := map[string]any{"page": number}
		for key, value := range exported.Metadata {
			entry[key] = value
		}
		delete(entry, "pages")
		delete(entry, "source_page_count")
		if dpi, _ := entry["dpi"].(int); dpi < req.dpi {
			entry["dpi_reduced"] = true
		}
		if text := texts[number]; text != nil {
			for _, key := range []string{"text", "chars", "note", "text_truncated"} {
				if value, ok := text[key]; ok {
					entry[key] = value
				}
			}
		}
		results = append(results, entry)
		links = append(links, exported.Content...)
	}
	if len(links) == 0 {
		return nativeToolResult{}, errors.New("no page was rendered: " + strings.Join(failures, "; "))
	}
	out := map[string]any{
		"object_id": req.objectID, "filename": staged.name, "page_count": len(pages),
		"format": req.format, "requested_dpi": req.dpi,
		"rendered": len(links), "failed": len(failures), "results": results,
		"truncated": next != nil,
	}
	if next != nil {
		out["next_pages"] = formatPageSelection(rangesOf(next))
	}
	return nativeToolResult{Metadata: out, Content: links}, nil
}

// renderPage returns the existing link for this page, format and DPI, or
// renders the page into staging and exports it. PDFium is not started for a
// page that is already in the export cache.
func (s *mcpServer) renderPage(staged *stagedFile, page pdfPage, number int, req renderRequest, filename string, source *pageSource) (nativeToolResult, error) {
	if result, ok := s.reusePageExport(req.objectID, filename, source, time.Now()); ok {
		return result, nil
	}
	data, width, height, dpi, err := renderPageImage(staged, page, number, req.format, req.dpi)
	if err != nil {
		return nativeToolResult{}, err
	}
	imageName := fmt.Sprintf(".render-%d.%s", number, filepath.Ext(filename)[1:])
	if err := staged.root.WriteFile(imageName, data, 0600); err != nil {
		return nativeToolResult{}, err
	}
	source.width, source.height, source.dpiUsed = width, height, dpi
	return s.createFileExport(req.objectID, filename, staged.dir, filepath.Join(staged.dir, imageName), time.Now(), source)
}

// renderPageImage renders one page in its own PDFium session, so a page that
// hangs costs renderPageDeadline and not the whole call. An image over
// maxRenderImageBytes is rendered again at a DPI scaled by the square root of
// the overshoot, down to minRenderDPI.
func renderPageImage(staged *stagedFile, page pdfPage, number int, format string, requested int) (data []byte, width, height, dpi int, err error) {
	dpi = requested
	if pixels := page.widthPt / 72 * page.heightPt / 72; pixels*float64(dpi*dpi) > maxRenderPixels {
		dpi = max(minRenderDPI, int(math.Sqrt(maxRenderPixels/pixels)))
	}
	file, err := staged.root.Open(staged.rel)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, 0, 0, err
	}
	err = pdfEngine.with(renderPageDeadline, func(instance pdfium.Pdfium) error {
		doc, err := instance.OpenDocument(&requests.OpenDocument{FileReader: file, FileReaderSize: info.Size()})
		if err != nil {
			return describePDFiumError(err)
		}
		defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		for {
			request := &requests.RenderToFile{
				RenderPageInDPI: &requests.RenderPageInDPI{
					Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: number - 1}},
					DPI:  dpi,
				},
				OutputTarget: requests.RenderToFileOutputTargetBytes,
			}
			if format == "png" {
				// BestCompression saves 5-10 % at three times the encode time.
				request.OutputFormat, request.PNGCompressionLevel = requests.RenderToFileOutputFormatPNG, png.DefaultCompression
			} else {
				request.OutputFormat, request.OutputQuality = requests.RenderToFileOutputFormatJPG, renderJPEGQuality
			}
			rendered, err := instance.RenderToFile(request)
			if err != nil {
				return fmt.Errorf("rendering failed: %w", describePDFiumError(err))
			}
			if rendered.ImageBytes == nil || len(*rendered.ImageBytes) == 0 {
				return errors.New("rendering produced no image")
			}
			size := int64(len(*rendered.ImageBytes))
			if size <= maxRenderImageBytes {
				data, width, height = *rendered.ImageBytes, rendered.Width, rendered.Height
				return nil
			}
			if dpi == minRenderDPI {
				return fmt.Errorf("even at %d DPI the image is %d bytes, above the %d-byte limit per image; try format jpeg", minRenderDPI, size, maxRenderImageBytes)
			}
			scaled := int(float64(dpi) * math.Sqrt(float64(maxRenderImageBytes)/float64(size)) * 0.9)
			dpi = max(minRenderDPI, min(scaled, dpi-1))
		}
	})
	return data, width, height, dpi, err
}

// renderFilename names a rendered page after its PDF, e.g.
// "Report (page 3).jpg", shortening the base to stay within 255 bytes.
func renderFilename(base string, page int, ext string) string {
	suffix := fmt.Sprintf(" (page %d).%s", page, ext)
	for len(base)+len(suffix) > 255 {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	if base = strings.TrimSpace(base); base == "" {
		base = "page"
	}
	return base + suffix
}
