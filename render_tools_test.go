package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pb/service"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
	"google.golang.org/grpc"
)

// showFixture serves ObjectShow with one text block whose text a test can
// change, and a toggle.
type showFixture struct {
	service.UnimplementedClientCommandsServer
	text string
}

func (f *showFixture) ObjectShow(_ context.Context, request *pb.RpcObjectShowRequest) *pb.RpcObjectShowResponse {
	if request.ObjectId != "o" {
		return &pb.RpcObjectShowResponse{Error: &pb.RpcObjectShowResponseError{Code: pb.RpcObjectShowResponseError_NOT_FOUND, Description: "object not found"}}
	}
	text := func(id, body string, style model.BlockContentTextStyle) *model.Block {
		return &model.Block{Id: id, Content: &model.BlockContentOfText{Text: &model.BlockContentText{Text: body, Style: style}}}
	}
	return &pb.RpcObjectShowResponse{ObjectView: &model.ObjectView{RootId: "o", Blocks: []*model.Block{
		{Id: "o", ChildrenIds: []string{"t", "g"}, Content: &model.BlockContentOfSmartblock{Smartblock: &model.BlockContentSmartblock{}}},
		text("t", f.text, model.BlockContentText_Paragraph),
		withChildren(text("g", "fold", model.BlockContentText_Toggle), "gc"),
		text("gc", "inside", model.BlockContentText_Paragraph),
	}}}
}

func withChildren(b *model.Block, ids ...string) *model.Block {
	b.ChildrenIds = ids
	return b
}

// reasons is incomplete_reasons joined, for Contains checks.
func reasons(res nativeToolResult) string {
	list, _ := res.Metadata["incomplete_reasons"].([]string)
	return strings.Join(list, " | ")
}

// renderTestServer is an MCP server with a heart fixture and a renderer URL.
func renderTestServer(t *testing.T, renderURL string) (*mcpServer, *showFixture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &showFixture{text: "first"}
	backend := grpc.NewServer()
	service.RegisterClientCommandsServer(backend, fixture)
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(backend.Stop)
	return &mcpServer{cfg: serverConfig{grpcAddr: listener.Addr().String(), token: "test-session", timeout: 5 * time.Second,
		outRoot: t.TempDir(), renderURL: renderURL}}, fixture
}

// fakeRenderer stands in for the anytype-render daemon and counts calls.
func fakeRenderer(t *testing.T, status int, reply map[string]any) (*httptest.Server, *int, *map[string]any) {
	t.Helper()
	calls := 0
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/render" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&last)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &last
}

func renderReply() map[string]any {
	return map[string]any{
		"image": base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0 fake jpeg")),
		"mime":  "image/jpeg", "width": 1280, "height": 900, "theme": "light",
		"complete": true, "unsettled": []any{}, "ms": 2400,
		"renderer": map[string]any{"frontend": "v0.56.1", "middleware": "0.50.18"},
		"layout": map[string]any{
			"layout": "edit", "content_width_px": 704, "content_height_px": 850, "blocks_in_dom": 12,
			"tables":         []any{map[string]any{"block_id": "t1", "column_widths_px": []any{120, 360, 90}, "cells_wrapped": 1}},
			"dataviews":      []any{},
			"clipped_blocks": []any{}, "images_failed": []any{}, "images_loading": []any{}, "toggles_not_open": []any{},
		},
		"rpc": map[string]any{"refused": []any{}},
	}
}

func TestObjectRenderNotConfigured(t *testing.T) {
	server := &mcpServer{cfg: serverConfig{outRoot: t.TempDir()}}
	_, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o"})
	if err == nil || !strings.Contains(err.Error(), "ANYTYPE_RENDER_URL") {
		t.Fatalf("err = %v, want a pointer to ANYTYPE_RENDER_URL", err)
	}
}

func TestObjectRenderExportsImageAndReport(t *testing.T) {
	daemon, calls, last := fakeRenderer(t, http.StatusOK, renderReply())
	server, fixture := renderTestServer(t, daemon.URL)

	res, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "width": float64(1280), "theme": "dark", "expand_toggles": true})
	if err != nil {
		t.Fatal(err)
	}
	if (*last)["theme"] != "dark" || (*last)["full_height"] != true || (*last)["format"] != "jpeg" {
		t.Errorf("request = %v", *last)
	}
	if ids, _ := (*last)["expand_ids"].([]any); len(ids) != 1 || ids[0] != "g" {
		t.Errorf("expand_ids = %v, want the toggle", (*last)["expand_ids"])
	}
	if len(res.Content) != 1 || res.Content[0]["type"] != "resource_link" {
		t.Fatalf("content = %v, want one resource_link", res.Content)
	}
	layout := res.Metadata["layout"].(map[string]any)
	if _, ok := layout["tables"]; !ok {
		t.Errorf("layout lost its tables: %v", layout)
	}
	for _, empty := range []string{"clipped_blocks", "images_failed", "images_loading", "toggles_not_open", "dataviews", "blocks_in_dom"} {
		if _, ok := layout[empty]; ok {
			t.Errorf("layout keeps %s although it says nothing: %v", empty, layout)
		}
	}
	if _, ok := res.Metadata["warnings"]; ok {
		t.Errorf("warnings without a refused call: %v", res.Metadata["warnings"])
	}
	// Toggles opened, nothing below, beside or behind Load more.
	if res.Metadata["complete"] != true || res.Metadata["incomplete_reasons"] != nil {
		t.Errorf("complete = %v, reasons = %v; want true and none", res.Metadata["complete"], res.Metadata["incomplete_reasons"])
	}

	// ChatGPT's repeat of the same call is answered without a second render.
	same := map[string]any{"space_id": "s", "object_id": "o", "width": float64(1280), "theme": "dark", "expand_toggles": true}
	again, err := server.toolObjectRender(same)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || again.Metadata["uri"] != res.Metadata["uri"] {
		t.Errorf("repeat rendered again (calls=%d) or changed the link", *calls)
	}
	// Render, fix, render: a changed object must never get the old picture.
	fixture.text = "fixed"
	if _, err := server.toolObjectRender(same); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Errorf("an edited object was answered from memory, calls=%d", *calls)
	}
	if _, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "theme": "light"}); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Errorf("different arguments must render again, calls=%d", *calls)
	}
}

func TestObjectRenderReportsRefusedCalls(t *testing.T) {
	reply := renderReply()
	reply["rpc"] = map[string]any{"refused": []any{"BlockDataviewViewSetActive"}}
	daemon, _, _ := fakeRenderer(t, http.StatusOK, reply)
	server, _ := renderTestServer(t, daemon.URL)
	res, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o"})
	if err != nil {
		t.Fatal(err)
	}
	warnings, _ := res.Metadata["warnings"].([]string)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "BlockDataviewViewSetActive") {
		t.Errorf("warnings = %v", res.Metadata["warnings"])
	}
}

func TestObjectRenderPassesDaemonErrors(t *testing.T) {
	daemon, calls, _ := fakeRenderer(t, http.StatusNotFound, map[string]any{"error": "the object did not open"})
	server, _ := renderTestServer(t, daemon.URL)
	_, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o"})
	if err == nil || !strings.Contains(err.Error(), "did not open") {
		t.Fatalf("err = %v", err)
	}
	// A wrong id fails on reading the object, before any browser run.
	before := *calls
	if _, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "missing"}); err == nil {
		t.Error("a missing object was rendered")
	}
	if *calls != before {
		t.Error("the renderer was called for an object that does not exist")
	}
	for _, bad := range []map[string]any{
		{"space_id": "s", "object_id": "o", "width": float64(100)},
		{"space_id": "s", "object_id": "o", "theme": "sepia"},
		{"space_id": "s", "object_id": "o", "format": "gif"},
	} {
		if _, err := server.toolObjectRender(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestObjectRenderFlagsWhatIsMissing(t *testing.T) {
	reply := renderReply()
	reply["complete"] = false
	reply["unsettled"] = []any{"images"}
	reply["layout"].(map[string]any)["toggles_not_open"] = []any{"g"}
	daemon, _, _ := fakeRenderer(t, http.StatusOK, reply)
	server, _ := renderTestServer(t, daemon.URL)

	res, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "expand_toggles": true})
	if err != nil {
		t.Fatal(err)
	}
	got := reasons(res)
	if res.Metadata["complete"] != false || !strings.Contains(got, "continues below") || !strings.Contains(got, "toggles that did not open: 1") {
		t.Errorf("complete = %v, reasons = %q", res.Metadata["complete"], got)
	}
	if w := strings.Join(res.Metadata["warnings"].([]string), " | "); !strings.Contains(w, "still loading (images)") || strings.Contains(w, "toggle") {
		t.Errorf("warnings = %q: loading belongs there, toggles in the reasons", w)
	}

	// A page opens with its toggles closed: hidden content, and how to show it.
	res, err = server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "theme": "dark"})
	if err != nil {
		t.Fatal(err)
	}
	if got := reasons(res); !strings.Contains(got, "closed toggles with content: 1 (expand_toggles)") {
		t.Errorf("reasons = %q", got)
	}
}

func TestObjectRenderSaysWhatIsCutOffAtTheSide(t *testing.T) {
	reply := renderReply()
	layout := reply["layout"].(map[string]any)
	layout["tables"] = []any{map[string]any{"block_id": "t1", "cut_off_px": 214}}
	layout["dataviews"] = []any{map[string]any{"block_id": "dv1", "cut_off_px": 175, "rows_total": 3, "rows_loaded": 3}}
	layout["clipped_blocks"] = []any{"t"}
	daemon, _, _ := fakeRenderer(t, http.StatusOK, reply)
	server, _ := renderTestServer(t, daemon.URL)
	res, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "expand_toggles": true})
	if err != nil {
		t.Fatal(err)
	}
	got := reasons(res)
	for _, want := range []string{"tables and views cut off at the side: 2, by up to 214 px", "text blocks running past the side: 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("reasons %q lack %q", got, want)
		}
	}
	if res.Metadata["complete"] != false || strings.Contains(got, "rows") || strings.Contains(got, "below") {
		t.Errorf("complete = %v, reasons = %q", res.Metadata["complete"], got)
	}
}

func TestFoldedTogglesCountsOnlyWhatHidesSomething(t *testing.T) {
	blocks := []anytypefiles.BlockInfo{
		{ID: "o", ChildrenIDs: []string{"a", "b"}},
		{ID: "a", Kind: "text", Style: "toggle", ChildrenIDs: []string{"a1"}},
		{ID: "a1", Kind: "text", Style: "toggle_header2", ChildrenIDs: []string{"a2"}},
		{ID: "a2", Kind: "text"},
		{ID: "b", Kind: "text", Style: "toggle"},
	}
	all := map[string]bool{"a": true, "a1": true, "b": true}
	if n := foldedToggles(blocks, all); n != 1 {
		t.Errorf("all closed: %d, want 1 (a; a1 is inside it, b is empty)", n)
	}
	if n := foldedToggles(blocks, map[string]bool{"a1": true}); n != 1 {
		t.Errorf("only the inner one closed: %d, want 1", n)
	}
	if ids := toggleIDs(blocks); len(ids) != 3 {
		t.Errorf("toggleIDs = %v", ids)
	}
}

func TestObjectRenderOlderDaemonWithoutCompleteness(t *testing.T) {
	reply := renderReply()
	delete(reply, "complete")
	delete(reply, "unsettled")
	daemon, _, _ := fakeRenderer(t, http.StatusOK, reply)
	server, _ := renderTestServer(t, daemon.URL)
	res, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Metadata["complete"]; ok {
		t.Errorf("complete invented for a daemon that does not measure it: %v", res.Metadata)
	}
}

func TestObjectRenderReportOnlyAndAllRows(t *testing.T) {
	reply := renderReply()
	reply["image"] = ""
	reply["complete"] = false
	reply["layout"].(map[string]any)["dataviews"] = []any{
		map[string]any{"block_id": "dv1", "kind": "query", "rows_shown": 10, "rows_loaded": 10, "rows_total": 63},
		map[string]any{"block_id": "dataview", "kind": "query", "rows_shown": 30, "rows_loaded": 30, "rows_total": 30},
		map[string]any{"block_id": "dv2", "kind": "query", "rows_shown": 500, "rows_loaded": 500, "rows_total": 800},
	}
	daemon, calls, last := fakeRenderer(t, http.StatusOK, reply)
	server, _ := renderTestServer(t, daemon.URL)

	res, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "image": false, "full_height": false})
	if err != nil {
		t.Fatal(err)
	}
	if (*last)["image"] != false || (*last)["all_rows"] != false {
		t.Errorf("request = %v", *last)
	}
	if len(res.Content) != 0 || res.Metadata["uri"] != nil {
		t.Errorf("report only, yet content = %v, uri = %v", res.Content, res.Metadata["uri"])
	}
	if got := reasons(res); !strings.Contains(got, "only the first screen") {
		t.Errorf("reasons = %q", got)
	}

	res, err = server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "image": false})
	if err != nil {
		t.Fatal(err)
	}
	// Rows not loaded count, rows loaded but below the picture do not.
	if got := reasons(res); !strings.Contains(got, "rows behind Load more in embedded views: 353 (all_rows)") {
		t.Errorf("reasons = %q", got)
	}
	res, err = server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "image": false, "all_rows": true})
	if err != nil {
		t.Fatal(err)
	}
	if (*last)["all_rows"] != true || *calls != 3 {
		t.Errorf("all_rows not passed on, or answered from memory: %v, calls=%d", *last, *calls)
	}
	got := reasons(res)
	for _, want := range []string{"rows beyond the 500 loaded per view: 300", "rows that did not load in time: 53"} {
		if !strings.Contains(got, want) {
			t.Errorf("reasons %q lack %q", got, want)
		}
	}
	if strings.Contains(got, "Load more") {
		t.Errorf("Load more named although all_rows was set: %q", got)
	}
}

func TestObjectRenderForgetsRendersAfterAWrite(t *testing.T) {
	daemon, calls, _ := fakeRenderer(t, http.StatusOK, renderReply())
	server, _ := renderTestServer(t, daemon.URL)
	render := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"object-render","arguments":{"space_id":"s","object_id":"o"}}}`
	read := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"file-list-output","arguments":{}}}`
	// Fails for want of arguments, but a write was attempted: rows of an
	// embedded query may have changed although the page's blocks did not.
	write := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"update-object-compact","arguments":{}}}`
	runMessages(t, server, render, read, render)
	if *calls != 1 {
		t.Fatalf("a read between two renders made the second render again, calls=%d", *calls)
	}
	runMessages(t, server, write, render)
	if *calls != 2 {
		t.Errorf("a render after a write came from memory, calls=%d", *calls)
	}
}

func TestObjectRenderPassesViews(t *testing.T) {
	daemon, calls, last := fakeRenderer(t, http.StatusOK, renderReply())
	server, _ := renderTestServer(t, daemon.URL)

	args := map[string]any{"space_id": "s", "object_id": "o", "views": []any{
		map[string]any{"block_id": "dv1", "view": " Kalender "},
		map[string]any{"view": "v2"},
	}}
	if _, err := server.toolObjectRender(args); err != nil {
		t.Fatal(err)
	}
	sent, _ := (*last)["views"].([]any)
	if len(sent) != 2 || sent[0].(map[string]any)["view"] != "Kalender" || sent[0].(map[string]any)["block_id"] != "dv1" || sent[1].(map[string]any)["block_id"] != "" {
		t.Errorf("views sent = %v", (*last)["views"])
	}
	// Another view is another picture, not a repeat.
	if _, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o"}); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Errorf("a render with other views was answered from memory, calls=%d", *calls)
	}
	for _, bad := range []any{"Kalender", []any{"Kalender"}, []any{map[string]any{"block_id": "dv1"}}} {
		if _, err := server.toolObjectRender(map[string]any{"space_id": "s", "object_id": "o", "views": bad}); err == nil {
			t.Errorf("views %v accepted", bad)
		}
	}
}
