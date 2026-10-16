package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pb/service"
	"google.golang.org/grpc"
)

func testRPC(t *testing.T, server *mcpServer, method string, params any) rpcResponse {
	t.Helper()
	id := json.RawMessage("1")
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	response, send := server.handleRequest(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: encoded})
	if !send {
		t.Fatalf("%s did not return a response", method)
	}
	return response
}

func TestInitializeResourceProtocol(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			server := &mcpServer{}
			response := testRPC(t, server, "initialize", map[string]any{"protocolVersion": version})
			if response.Error != nil {
				t.Fatal(response.Error)
			}
			result := response.Result.(map[string]any)
			want := "2025-11-25"
			if version == "2025-06-18" {
				want = version
			}
			if result["protocolVersion"] != want {
				t.Fatalf("negotiated %v, want %s", result["protocolVersion"], want)
			}
			capabilities := result["capabilities"].(map[string]any)
			if resource, ok := capabilities["resources"].(map[string]any); !ok || len(resource) != 0 {
				t.Fatalf("unexpected resources capability: %#v", capabilities)
			}
		})
	}
	for _, params := range []any{nil, map[string]any{}, map[string]any{"protocolVersion": 42}} {
		response := testRPC(t, &mcpServer{}, "initialize", params)
		if response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("invalid initialize accepted: %#v", response)
		}
	}
}

type downloadFixture struct {
	service.UnimplementedClientCommandsServer
	files map[string][]byte
}

func (f *downloadFixture) FileDownload(_ context.Context, request *pb.RpcFileDownloadRequest) *pb.RpcFileDownloadResponse {
	path := filepath.Join(request.Path, request.ObjectId)
	if err := os.WriteFile(path, f.files[request.ObjectId], 0600); err != nil {
		return &pb.RpcFileDownloadResponse{Error: &pb.RpcFileDownloadResponseError{Code: pb.RpcFileDownloadResponseError_UNKNOWN_ERROR, Description: err.Error()}}
	}
	return &pb.RpcFileDownloadResponse{LocalPath: path}
}

func exportTestServer(t *testing.T, files map[string][]byte) *mcpServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := grpc.NewServer()
	service.RegisterClientCommandsServer(backend, &downloadFixture{files: files})
	go func() { _ = backend.Serve(listener) }()
	t.Cleanup(backend.Stop)
	root := t.TempDir()
	return &mcpServer{cfg: serverConfig{grpcAddr: listener.Addr().String(), token: "test-session", timeout: 5 * time.Second, outRoot: root, serverOutRoot: root}}
}

func exportURI(t *testing.T, server *mcpServer, objectID, filename string) string {
	t.Helper()
	result := testRPC(t, server, "tools/call", map[string]any{"name": "file-export", "arguments": map[string]any{"object_id": objectID, "filename": filename}})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	payload := result.Result.(map[string]any)
	if payload["isError"] != false {
		t.Fatalf("export failed: %#v", payload)
	}
	metadata := payload["structuredContent"].(map[string]any)
	var uri string
	for _, block := range payload["content"].([]map[string]any) {
		if block["type"] == "resource_link" {
			uri = block["uri"].(string)
		}
		if _, ok := block["blob"]; ok {
			t.Fatal("file bytes leaked into tools/call")
		}
	}
	if uri == "" || metadata["uri"] != uri || metadata["sha256"] == "" || metadata["expires_at"] == "" {
		t.Fatalf("missing native link or metadata: %#v", payload)
	}
	encoded, _ := json.Marshal(payload)
	if len(encoded) > 4096 || bytes.Contains(encoded, []byte("local_path")) || bytes.Contains(encoded, []byte("server_path")) {
		t.Fatal("export result must contain only small metadata and a native link")
	}
	return uri
}

func readExportBytes(t *testing.T, server *mcpServer, uri string) ([]byte, string) {
	t.Helper()
	response := testRPC(t, server, "resources/read", map[string]any{"uri": uri})
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	result := response.Result.(map[string]any)
	content := result["contents"].([]map[string]any)[0]
	if content["uri"] != uri || content["text"] != nil || result["structuredContent"] != nil {
		t.Fatalf("resource is not a native binary result: %#v", result)
	}
	data, err := base64.StdEncoding.DecodeString(content["blob"].(string))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) >= 10<<20 {
		t.Fatalf("resource response exceeds tunnel envelope budget: %d bytes, %v", len(encoded), err)
	}
	return data, content["mimeType"].(string)
}

func TestFileExportRoundTripAndReconnect(t *testing.T) {
	files := map[string][]byte{
		"picture.png":  {137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 0},
		"document.pdf": []byte("%PDF-1.7\nfile export fixture\n%%EOF\n"),
		"binary.bin":   {0, 255, 1, 0, 254, 3},
		"empty.bin":    {},
		"limit.bin":    bytes.Repeat([]byte{0, 255, 16, 42}, maxExportBytes/4),
	}
	server := exportTestServer(t, files)
	for name, expected := range files {
		t.Run(name, func(t *testing.T) {
			uri := exportURI(t, server, name, "Überblick #1 "+name)
			// A fresh server knows only the shared output root, with no registry
			// and no backend credentials or connection from the export call.
			reconnected := &mcpServer{cfg: serverConfig{outRoot: server.cfg.outRoot}}
			actual, mimeType := readExportBytes(t, reconnected, uri)
			if !bytes.Equal(actual, expected) || sha256Hex(actual) != sha256Hex(expected) {
				t.Fatal("resource bytes changed")
			}
			if name == "picture.png" && mimeType != "image/png" || name == "document.pdf" && mimeType != "application/pdf" {
				t.Fatalf("unexpected MIME type %q", mimeType)
			}
		})
	}
	listed, err := server.toolListOutput(map[string]any{"recursive": true, "include_dirs": true})
	if err != nil || listed["count"] != 0 {
		t.Fatalf("private snapshots or staging directories leaked into output listing: %#v, %v", listed, err)
	}
	for _, method := range []string{"resources/list", "resources/templates/list"} {
		response := testRPC(t, server, method, map[string]any{})
		encoded, _ := json.Marshal(response)
		if response.Error != nil || bytes.Contains(encoded, []byte("anytype-export://")) {
			t.Fatalf("export handles must not be enumerated: %s", encoded)
		}
	}
}

func TestFileExportTooLargeAndExistingDownload(t *testing.T) {
	server := exportTestServer(t, map[string][]byte{
		"large.bin": make([]byte, maxExportBytes+1), "small.bin": []byte("original bytes"),
	})
	response := testRPC(t, server, "tools/call", map[string]any{"name": "file-export", "arguments": map[string]any{"object_id": "large.bin"}})
	result := response.Result.(map[string]any)
	encoded, _ := json.Marshal(result)
	if result["isError"] != true || !bytes.Contains(encoded, []byte("6291456")) || bytes.Contains(encoded, []byte("resource_link")) {
		t.Fatalf("oversize export was not refused: %s", encoded)
	}
	entries, _ := os.ReadDir(server.cfg.outRoot)
	if len(entries) != 0 {
		t.Fatalf("failed export left staging files: %v", entries)
	}
	download, err := server.toolDownload(map[string]any{"object_id": "small.bin", "target_name": "local copy.bin"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(download["local_path"].(string))
	if err != nil || string(data) != "original bytes" {
		t.Fatalf("existing file-download changed: %q, %v", data, err)
	}
	uri := exportURI(t, server, "small.bin", "attachment.bin")
	readExportBytes(t, server, uri)
	data, err = os.ReadFile(download["local_path"].(string))
	if err != nil || string(data) != "original bytes" {
		t.Fatal("file-export modified an existing download")
	}
}

func TestFileExportRejectsInvalidArgumentsAndURIs(t *testing.T) {
	server := &mcpServer{cfg: serverConfig{outRoot: t.TempDir()}}
	for _, args := range []map[string]any{
		{}, {"object_id": 12}, {"object_id": "file", "filename": "../escape"},
		{"object_id": "file", "filename": "..\\escape"}, {"object_id": "file", "filename": ""},
		{"object_id": "file", "filename": "bad\nname"}, {"object_id": "file", "filename": strings.Repeat("x", 256)},
		{"object_id": "file", "filename": 12}, {"object_id": "file", "path": "/arbitrary"},
	} {
		if _, err := server.toolExport(args); err == nil {
			t.Fatalf("invalid export arguments accepted: %#v", args)
		}
	}
	for _, uri := range []string{"", "file:///etc/passwd", "https://example.test/file", "anytype-export://bad/file", "anytype-export://" + strings.Repeat("a", 48) + "/../escape", "anytype-export://" + strings.Repeat("a", 48) + "/file?path=other"} {
		response := testRPC(t, server, "resources/read", map[string]any{"uri": uri})
		if response.Error == nil || response.Error.Code != -32602 {
			t.Fatalf("invalid URI accepted: %q: %#v", uri, response)
		}
	}
	response := testRPC(t, server, "resources/read", map[string]any{"uri": "anytype-export://" + strings.Repeat("a", 48) + "/missing"})
	if response.Error == nil || response.Error.Code != -32002 {
		t.Fatalf("missing resource did not return -32002: %#v", response)
	}
}

func TestFileExportExpiryIntegrityAndConfinement(t *testing.T) {
	server := exportTestServer(t, map[string][]byte{"fixture.bin": []byte("snapshot bytes")})
	uri := exportURI(t, server, "fixture.bin", "snapshot.bin")
	parsed, _ := url.Parse(uri)
	dataPath := filepath.Join(server.cfg.outRoot, exportCacheDirName, parsed.Host, "data")
	if err := os.WriteFile(dataPath, []byte("tampered bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	response := testRPC(t, server, "resources/read", map[string]any{"uri": uri})
	if response.Error == nil || response.Error.Code != -32603 {
		t.Fatal("modified snapshot was served")
	}
	if err := os.Remove(dataPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("snapshot bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dataPath); err != nil {
		t.Fatal(err)
	}
	response = testRPC(t, server, "resources/read", map[string]any{"uri": uri})
	if response.Error == nil {
		t.Fatal("resource read followed a symlink outside the export cache")
	}

	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "source.bin")
	if err := os.WriteFile(source, []byte("expired bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	expired, err := server.createFileExport("fixture", "expired.bin", sourceRoot, source, time.Now().Add(-2*exportTTL))
	if err != nil {
		t.Fatal(err)
	}
	expiredURI := expired.Metadata["uri"].(string)
	response = testRPC(t, server, "resources/read", map[string]any{"uri": expiredURI})
	if response.Error == nil || response.Error.Code != -32002 {
		t.Fatal("expired snapshot was served")
	}
	expiredParsed, _ := url.Parse(expiredURI)
	if _, err := os.Stat(filepath.Join(server.cfg.outRoot, exportCacheDirName, expiredParsed.Host)); !os.IsNotExist(err) {
		t.Fatalf("expired snapshot was not removed: %v", err)
	}
	link := filepath.Join(sourceRoot, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := server.createFileExport("fixture", "escaped.bin", sourceRoot, link, time.Now()); err == nil {
		t.Fatal("export followed a symlink outside its staging root")
	}
}
