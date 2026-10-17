package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// fakeSearchAPI behaves like Anytype's REST search: without types it returns
// only objects, with file types only files.
func fakeSearchAPI(t *testing.T, objects, files int) *mcpServer {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Types []string `json:"types"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		prefix, total := "object", objects
		if len(body.Types) > 0 {
			prefix, total = "file", files
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		data := []any{}
		for i := offset; i < min(total, offset+limit); i++ {
			data = append(data, map[string]any{"id": fmt.Sprintf("%s-%d", prefix, i), "name": fmt.Sprintf("%s %d", prefix, i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "pagination": map[string]any{"total": total, "offset": offset, "limit": limit, "has_more": offset+len(data) < total}})
	}))
	t.Cleanup(api.Close)
	return &mcpServer{cfg: serverConfig{apiBaseURL: api.URL, apiKey: "test", apiVersion: "test", timeout: 5 * time.Second}}
}

func searchIDs(t *testing.T, server *mcpServer, args map[string]any) ([]string, map[string]any) {
	t.Helper()
	out, err := server.toolSearchCompact(args, false)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, object := range out["objects"].([]map[string]any) {
		ids = append(ids, object["id"].(string))
	}
	return ids, out["pagination"].(map[string]any)
}

func TestSearchWithoutTypesIncludesFiles(t *testing.T) {
	server := fakeSearchAPI(t, 3, 4)
	for _, tc := range []struct {
		offset, limit int
		want          string
	}{
		{0, 50, "[object-0 object-1 object-2 file-0 file-1 file-2 file-3]"},
		{0, 2, "[object-0 object-1]"},
		{2, 2, "[object-2 file-0]"},
		{4, 2, "[file-1 file-2]"},
		{6, 5, "[file-3]"},
		{9, 5, "[]"},
	} {
		ids, pagination := searchIDs(t, server, map[string]any{"query": "x", "offset": tc.offset, "limit": tc.limit})
		if fmt.Sprint(ids) != tc.want && !(len(ids) == 0 && tc.want == "[]") {
			t.Errorf("offset %d limit %d: got %v, want %s", tc.offset, tc.limit, ids, tc.want)
		}
		if pagination["total"] != 7 || pagination["has_more"] != (tc.offset+len(ids) < 7) {
			t.Errorf("offset %d limit %d: pagination %v", tc.offset, tc.limit, pagination)
		}
	}
	if ids, _ := searchIDs(t, server, map[string]any{"query": "x", "types": []any{}}); len(ids) != 7 {
		t.Errorf("an empty types list switched the file search off: %v", ids)
	}
	if ids, _ := searchIDs(t, server, map[string]any{"query": "x", "include_files": false}); fmt.Sprint(ids) != "[object-0 object-1 object-2]" {
		t.Errorf("include_files=false still returned files: %v", ids)
	}
	if ids, _ := searchIDs(t, server, map[string]any{"query": "x", "types": []any{"image"}}); fmt.Sprint(ids) != "[file-0 file-1 file-2 file-3]" {
		t.Errorf("explicit types were not passed through alone: %v", ids)
	}
}
