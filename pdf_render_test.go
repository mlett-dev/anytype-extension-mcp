package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"
)

// noisyPDF has one page of many small coloured rectangles, which compress
// badly, next to a page that is a single filled rectangle.
func noisyPDF() []byte {
	random := rand.New(rand.NewPCG(5, 6))
	return buildTestPDF(2, func(n int) string {
		if n == 2 {
			return "0 0 1 rg 72 72 400 600 re f\n"
		}
		var b strings.Builder
		for i := 0; i < 4000; i++ {
			fmt.Fprintf(&b, "%.2f %.2f %.2f rg %d %d 6 6 re f\n", random.Float64(), random.Float64(), random.Float64(), random.IntN(600), random.IntN(780))
		}
		return b.String()
	})
}

func renderCall(t *testing.T, server *mcpServer, args map[string]any) (map[string]any, []string, string) {
	t.Helper()
	response := testRPC(t, server, "tools/call", map[string]any{"name": "pdf-render-pages", "arguments": args})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	result := response.Result.(map[string]any)
	encoded, _ := json.Marshal(result)
	if result["isError"] == true {
		return nil, nil, string(encoded)
	}
	var uris []string
	for _, block := range result["content"].([]map[string]any) {
		if block["type"] == "resource_link" {
			uris = append(uris, block["uri"].(string))
		}
	}
	return result["structuredContent"].(map[string]any), uris, string(encoded)
}

func imageAt(t *testing.T, server *mcpServer, uri string) (string, int, int, int) {
	t.Helper()
	data, mimeType := readExportBytes(t, server, uri)
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s is not a decodable image: %v", uri, err)
	}
	if "image/"+format != mimeType {
		t.Fatalf("%s: decoded %s but served as %s", uri, format, mimeType)
	}
	return format, config.Width, config.Height, len(data)
}

func TestPDFRenderPages(t *testing.T) {
	server := exportTestServer(t, map[string][]byte{
		"doc.pdf":     inspectPDF(12, map[int]bool{6: true}, 0),
		"many.pdf":    inspectPDF(25, nil, 0),
		"picture.png": {137, 80, 78, 71, 13, 10, 26, 10, 1, 2, 3},
	})

	out, uris, encoded := renderCall(t, server, map[string]any{"object_id": "doc.pdf", "pages": "3,1-2"})
	if len(uris) != 3 || out["rendered"] != 3 || out["page_count"] != 12 || out["truncated"] != false {
		t.Fatalf("render of 3 pages: %s", encoded)
	}
	results := out["results"].([]map[string]any)
	for i, uri := range uris {
		// Letter at 150 DPI; PDFium rounds the height up.
		if format, w, h, _ := imageAt(t, server, uri); format != "jpeg" || w != 1275 || (h != 1650 && h != 1651) {
			t.Fatalf("page %d: %s %dx%d", i+1, format, w, h)
		}
		if results[i]["page"] != i+1 || results[i]["dpi"] != 150 || results[i]["width_px"] != 1275 || results[i]["filename"] != fmt.Sprintf("doc (page %d).jpg", i+1) {
			t.Fatalf("result %d: %#v", i, results[i])
		}
	}

	// ChatGPT sends every call twice: the second must not render again, nor
	// even start PDFium.
	sessions := pdfEngine.sessions.Load()
	_, again, _ := renderCall(t, server, map[string]any{"object_id": "doc.pdf", "pages": "1-3"})
	if fmt.Sprint(again) != fmt.Sprint(uris) || pdfEngine.sessions.Load() != sessions {
		t.Fatalf("repeated render: links %v, %d new PDFium sessions", again, pdfEngine.sessions.Load()-sessions)
	}
	// Another DPI or format is another image.
	_, other, _ := renderCall(t, server, map[string]any{"object_id": "doc.pdf", "pages": "1", "dpi": 100})
	if len(other) != 1 || other[0] == uris[0] {
		t.Fatal("a different DPI reused the 150 DPI image")
	}

	_, uris, encoded = renderCall(t, server, map[string]any{"object_id": "doc.pdf", "pages": "2", "format": "png", "dpi": 300})
	if len(uris) != 1 {
		t.Fatalf("png render: %s", encoded)
	}
	if format, w, h, _ := imageAt(t, server, uris[0]); format != "png" || w != 2550 || (h != 3300 && h != 3301) {
		t.Fatalf("png at 300 DPI: %s %dx%d", format, w, h)
	}

	out, _, encoded = renderCall(t, server, map[string]any{"object_id": "doc.pdf", "pages": "5-7", "include_text": true})
	results, _ = out["results"].([]map[string]any)
	if len(results) != 3 || !strings.HasPrefix(results[0]["text"].(string), "Page 5:") || results[1]["note"] == nil {
		t.Fatalf("include_text: %s", encoded)
	}

	out, uris, _ = renderCall(t, server, map[string]any{"object_id": "many.pdf", "pages": "1-25", "dpi": 50})
	if len(uris) != maxRenderPages || out["next_pages"] != "21-25" || out["truncated"] != true {
		t.Fatalf("page cap: %d links, next %v", len(uris), out["next_pages"])
	}

	for want, args := range map[string]map[string]any{
		"has 12 pages":      {"object_id": "doc.pdf", "pages": "12-13"},
		"not a PDF":         {"object_id": "picture.png", "pages": "1"},
		"dpi must be":       {"object_id": "doc.pdf", "pages": "1", "dpi": 30},
		"format must be":    {"object_id": "doc.pdf", "pages": "1", "format": "gif"},
		"pages is required": {"object_id": "doc.pdf"},
	} {
		if _, _, encoded := renderCall(t, server, args); !strings.Contains(encoded, want) {
			t.Errorf("%v: expected an error containing %q, got %s", args, want, encoded)
		}
	}
}

// The limit applies to each image: a page over it is rendered at a lower
// DPI, and a page that cannot fit fails alone.
func TestPDFRenderFitsEachImageIntoTheLimit(t *testing.T) {
	savedDeadline := renderPageDeadline
	t.Cleanup(func() { renderPageDeadline = savedDeadline })
	renderPageDeadline = 5 * time.Minute
	data := noisyPDF()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/noisy.pdf", data, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	staged := &stagedFile{dir: dir, root: root, rel: "noisy.pdf", name: "noisy.pdf"}
	pages, err := readPDFPages(root, "noisy.pdf")
	if err != nil {
		t.Fatal(err)
	}
	noisy, _, _, _, err := renderPageImage(staged, pages[0], 1, "png", minRenderDPI)
	if err != nil {
		t.Fatal(err)
	}
	solid, _, _, _, err := renderPageImage(staged, pages[1], 2, "png", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(solid) >= len(noisy) {
		t.Fatalf("fixture does not separate: solid page at 300 DPI %d bytes, noisy at 50 DPI %d bytes", len(solid), len(noisy))
	}

	saved := maxRenderImageBytes
	t.Cleanup(func() { maxRenderImageBytes = saved })
	maxRenderImageBytes = int64(len(noisy) - 1)
	server := exportTestServer(t, map[string][]byte{"noisy.pdf": data})
	out, uris, encoded := renderCall(t, server, map[string]any{"object_id": "noisy.pdf", "pages": "1-2", "format": "png", "dpi": 300})
	results, _ := out["results"].([]map[string]any)
	if len(uris) != 1 || out["failed"] != 1 || len(results) != 2 || !strings.Contains(results[0]["error"].(string), "even at 50 DPI") {
		t.Fatalf("expected page 1 to fail and page 2 to render: %s", encoded)
	}
	if _, _, _, size := imageAt(t, server, uris[0]); int64(size) > maxRenderImageBytes {
		t.Fatalf("image of %d bytes over the %d-byte limit", size, maxRenderImageBytes)
	}

	// With the limit just under the 300 DPI size of the plain page, it is
	// rendered again at a lower DPI. A fresh server has an empty export cache,
	// so the 300 DPI image from above is not simply reused.
	maxRenderImageBytes = int64(len(solid) - 1)
	server = exportTestServer(t, map[string][]byte{"noisy.pdf": data})
	out, uris, encoded = renderCall(t, server, map[string]any{"object_id": "noisy.pdf", "pages": "2", "format": "png", "dpi": 300})
	results, _ = out["results"].([]map[string]any)
	if len(uris) != 1 || results[0]["dpi_reduced"] != true || results[0]["dpi"].(int) >= 300 {
		t.Fatalf("DPI was not reduced to fit: %s", encoded)
	}
}
