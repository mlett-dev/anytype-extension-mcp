package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// testPDF builds a valid PDF whose page N shows "(Page N)". padding adds a
// comment of that many bytes to the content stream of page paddedPage.
func testPDF(pages, paddedPage, padding int) []byte {
	var buf bytes.Buffer
	var offsets []int
	object := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	buf.WriteString("%PDF-1.7\n")
	kids := make([]string, pages)
	for i := range kids {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	object("<< /Type /Catalog /Pages 2 0 R >>")
	object(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages))
	object("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i := 1; i <= pages; i++ {
		object(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*(i-1)))
		content := fmt.Sprintf("BT /F1 24 Tf 72 700 Td (Page %d) Tj ET\n", i)
		if i == paddedPage {
			content += "%" + strings.Repeat("x", padding) + "\n"
		}
		object(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return buf.Bytes()
}

func pdfPages(t *testing.T, data []byte) int {
	t.Helper()
	count, err := api.PageCount(context.Background(), bytes.NewReader(data), model.NewStatelessConfiguration())
	if err != nil {
		t.Fatalf("export is not a readable PDF: %v", err)
	}
	return count
}

func TestParsePageSelection(t *testing.T) {
	for spec, want := range map[string]string{
		"1-5,9": "1-5,9", " 9 , 1-5 ": "1-5,9", "3,1-2,2-4": "1-4", "7": "7", "4-4,5": "4-5", "1-3,3-1000": "1-1000",
	} {
		ranges, err := parsePageSelection(spec)
		if err != nil || formatPageSelection(ranges) != want {
			t.Errorf("%q: got %q, %v; want %q", spec, formatPageSelection(ranges), err, want)
		}
	}
	for _, spec := range []string{"", " ", "0", "5-3", "1-", "-3", "even", "odd", "!2", "l", "1,,2", "+1", "1-2-3", "1.5", "100001", strings.Repeat("1,", 101) + "1"} {
		if _, err := parsePageSelection(spec); err == nil {
			t.Errorf("%q accepted", spec)
		}
	}
}

func TestPagesFilenameStaysWithinLimit(t *testing.T) {
	ranges := []pageRange{{1, 5}, {9, 9}}
	if got := pagesFilename("Report.pdf", ranges); got != "Report (pages 1-5,9).pdf" {
		t.Fatalf("got %q", got)
	}
	long := pagesFilename(strings.Repeat("ä", 200)+".pdf", ranges)
	if len(long) > 255 || validateExportFilename(long) != nil || !strings.HasSuffix(long, " (pages 1-5,9).pdf") {
		t.Fatalf("long name not shortened validly: %d bytes", len(long))
	}
}

func TestFileExportPDFPages(t *testing.T) {
	files := map[string][]byte{
		"report.pdf":  testPDF(12, 0, 0),
		"scan.pdf":    testPDF(3, 2, maxExportBytes),
		"broken.pdf":  []byte("%PDF-1.7\nthis is not a PDF body\n%%EOF\n"),
		"picture.png": {137, 80, 78, 71, 13, 10, 26, 10, 1, 2, 3},
	}
	server := exportTestServer(t, files)
	call := func(args map[string]any) map[string]any {
		t.Helper()
		response := testRPC(t, server, "tools/call", map[string]any{"name": "file-export", "arguments": args})
		if response.Error != nil {
			t.Fatal(response.Error)
		}
		return response.Result.(map[string]any)
	}
	exported := func(result map[string]any) ([]byte, map[string]any) {
		t.Helper()
		if result["isError"] != false {
			t.Fatalf("export failed: %#v", result)
		}
		meta := result["structuredContent"].(map[string]any)
		data, mimeType := readExportBytes(t, server, meta["uri"].(string))
		if mimeType != "application/pdf" {
			t.Fatalf("page export has MIME type %q", mimeType)
		}
		return data, meta
	}

	data, meta := exported(call(map[string]any{"object_id": "report.pdf", "pages": "9, 1-2"}))
	if meta["pages"] != "1-2,9" || meta["source_page_count"] != 12 || meta["filename"] != "report (pages 1-2,9).pdf" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if pdfPages(t, data) != 3 || !bytes.Contains(data, []byte("(Page 9)")) || bytes.Contains(data, []byte("(Page 5)")) {
		t.Fatal("export does not hold exactly the selected pages")
	}
	// pdfcpu writes different bytes each run, so a repeated page export must be
	// recognised by source and selection, or the cache fills up again.
	again := call(map[string]any{"object_id": "report.pdf", "pages": "1-2,9"})["structuredContent"].(map[string]any)
	if again["uri"] != meta["uri"] || again["pages"] != "1-2,9" || again["source_page_count"] != 12 {
		t.Fatalf("repeating a page export did not return the existing link: %#v", again)
	}
	for _, other := range []map[string]any{
		{"object_id": "report.pdf", "pages": "1-2"},
		{"object_id": "report.pdf", "filename": "report (pages 1-2,9).pdf"},
	} {
		if call(other)["structuredContent"].(map[string]any)["uri"] == meta["uri"] {
			t.Fatalf("%v reused the page export", other)
		}
	}

	// The use case: a PDF too large as a whole names its page count, and a
	// page range of it fits.
	whole := call(map[string]any{"object_id": "scan.pdf"})
	encoded, _ := json.Marshal(whole)
	if whole["isError"] != true || !bytes.Contains(encoded, []byte("This PDF has 3 pages")) {
		t.Fatalf("oversized PDF error does not name the page count: %s", encoded)
	}
	data, _ = exported(call(map[string]any{"object_id": "scan.pdf", "pages": "3", "filename": "Seite 3.pdf"}))
	if pdfPages(t, data) != 1 || !bytes.Contains(data, []byte("(Page 3)")) {
		t.Fatal("small page of a large PDF was not exported")
	}
	// An extract over the limit must point to a smaller selection, not to the
	// dead end of "larger-file transfers are not implemented".
	tooBig := call(map[string]any{"object_id": "scan.pdf", "pages": "2"})
	encoded, _ = json.Marshal(tooBig)
	if tooBig["isError"] != true || !bytes.Contains(encoded, []byte("select fewer of them")) || !bytes.Contains(encoded, []byte("has 3 pages")) || bytes.Contains(encoded, []byte("not implemented")) {
		t.Fatalf("oversized extract not explained: %s", encoded)
	}

	for args, want := range map[string]map[string]any{
		"has 12 pages":      {"object_id": "report.pdf", "pages": "11-13"},
		"not a PDF":         {"object_id": "picture.png", "pages": "1"},
		"could not be read": {"object_id": "broken.pdf", "pages": "1"},
		"pages must be":     {"object_id": "report.pdf", "pages": "even"},
	} {
		result := call(want)
		encoded, _ := json.Marshal(result)
		if result["isError"] != true || !bytes.Contains(encoded, []byte(args)) {
			t.Errorf("%v: expected error containing %q, got %s", want, args, encoded)
		}
	}
	// A plain PNG still says that larger transfers are not implemented,
	// without a page hint.
	if err := (&exportTooLargeError{size: maxExportBytes + 1}).Error(); strings.Contains(err, "pages") {
		t.Fatalf("non-PDF size error mentions pages: %s", err)
	}

	many := testRPC(t, server, "tools/call", map[string]any{"name": "file-export-many", "arguments": map[string]any{"items": []any{
		map[string]any{"object_id": "report.pdf", "pages": "1"}, map[string]any{"object_id": "report.pdf", "pages": "x"},
	}}}).Result.(map[string]any)
	if many["isError"] != true {
		t.Fatal("file-export-many accepted a malformed pages item")
	}
	uri, _ := url.Parse(meta["uri"].(string))
	if uri.Host == "" {
		t.Fatal("no export handle")
	}
}

// Corrupt input must come back as an error. A panic that escaped readPDF or
// extractPDFPages would end the whole stdio server.
func TestPDFPagesSurviveCorruptInput(t *testing.T) {
	source := testPDF(4, 0, 0)
	random := rand.New(rand.NewPCG(1, 2))
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := 0; i < 400; i++ {
		data := bytes.Clone(source)
		switch i % 3 {
		case 0:
			for j := 0; j < 1+random.IntN(8); j++ {
				data[random.IntN(len(data))] = byte(random.IntN(256))
			}
		case 1:
			data = data[:random.IntN(len(data))]
		case 2:
			at := random.IntN(len(data))
			data = append(data[:at:at], data[min(len(data), at+random.IntN(64)):]...)
		}
		if err := root.WriteFile("in.pdf", data, 0600); err != nil {
			t.Fatal(err)
		}
		_, _ = extractPDFPages(root, "in.pdf", "out.pdf", []pageRange{{1, 2}})
		_, _ = pdfPageCount(root, "in.pdf")
	}
}
