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
		text("g", "fold", model.BlockContentText_Toggle),
	}}}
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
		"renderer": map[string]any{"frontend": "v0.56.1", "middleware": "0.50.18"},
		"layout": map[string]any{
			"layout": "edit", "content_width_px": 704, "height_px": 900, "blocks_in_dom": 12,
			"tables":         []any{map[string]any{"block_id": "t1", "column_widths_px": []any{120, 360, 90}, "cells_wrapped": 1}},
			"clipped_blocks": []any{}, "images_failed": 0,
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
	for _, empty := range []string{"clipped_blocks", "images_failed", "blocks_in_dom"} {
		if _, ok := layout[empty]; ok {
			t.Errorf("layout keeps %s although it says nothing: %v", empty, layout)
		}
	}
	if _, ok := res.Metadata["warnings"]; ok {
		t.Errorf("warnings without a refused call: %v", res.Metadata["warnings"])
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
