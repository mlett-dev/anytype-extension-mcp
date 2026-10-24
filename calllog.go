package main

// The call log: one JSON line per message, for later analysis of how clients
// use this server — which tools, how often, how fast, what fails, what is
// sent twice, which export links are never read (scripts/calllog-report.py).
//
// It records the shape of a call, not its content: the argument names, a
// hash over the arguments, ids as short hashes, sizes and timings. No values,
// texts or results. Error messages are kept, with Anytype ids replaced by
// their hash, because "why did it fail" is what the log is read for most.
//
// Each client connection runs its own server process, and they share one
// directory: one file per UTC day, appended (O_APPEND) in single writes of
// whole lines, never rotated by renaming. Days past the retention are deleted
// at start; a day that reaches the size cap gets one marker line, then no
// more. A log that cannot be written never fails a call — it switches off.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	callLogPrefix       = "calls-"
	callLogSuffix       = ".jsonl"
	defaultCallLogDays  = 30
	callLogMaxDayBytes  = 64 << 20
	callLogErrorMaxRune = 300
)

type callLog struct {
	dir      string
	consumer string
	proc     string // random per process: tells concurrent sessions apart
	keepDays int
	maxBytes int64
	seq      int64
	client   string // clientInfo from initialize
	day      string
	file     *os.File
	full     bool
	off      bool
}

// newCallLog returns nil when dir is empty: logging is off.
func newCallLog(dir, consumer string, keepDays int, now time.Time) *callLog {
	if dir == "" {
		return nil
	}
	if keepDays <= 0 {
		keepDays = defaultCallLogDays
	}
	l := &callLog{dir: dir, consumer: consumer, proc: randomHex(4), keepDays: keepDays, maxBytes: callLogMaxDayBytes}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		l.disable(err)
		return l
	}
	l.prune(now)
	return l
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (l *callLog) disable(err error) {
	if !l.off {
		fmt.Fprintf(os.Stderr, "%s: call log off: %v\n", serverName, err)
	}
	l.off = true
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

func (l *callLog) prune(now time.Time) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	oldest := now.UTC().AddDate(0, 0, -l.keepDays).Format("2006-01-02")
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, callLogPrefix) || !strings.HasSuffix(name, callLogSuffix) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, callLogPrefix), callLogSuffix)
		if _, err := time.Parse("2006-01-02", day); err == nil && day < oldest {
			_ = os.Remove(filepath.Join(l.dir, name))
		}
	}
}

func (l *callLog) write(entry map[string]any, now time.Time) {
	if l == nil || l.off {
		return
	}
	day := now.UTC().Format("2006-01-02")
	if l.file == nil || day != l.day {
		if l.file != nil {
			l.file.Close()
		}
		// Readable by all: the server runs as root in its container, and the
		// host user who reads the log is not root. The directory's own mode
		// decides who gets in.
		f, err := os.OpenFile(filepath.Join(l.dir, callLogPrefix+day+callLogSuffix), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			l.disable(err)
			return
		}
		l.file, l.day, l.full = f, day, false
	}
	if l.full {
		return
	}
	if info, err := l.file.Stat(); err == nil && info.Size() >= l.maxBytes {
		l.full = true
		entry = map[string]any{"ts": entry["ts"], "proc": l.proc, "log": "day full, nothing more is written today"}
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if _, err := l.file.Write(append(line, '\n')); err != nil {
		l.disable(err)
	}
}

// record writes one line for a message and the response to it (sent tells
// whether there was one; notifications get none).
func (l *callLog) record(req rpcRequest, resp rpcResponse, sent bool, size int, start time.Time, notes map[string]any) {
	if l == nil || l.off {
		return
	}
	now := time.Now()
	l.seq++
	e := map[string]any{
		"ts":     start.UTC().Format(time.RFC3339Nano),
		"proc":   l.proc,
		"seq":    l.seq,
		"method": req.Method,
		"ms":     now.Sub(start).Milliseconds(),
	}
	if l.consumer != "" {
		e["consumer"] = l.consumer
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		if json.Unmarshal(req.Params, &p) == nil {
			l.client = strings.TrimSuffix(p.ClientInfo.Name+"/"+p.ClientInfo.Version, "/")
			e["protocol"] = p.ProtocolVersion
		}
	case "tools/call":
		var p struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) == nil {
			e["tool"] = p.Name
			if hints, ok := toolAnnotations[p.Name]; ok && hints.kind != readOnly {
				e["writes"] = true
			}
			describeArgs(e, p.Arguments)
		}
		describeToolResult(e, resp.Result)
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.URI != "" {
			e["uri"] = shortHash(p.URI, 12)
		}
	}
	if l.client != "" {
		e["client"] = l.client
	}
	if resp.Error != nil {
		e["ok"] = false
		e["error"] = scrubError(resp.Error.Message)
	} else if _, set := e["ok"]; !set && sent {
		e["ok"] = true
	}
	if sent {
		e["bytes"] = size
	}
	for k, v := range notes {
		e[k] = v
	}
	l.write(e, now)
}

// describeArgs adds the argument names, a hash over the arguments (equal
// hashes = the same call, which is how repeats are found) and every *_id
// argument as a short hash, so calls on one object can be followed.
func describeArgs(e map[string]any, args map[string]json.RawMessage) {
	keys := make([]string, 0, len(args))
	ids := map[string]string{}
	canonical := map[string]any{}
	for k, raw := range args {
		keys = append(keys, k)
		var v any
		if json.Unmarshal(raw, &v) == nil {
			canonical[k] = v
		}
		if s, ok := v.(string); ok && strings.HasSuffix(k, "_id") && s != "" {
			ids[k] = shortHash(s, 8)
		}
	}
	sort.Strings(keys)
	e["args"] = keys
	// json.Marshal sorts map keys, nested ones too: the same arguments in any
	// order give the same hash.
	if encoded, err := json.Marshal(canonical); err == nil {
		e["args_hash"] = shortHash(string(encoded), 16)
	}
	if len(ids) > 0 {
		e["ids"] = ids
	}
}

func describeToolResult(e map[string]any, result any) {
	r, ok := result.(map[string]any)
	if !ok {
		return
	}
	isError, _ := r["isError"].(bool)
	e["ok"] = !isError
	if isError {
		if payload, ok := r["structuredContent"].(map[string]any); ok {
			if msg, ok := payload["error"].(string); ok {
				e["error"] = scrubError(msg)
			}
		}
	}
	content, _ := r["content"].([]map[string]any)
	var links []string
	images := 0
	for _, c := range content {
		switch c["type"] {
		case "resource_link":
			if uri, ok := c["uri"].(string); ok {
				links = append(links, shortHash(uri, 12))
			}
		case "image":
			images++
		}
	}
	if len(links) > 0 {
		e["links"] = links
	}
	if images > 0 {
		e["images"] = images
	}
}

var anytypeIDPattern = regexp.MustCompile(`\bbafy[a-z2-7]{20,}(\.[a-z0-9]+)?`)

func scrubError(msg string) string {
	msg = anytypeIDPattern.ReplaceAllStringFunc(msg, func(id string) string { return "<id " + shortHash(id, 8) + ">" })
	if utf8.RuneCountInString(msg) > callLogErrorMaxRune {
		msg = string([]rune(msg)[:callLogErrorMaxRune]) + "…"
	}
	return msg
}

func shortHash(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}
