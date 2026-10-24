package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runMessages feeds line-framed JSON-RPC messages through the server loop.
func runMessages(t *testing.T, s *mcpServer, messages ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := s.run(strings.NewReader(strings.Join(messages, "\n")+"\n"), &out); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
	return out.String()
}

func readLogLines(t *testing.T, dir string) (string, []map[string]any) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, callLogPrefix+"*"+callLogSuffix))
	if len(files) != 1 {
		t.Fatalf("log files = %v, want one", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("not a JSON line: %q", line)
		}
		lines = append(lines, e)
	}
	return string(raw), lines
}

func TestCallLogRecordsShapeNotContent(t *testing.T) {
	dir := t.TempDir()
	s := &mcpServer{cfg: serverConfig{outRoot: t.TempDir()}}
	s.callLog = newCallLog(dir, "tester", 0, time.Now())

	const marker = "Geheimer Seitentitel 4711"
	const objectID = "bafyreixxxxxxxxaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runMessages(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"probe-client","version":"1.2"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		// object-render without a renderer fails before any network: a tool error.
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"object-render","arguments":{"space_id":"`+marker+`","object_id":"`+objectID+`"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"object-render","arguments":{"object_id":"`+objectID+`","space_id":"`+marker+`"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"anytype-export://nothing/`+marker+`.pdf"}}`,
		`{not json}`,
	)

	raw, lines := readLogLines(t, dir)
	if strings.Contains(raw, marker) || strings.Contains(raw, "Geheim") || strings.Contains(raw, objectID) {
		t.Fatalf("an argument value reached the log:\n%s", raw)
	}
	if len(lines) != 6 {
		t.Fatalf("%d lines, want one per message:\n%s", len(lines), raw)
	}
	init, call, again, read, bad := lines[0], lines[2], lines[3], lines[4], lines[5]
	if init["method"] != "initialize" || init["protocol"] != "2025-06-18" || init["client"] != "probe-client/1.2" || init["consumer"] != "tester" {
		t.Errorf("initialize line = %v", init)
	}
	if lines[1]["method"] != "notifications/initialized" || lines[1]["bytes"] != nil {
		t.Errorf("a notification gets no response and no size: %v", lines[1])
	}
	if call["tool"] != "object-render" || call["client"] != "probe-client/1.2" || call["ok"] != false {
		t.Errorf("tools/call line = %v", call)
	}
	if keys, _ := call["args"].([]any); len(keys) != 2 || keys[0] != "object_id" || keys[1] != "space_id" {
		t.Errorf("args = %v, want the sorted names", call["args"])
	}
	if ids, _ := call["ids"].(map[string]any); len(ids) != 2 || ids["object_id"] != shortHash(objectID, 8) {
		t.Errorf("ids = %v, want both hashed", call["ids"])
	}
	if call["args_hash"] == nil || call["args_hash"] != again["args_hash"] {
		t.Errorf("the same arguments in another order must hash alike: %v vs %v", call["args_hash"], again["args_hash"])
	}
	if !strings.Contains(call["error"].(string), "ANYTYPE_RENDER_URL") {
		t.Errorf("error = %v", call["error"])
	}
	if call["proc"] != again["proc"] || call["seq"].(float64)+1 != again["seq"].(float64) {
		t.Errorf("proc/seq do not follow one session: %v, %v", call, again)
	}
	if read["uri"] != shortHash("anytype-export://nothing/"+marker+".pdf", 12) || read["ok"] != false {
		t.Errorf("resources/read line = %v", read)
	}
	if bad["method"] != "(unparsable)" || bad["ok"] != false {
		t.Errorf("parse error line = %v", bad)
	}
}

func TestCallLogLinksMatchLaterReads(t *testing.T) {
	e := map[string]any{}
	uri := "anytype-export://abc/render.jpg"
	describeToolResult(e, toolResult(nativeToolResult{
		Metadata: map[string]any{"uri": uri},
		Content:  []map[string]any{{"type": "resource_link", "uri": uri}},
	}, false))
	if links, _ := e["links"].([]string); len(links) != 1 || links[0] != shortHash(uri, 12) || e["ok"] != true {
		t.Errorf("entry = %v", e)
	}
}

func TestCallLogScrubsIDsInErrors(t *testing.T) {
	id := "bafyreixxxxxxxxbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.2abc"
	got := scrubError("ObjectShow error: " + id + " gone " + strings.Repeat("x", 400))
	if strings.Contains(got, "bafyrei") || !strings.Contains(got, "<id "+shortHash(id, 8)+">") {
		t.Errorf("scrubbed = %q", got)
	}
	if n := len([]rune(got)); n > callLogErrorMaxRune+1 {
		t.Errorf("error not cut: %d runes", n)
	}
}

func TestCallLogRetentionAndCap(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"calls-2026-08-01.jsonl", "calls-2026-10-20.jsonl", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	l := newCallLog(dir, "", 30, now)
	for _, c := range []struct {
		name string
		kept bool
	}{{"calls-2026-08-01.jsonl", false}, {"calls-2026-10-20.jsonl", true}, {"notes.txt", true}} {
		if _, err := os.Stat(filepath.Join(dir, c.name)); (err == nil) != c.kept {
			t.Errorf("%s kept=%v, want %v", c.name, err == nil, c.kept)
		}
	}

	l.maxBytes = 200
	for i := 0; i < 10; i++ {
		l.write(map[string]any{"ts": "t", "pad": strings.Repeat("y", 80)}, now)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "calls-2026-10-24.jsonl"))
	if strings.Count(string(raw), "day full") != 1 || len(raw) > 400 {
		t.Errorf("cap not kept, %d bytes:\n%s", len(raw), raw)
	}
}

func TestCallLogOffWithoutDir(t *testing.T) {
	var l *callLog = newCallLog("", "x", 0, time.Now())
	if l != nil {
		t.Fatal("a log without a directory")
	}
	l.record(rpcRequest{Method: "ping"}, rpcResponse{}, true, 1, time.Now(), nil) // must not panic
}

func TestExplainError(t *testing.T) {
	got := explainError("ObjectShow error (UNKNOWN_ERROR): build tree: tree does not exist")
	if !strings.HasPrefix(got, "object not found in this space") || !strings.Contains(got, "tree does not exist") {
		t.Errorf("got %q", got)
	}
	got = explainError("ObjectShow error (UNKNOWN_ERROR): wait space: space not exists")
	if !strings.HasPrefix(got, "space not found") {
		t.Errorf("got %q", got)
	}
	if got := explainError("width must be 480-3840 px"); got != "width must be 480-3840 px" {
		t.Errorf("other errors must stay as they are: %q", got)
	}
}
