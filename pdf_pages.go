package main

// Page extraction for file-export. A PDF too large to hand over whole can still
// be handed over in parts: `pages` selects them, and the export is a new PDF
// holding only those pages, not the original bytes.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	// pdfcpu holds the whole object graph in memory, so the source is capped
	// even though only the output has to fit maxExportBytes.
	maxPDFSourceBytes = 256 << 20
	pdfReadTimeout    = time.Minute
	maxPagesSpecLen   = 200
	maxPageNumber     = 100000
)

func init() {
	// The image is FROM scratch: no home and no config directory. Without this,
	// pdfcpu's internal default configuration tries to create one on disk.
	model.ConfigPath = "disable"
}

// pageRange is an inclusive, 1-based range of pages.
type pageRange struct{ first, last int }

// parsePageSelection accepts "N" and "N-M" separated by commas, ignoring
// spaces, and returns the ranges sorted and merged. pdfcpu's own grammar
// accepts more (negation, even/odd, open ranges); none of that is offered.
func parsePageSelection(spec string) ([]pageRange, error) {
	spec = strings.Join(strings.Fields(spec), "")
	invalid := fmt.Errorf("pages must be page numbers and ranges such as \"1-5,9\", got %q", spec)
	if spec == "" || len(spec) > maxPagesSpecLen {
		return nil, invalid
	}
	var ranges []pageRange
	for _, part := range strings.Split(spec, ",") {
		firstText, lastText, isRange := strings.Cut(part, "-")
		if !isRange {
			lastText = firstText
		}
		first, okFirst := pageNumber(firstText)
		last, okLast := pageNumber(lastText)
		if !okFirst || !okLast || last < first {
			return nil, invalid
		}
		ranges = append(ranges, pageRange{first, last})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].first < ranges[j].first })
	merged := ranges[:1]
	for _, r := range ranges[1:] {
		tail := &merged[len(merged)-1]
		if r.first <= tail.last+1 {
			tail.last = max(tail.last, r.last)
			continue
		}
		merged = append(merged, r)
	}
	return merged, nil
}

func pageNumber(text string) (int, bool) {
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(text)
	return n, err == nil && n >= 1 && n <= maxPageNumber
}

func formatPageSelection(ranges []pageRange) string {
	parts := make([]string, len(ranges))
	for i, r := range ranges {
		parts[i] = strconv.Itoa(r.first)
		if r.last != r.first {
			parts[i] += "-" + strconv.Itoa(r.last)
		}
	}
	return strings.Join(parts, ",")
}

// pagesFilename names a page export after its source, e.g.
// "Report (pages 1-5,9).pdf", shortening the base to stay within 255 bytes.
func pagesFilename(source string, ranges []pageRange) string {
	suffix := " (pages " + formatPageSelection(ranges) + ").pdf"
	base := strings.TrimSuffix(source, filepath.Ext(source))
	for len(base)+len(suffix) > 255 {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	return strings.TrimSpace(base) + suffix
}

// isPDF looks for the header within the first KiB, where the PDF
// specification lets readers find it, instead of trusting the file name.
func isPDF(root *os.Root, rel string) bool {
	file, err := root.Open(rel)
	if err != nil {
		return false
	}
	defer file.Close()
	head := make([]byte, 1024)
	n, _ := io.ReadFull(file, head)
	return bytes.Contains(head[:n], []byte("%PDF-"))
}

// readPDF parses a PDF inside root. Malformed input can make pdfcpu panic
// outside its own recovery, and this server has no other recover: a panic here
// would end the stdio process, so it becomes an error instead.
func readPDF(root *os.Root, rel string) (pdf *model.Context, err error) {
	defer func() {
		if r := recover(); r != nil {
			pdf, err = nil, fmt.Errorf("the PDF could not be read: %v", r)
		}
	}()
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
		return nil, errors.New("only regular files can be exported")
	}
	if info.Size() > maxPDFSourceBytes {
		return nil, fmt.Errorf("the PDF is %d bytes; pages can be selected from PDFs of at most %d MiB", info.Size(), maxPDFSourceBytes>>20)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfReadTimeout)
	defer cancel()
	pdf, err = api.ReadValidateAndOptimize(ctx, file, model.NewStatelessConfiguration(), nil)
	if err != nil {
		return nil, fmt.Errorf("the PDF could not be read: %w", err)
	}
	return pdf, nil
}

// extractPDFPages writes the selected pages of root/rel to root/target and
// returns the source's page count.
func extractPDFPages(root *os.Root, rel, target string, ranges []pageRange) (pageCount int, err error) {
	pdf, err := readPDF(root, rel)
	if err != nil {
		return 0, err
	}
	pageCount = pdf.PageCount
	var pages []int
	for _, r := range ranges {
		if r.last > pageCount {
			return pageCount, fmt.Errorf("pages %s: this PDF has %d pages", formatPageSelection(ranges), pageCount)
		}
		for page := r.first; page <= r.last; page++ {
			pages = append(pages, page)
		}
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the selected pages could not be extracted: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), pdfReadTimeout)
	defer cancel()
	extracted, err := pdfcpu.ExtractPages(ctx, pdf, pages, false)
	if err != nil {
		return pageCount, fmt.Errorf("the selected pages could not be extracted: %w", err)
	}
	var out bytes.Buffer
	if err := api.WriteContext(ctx, extracted, &out); err != nil {
		return pageCount, fmt.Errorf("the selected pages could not be written: %w", err)
	}
	if out.Len() == 0 {
		return pageCount, errors.New("the selected pages produced an empty PDF")
	}
	return pageCount, root.WriteFile(target, out.Bytes(), 0600)
}

// pdfPageCount reports the page count of a PDF for the size error, so the
// caller can choose ranges. ok is false for anything that is not a readable PDF.
func pdfPageCount(root *os.Root, rel string) (count int, ok bool) {
	if !isPDF(root, rel) {
		return 0, false
	}
	pdf, err := readPDF(root, rel)
	if err != nil {
		return 0, false
	}
	return pdf.PageCount, true
}
