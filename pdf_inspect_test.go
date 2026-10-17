package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// inspectPDF has a text layer on every page except those in textless; each
// text page reads "Page N: …" with enough characters to count as text.
func inspectPDF(pages int, textless map[int]bool, words int) []byte {
	return buildTestPDF(pages, func(n int) string {
		if textless[n] {
			return "0 0 1 rg 72 72 400 600 re f\n"
		}
		body := "the quick brown fox jumps over the lazy dog"
		if words > 0 {
			body = strings.Repeat("lorem ipsum ", words)
		}
		return fmt.Sprintf("BT /F1 10 Tf 72 700 Td (Page %d: %s) Tj ET\n", n, body)
	})
}

func readPagesFrom(t *testing.T, data []byte) ([]pdfPage, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/in.pdf", data, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return readPDFPages(root, "in.pdf")
}

// The overview must not grow with the page count, even in the worst case of
// pages alternating between text and no text.
func TestPDFInspectOverviewStaysSmall(t *testing.T) {
	textless := map[int]bool{}
	for n := 2; n <= 1000; n += 2 {
		textless[n] = true
	}
	start := time.Now()
	pages, err := readPagesFrom(t, inspectPDF(1000, textless, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read 1000 pages in %s", time.Since(start).Round(time.Millisecond))
	overview := pdfOverview(pages)
	encoded, _ := json.Marshal(overview)
	layer := overview["text_layer"].(map[string]any)
	if len(encoded) > 2048 || overview["ranges_truncated"] != true || layer["count_with_text"] != 500 || layer["count_without_text"] != 500 {
		t.Fatalf("overview of 1000 alternating pages is %d bytes: %s", len(encoded), encoded)
	}

	start = time.Now()
	pages, err = readPagesFrom(t, inspectPDF(500, nil, 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read 500 text pages in %s", time.Since(start).Round(time.Millisecond))
	encoded, _ = json.Marshal(pdfOverview(pages))
	if !bytes.Contains(encoded, []byte(`"with_text":"1-500"`)) || !bytes.Contains(encoded, []byte(`"format":"Letter portrait"`)) || len(encoded) > 400 {
		t.Fatalf("plain overview: %s", encoded)
	}
}

func TestPDFInspectTool(t *testing.T) {
	files := map[string][]byte{
		"doc.pdf":     inspectPDF(12, map[int]bool{6: true}, 0),
		"long.pdf":    inspectPDF(12, nil, 250),
		"many.pdf":    inspectPDF(25, nil, 0),
		"broken.pdf":  []byte("%PDF-1.7\nnot a PDF body\n%%EOF\n"),
		"picture.png": {137, 80, 78, 71, 13, 10, 26, 10, 1, 2, 3},
	}
	server := exportTestServer(t, files)
	call := func(args map[string]any) (map[string]any, string) {
		t.Helper()
		response := testRPC(t, server, "tools/call", map[string]any{"name": "pdf-inspect", "arguments": args})
		if response.Error != nil {
			t.Fatal(response.Error)
		}
		result := response.Result.(map[string]any)
		encoded, _ := json.Marshal(result)
		if result["isError"] == true {
			return nil, string(encoded)
		}
		return result["structuredContent"].(map[string]any), string(encoded)
	}

	out, encoded := call(map[string]any{"object_id": "doc.pdf"})
	layer, _ := out["text_layer"].(map[string]any)
	sizes, _ := out["page_sizes"].([]map[string]any)
	if out["page_count"] != 12 || layer["with_text"] != "1-5,7-12" || layer["without_text"] != "6" || len(sizes) != 1 || sizes[0]["pages"] != "1-12" || out["fits_file_export"] != true || out["pages"] != nil {
		t.Fatalf("overview: %s", encoded)
	}

	out, encoded = call(map[string]any{"object_id": "doc.pdf", "pages": "5-7", "text": true})
	details, _ := out["pages"].([]map[string]any)
	if len(details) != 3 || !strings.HasPrefix(details[0]["text"].(string), "Page 5:") || details[1]["note"] == nil || details[1]["chars"] != 0 || out["truncated"] != false {
		t.Fatalf("text of pages 5-7: %s", encoded)
	}

	out, encoded = call(map[string]any{"object_id": "doc.pdf", "query": "PAGE 1"})
	search, _ := out["search"].(map[string]any)
	if search["match_count"] != 4 || search["pages_with_matches"] != "1,10-12" || search["not_searchable"] != "6" || len(search["matches"].([]map[string]any)) != 4 {
		t.Fatalf("search: %s", encoded)
	}
	out, _ = call(map[string]any{"object_id": "doc.pdf", "query": "page 1", "pages": "1-5"})
	if search := out["search"].(map[string]any); search["match_count"] != 1 || search["not_searchable"] != "" {
		t.Fatalf("search limited to pages 1-5: %v", search)
	}

	// Text budget: pages are returned whole until the next would exceed it.
	out, encoded = call(map[string]any{"object_id": "long.pdf", "pages": "1-12", "text": true})
	details, _ = out["pages"].([]map[string]any)
	total := 0
	for _, d := range details {
		total += utf8.RuneCountInString(d["text"].(string))
	}
	if out["truncated"] != true || total > maxInspectTextRunes || len(details) == 0 || out["next_pages"] != fmt.Sprintf("%d-12", len(details)+1) {
		t.Fatalf("text budget: %d pages, %d runes, next %v", len(details), total, out["next_pages"])
	}
	_ = encoded
	out, _ = call(map[string]any{"object_id": "many.pdf", "pages": "1-25"})
	if details := out["pages"].([]map[string]any); len(details) != maxInspectPages || out["next_pages"] != "21-25" {
		t.Fatalf("page cap: %d pages, next %v", len(details), out["next_pages"])
	}

	for want, args := range map[string]map[string]any{
		"has 12 pages":      {"object_id": "doc.pdf", "pages": "11-13"},
		"not a PDF":         {"object_id": "picture.png"},
		"could not be read": {"object_id": "broken.pdf"},
		"text needs pages":  {"object_id": "doc.pdf", "text": true},
		"pages must be":     {"object_id": "doc.pdf", "pages": "odd"},
		"unknown argument":  {"object_id": "doc.pdf", "format": "png"},
	} {
		if _, encoded := call(args); !strings.Contains(encoded, want) {
			t.Errorf("%v: expected an error containing %q, got %s", args, want, encoded)
		}
	}
}

// A session over the deadline is killed and the engine keeps working, also
// after an idle shutdown.
func TestPDFiumWatchdogAndRestart(t *testing.T) {
	saved := pdfReadDeadline
	t.Cleanup(func() { pdfReadDeadline = saved })
	pdfReadDeadline = time.Millisecond
	start := time.Now()
	if _, err := readPagesFrom(t, inspectPDF(1000, nil, 1)); err == nil || !strings.Contains(err.Error(), "took longer") {
		t.Fatalf("deadline not enforced: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("killed session took %s to return", time.Since(start))
	}
	pdfReadDeadline = saved
	if pages, err := readPagesFrom(t, inspectPDF(3, nil, 2)); err != nil || len(pages) != 3 {
		t.Fatalf("engine did not recover after a kill: %d pages, %v", len(pages), err)
	}
	pdfEngine.closeIdle()
	if pages, err := readPagesFrom(t, inspectPDF(4, nil, 3)); err != nil || len(pages) != 4 {
		t.Fatalf("engine did not restart after idle shutdown: %d pages, %v", len(pages), err)
	}
}

func TestPDFInspectSurvivesCorruptInput(t *testing.T) {
	saved := pdfReadDeadline
	t.Cleanup(func() { pdfReadDeadline = saved })
	pdfReadDeadline = 5 * time.Second
	source := inspectPDF(4, map[int]bool{2: true}, 0)
	random := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 150; i++ {
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
		start := time.Now()
		_, _ = readPagesFrom(t, data)
		if time.Since(start) > 10*time.Second {
			t.Fatalf("mutation %d took %s", i, time.Since(start))
		}
	}
}
