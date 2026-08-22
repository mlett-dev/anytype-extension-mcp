package main

// What can actually arrive at a tool call, and in what shape.
//
// Every file entrance in this server takes either a path under the input root
// or base64 inside the call (see bytes_tools.go). Base64 is expensive in a way
// that is easy to miss: the payload becomes part of the tool arguments, so it
// travels through the calling model's context and is paid for as tokens, twice
// the file size and then some. The obvious wish is for the caller to hand over
// a file the way a chat attachment is handed over — as a reference the runtime
// resolves — so the bytes never enter the conversation at all.
//
// Whether such a channel exists is not answerable from the specification
// alone. MCP's tools/call carries `arguments` as a plain JSON object validated
// against inputSchema, and JSON has no binary type; the typed content blocks
// (text, image, audio, resource, resource_link) live on tool *results*, prompt
// messages and sampling messages, never on arguments. But a connector runtime
// sits between the model and this server and is free to put things in the call
// that the specification does not describe — an opaque handle, a signed URL, an
// extra key in _meta. Only the bytes on the wire can say.
//
// debug-receive-file is that measurement. It declares no parameter that could
// hold file content as text, reports the raw JSON-RPC params exactly as they
// arrived (with long strings summarised, so measuring the channel does not
// itself flood the context it is trying to protect), and classifies whatever
// file reference it can find. It uploads nothing.
//
// Reading the result: reference_kind is the answer. "none" with a successful
// call means the runtime had no way to pass the file — but only if a bare
// zero-argument call also succeeds, which is why every argument is optional:
// without that control, "nothing arrived" cannot be told apart from "the call
// was rejected". "inline_base64" means the runtime had no channel either and
// the model fell back to text, against the instruction in the description.
// Anything else — a URI, a handle, a structured reference — is a real finding
// and the shape it reports is what a non-base64 upload path would be built on.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// maxPreviewString is where a string in the echoed params stops being repeated
// and starts being described. A base64 image is the whole reason this tool
// exists; echoing it back would double the cost of finding out that it cost
// too much.
const maxPreviewString = 160

// minBase64Blob is the shortest string worth suspecting of being encoded
// content. Short strings decode as base64 by accident all the time ("test" is
// valid base64), and a false positive here would report a filename as a file.
const minBase64Blob = 64

func (s *mcpServer) debugToolDefs() []any {
	return []any{
		map[string]any{
			"name": "debug-receive-file",
			"description": "Diagnostic only: reports what a file handed to this tool actually looks like when it arrives, and uploads nothing anywhere. " +
				"Use it to answer one question — can a file reach this server without its bytes being written into the tool call as text? " +
				"DO NOT base64-encode anything for this tool, and do not paste file content into any field. " +
				"Attach or pass the file the way your runtime offers, if it offers a way at all. " +
				"If the only thing you could do is turn the file into text, then pass NO file argument at all and say so in `note` — " +
				"that answer is the useful result, and a text copy of the file would destroy it. " +
				"Calling this with no arguments whatsoever is valid and is the control case. " +
				"Returns the filename, MIME type, size and SHA-256 it was able to determine, plus the kind of reference that arrived and the raw call parameters with long strings summarised.",
			"inputSchema": map[string]any{
				"type": "object",
				// Deliberately not restSchema: additionalProperties is open
				// here because the finding may well be a key nobody declared.
				// A runtime that injects its own attachment field would have it
				// stripped by a closed schema, and the measurement would report
				// the schema's behaviour instead of the runtime's.
				"additionalProperties": true,
				"required":             []string{},
				"properties": map[string]any{
					"file": map[string]any{
						"type":        "object",
						"description": "The file itself, as a reference — whatever object shape your runtime uses for an attachment. Do not construct one by hand and do not put encoded content in it. Every field is optional; send what the runtime gives you.",
						"properties": map[string]any{
							"name":      strProp("Filename, if known."),
							"mime_type": strProp("Media type, if known."),
							"size":      map[string]any{"type": "integer", "description": "Size in bytes, if known."},
							"id":        strProp("Opaque identifier or handle for the file, if the runtime uses one."),
							"uri":       strProp("Location the file can be fetched from, if the runtime provides one. A reference, never the content itself."),
						},
						"additionalProperties": true,
					},
					"file_reference": strProp("A file reference as a bare string, if that is the only shape available: a URI, a handle or an id. This is a pointer to content, never content. Do not put a data: URI or base64 here — that is content in disguise."),
					"note":           strProp("What you attempted and what your runtime allowed. Say so here if there was no way to pass a file without encoding it as text — that is the result this test is looking for."),
					"fetch_reference": map[string]any{
						"type":        "boolean",
						"description": "Whether to try fetching a http/https reference to measure the real bytes. On by default. Public hosts only; a private or loopback address is refused, since this server sits inside the Anytype container's network.",
						"default":     true,
					},
				},
			},
		},
	}
}

// openAIFileParamDefs is the file-argument contract ChatGPT documents, written
// out exactly as OpenAI's own example does.
//
// The mechanism is the tool-level _meta below, not the schema: "openai/fileParams"
// names the top-level arguments that are files, and for those ChatGPT resolves
// one of its own files BEFORE the tool call and substitutes this object. That is
// the difference between this tool and debug-receive-file, and it is the whole
// answer to why the earlier attempts failed — without the marker, `file` is an
// ordinary JSON object, so the model had to write the sandbox handle in by hand,
// and the proxy bridge then tried a mount rewrite it had no configuration for.
//
// The field names are not ours to choose and the required-set is checked when
// the tool is scanned: all four fields must be declared, download_url and
// file_id must be required, mime_type and file_name must not be. So this schema
// is written literally rather than through the restSchema/strProp helpers —
// descriptions are left off the four fields to keep it byte-for-byte the shape
// OpenAI documents, and the guidance lives in the tool description instead.
func openAIFileParamDefs() map[string]any {
	return map[string]any{
		"OpenAIFile": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"download_url": map[string]any{"type": "string"},
				"file_id":      map[string]any{"type": "string"},
				"mime_type":    map[string]any{"type": "string"},
				"file_name":    map[string]any{"type": "string"},
			},
			"required":             []string{"download_url", "file_id"},
			"additionalProperties": false,
		},
	}
}

func (s *mcpServer) openAIFileToolDefs() []any {
	return []any{
		map[string]any{
			"name": "debug-receive-openai-file",
			"description": "Diagnostic only: takes a ChatGPT file as a real file argument and reports what arrived. Uploads nothing. " +
				"Do NOT construct the file object by hand and do NOT put base64 anywhere — just select the file (for example an image you just generated) as the `file` argument and let the runtime fill it in. " +
				"The server then fetches download_url itself, so the bytes never pass through the conversation. " +
				"Returns file name, MIME type (declared and sniffed from the content), size and SHA-256. " +
				"If this works, it is the transport that replaces base64; debug-receive-file remains the open-ended prober for everything else.",
			"inputSchema": map[string]any{
				"type":  "object",
				"$defs": openAIFileParamDefs(),
				"properties": map[string]any{
					"file": map[string]any{"$ref": "#/$defs/OpenAIFile"},
					"note": map[string]any{"type": "string"},
				},
				"required": []string{"file"},
			},
			"_meta": map[string]any{
				"openai/fileParams": []string{"file"},
			},
		},
	}
}

func (s *mcpServer) dispatchDebugTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "debug-receive-file":
		res, err := s.toolDebugReceiveFile(args)
		return res, err, true
	case "debug-receive-openai-file":
		res, err := s.toolDebugReceiveOpenAIFile(args)
		return res, err, true
	}
	return nil, nil, false
}

func (s *mcpServer) toolDebugReceiveOpenAIFile(args map[string]any) (map[string]any, error) {
	out := map[string]any{
		"tool":             "debug-receive-openai-file",
		"server_version":   serverVersion,
		"protocol_version": protoVersion,
		"uploaded":         false,
	}
	if len(s.lastRawParams) > 0 {
		var envelope map[string]any
		if err := json.Unmarshal(s.lastRawParams, &envelope); err == nil {
			out["raw_params_summary"] = summariseJSON(envelope)
			out["call_meta"] = summariseJSON(envelope["_meta"])
		}
	}

	file, ok := args["file"].(map[string]any)
	if !ok || len(file) == 0 {
		out["file_param_present"] = false
		out["content_resolved"] = false
		out["verdict"] = "No file argument arrived, although the schema requires one. Either no file was selected, or the runtime did not honour the openai/fileParams marker on this tool. Check raw_params_summary for what was sent instead, and compare with a debug-receive-file call."
		return out, nil
	}
	out["file_param_present"] = true

	downloadURL := firstStringField(file, "download_url")
	fileID := firstStringField(file, "file_id")
	fileName := firstStringField(file, "file_name")
	declaredMIME := firstStringField(file, "mime_type")

	out["file_id"] = nullableString(fileID)
	out["filename"] = nullableString(fileName)
	out["mime_type_declared"] = nullableString(declaredMIME)
	// The download URL is a bearer credential: whoever holds it can read the
	// file. Reporting its shape answers the question; echoing it in full would
	// put a live secret into the conversation for no gain.
	out["download_url"] = describeSignedURL(downloadURL)

	if downloadURL == "" {
		out["content_resolved"] = false
		out["verdict"] = "A file object arrived but without download_url, so there is nothing to fetch. file_id alone cannot be resolved by this server — MCP has no way for a server to ask a client for file content."
		return out, nil
	}

	data, status, err := fetchReference(downloadURL, s.cfg.timeout)
	if err != nil {
		out["reference_fetch"] = map[string]any{"attempted": true, "http_status": status, "error": err.Error()}
		out["content_resolved"] = false
		out["verdict"] = "The runtime supplied a download_url, which is the mechanism working, but this server could not fetch it: " + err.Error() + ". These URLs are short-lived, so a stale one is the first thing to suspect."
		return out, nil
	}

	out["reference_fetch"] = map[string]any{"attempted": true, "http_status": status}
	out["content_resolved"] = true
	out["size_bytes"] = len(data)
	out["sha256"] = sha256Hex(data)
	sniffed := sniffMIME(fileName, data)
	out["mime_type_sniffed"] = sniffed
	out["mime_type"] = sniffed
	out["reference_kind"] = "openai_file_param"
	out["verdict"] = fmt.Sprintf("The file arrived as a real file argument and this server fetched %d bytes from download_url itself. The bytes never entered the tool call, so nothing about the file was paid for as tokens. This is the transport the file-upload-attachment family uses.", len(data))
	return out, nil
}

// describeSignedURL reports enough of a URL to recognise it without disclosing
// the signature that makes it work.
func describeSignedURL(raw string) any {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return map[string]any{"parse_error": err.Error(), "length": len(raw)}
	}
	return map[string]any{
		"scheme":     parsed.Scheme,
		"host":       parsed.Host,
		"path":       parsed.Path,
		"signed":     parsed.RawQuery != "",
		"length":     len(raw),
		"redacted":   true,
		"query_keys": queryKeys(parsed),
	}
}

func queryKeys(parsed *url.URL) []string {
	values := parsed.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fileCandidate is one place in the arguments that could be carrying the file,
// found by walking the parsed JSON rather than by reading declared parameters —
// an undeclared key is exactly the kind of thing worth catching.
type fileCandidate struct {
	Path     string
	Kind     string
	Rank     int
	Detail   string
	Bytes    []byte
	URI      string
	Handle   string
	Filename string
	MIME     string
	Fields   map[string]any
}

func (s *mcpServer) toolDebugReceiveFile(args map[string]any) (map[string]any, error) {
	rawParams := s.lastRawParams
	out := map[string]any{
		"tool":             "debug-receive-file",
		"server":           serverName,
		"server_version":   serverVersion,
		"protocol_version": protoVersion,
		"uploaded":         false,
	}

	// The params as they came off the wire, not as they were parsed: _meta,
	// unexpected keys and the exact JSON types are all part of the answer, and
	// the map[string]any the dispatcher receives has already dropped some of
	// that context.
	var envelope map[string]any
	if len(rawParams) > 0 {
		out["raw_params_bytes"] = len(rawParams)
		if err := json.Unmarshal(rawParams, &envelope); err != nil {
			out["raw_params_error"] = err.Error()
		}
	} else {
		out["raw_params_bytes"] = 0
	}
	if envelope != nil {
		out["raw_params_summary"] = summariseJSON(envelope)
		if meta, ok := envelope["_meta"]; ok {
			out["call_meta"] = summariseJSON(meta)
		} else {
			out["call_meta"] = nil
		}
	}

	rawArgs := any(args)
	if envelope != nil {
		if a, ok := envelope["arguments"]; ok {
			rawArgs = a
		}
	}
	out["argument_shape"] = describeShape(rawArgs)

	candidates := []fileCandidate{}
	collectCandidates("arguments", rawArgs, &candidates)
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Rank > candidates[j].Rank })
	candidates = dedupeByPath(candidates)

	summaries := make([]any, 0, len(candidates))
	for _, c := range candidates {
		entry := map[string]any{"path": c.Path, "kind": c.Kind, "detail": c.Detail}
		if c.Filename != "" {
			entry["filename"] = c.Filename
		}
		summaries = append(summaries, entry)
	}
	out["candidates"] = summaries

	if len(candidates) == 0 {
		out["reference_kind"] = "none"
		out["content_resolved"] = false
		out["filename"] = nil
		out["mime_type"] = nil
		out["size_bytes"] = nil
		out["sha256"] = nil
		out["verdict"] = "No file reference of any kind arrived in the tool call. If this call itself succeeded, the connector runtime offered no way to attach a file to an MCP tool call: tools/call carries only the JSON object described by inputSchema. Confirm with a deliberate zero-argument call — if that also succeeds, the tool is reachable and the absence is the finding, not a rejected schema."
		return out, nil
	}

	best := candidates[0]
	out["reference_kind"] = best.Kind
	out["reference_path"] = best.Path
	if best.Fields != nil {
		out["reference_fields"] = summariseJSON(best.Fields)
	}

	filename := best.Filename
	if filename == "" {
		filename = findFilename(rawArgs)
	}
	declaredMIME := best.MIME
	if declaredMIME == "" {
		declaredMIME = findDeclaredMIME(rawArgs)
	}

	data := best.Bytes
	resolution := best.Detail

	if len(data) == 0 && best.URI != "" && optionalBool(args, "fetch_reference", true) {
		fetched, status, err := fetchReference(best.URI, s.cfg.timeout)
		if err != nil {
			out["reference_fetch"] = map[string]any{"attempted": true, "error": err.Error()}
			resolution = "reference could not be fetched: " + err.Error()
		} else {
			out["reference_fetch"] = map[string]any{"attempted": true, "http_status": status, "bytes": len(fetched)}
			data = fetched
			resolution = fmt.Sprintf("reference fetched over HTTP (%s)", status)
		}
	} else if len(data) == 0 && best.URI != "" {
		out["reference_fetch"] = map[string]any{"attempted": false, "reason": "fetch_reference is off"}
	}

	out["resolution"] = resolution
	out["filename"] = nullableString(filename)
	out["mime_type_declared"] = nullableString(declaredMIME)

	if len(data) > 0 {
		out["content_resolved"] = true
		out["size_bytes"] = len(data)
		out["sha256"] = sha256Hex(data)
		sniffed := sniffMIME(filename, data)
		out["mime_type_sniffed"] = sniffed
		out["mime_type"] = sniffed
	} else {
		out["content_resolved"] = false
		out["size_bytes"] = nil
		out["sha256"] = nil
		out["mime_type_sniffed"] = nil
		out["mime_type"] = nullableString(declaredMIME)
		if best.Handle != "" {
			out["handle"] = best.Handle
		}
	}

	out["verdict"] = verdictFor(best, len(data) > 0)
	return out, nil
}

// dedupeByPath keeps one finding per location. A URI inside a file object is
// reached twice — once promoted by the object, carrying its sibling name and
// media type, once by the walk — and the enriched one is first because the
// sort is stable and the object was appended before its children.
func dedupeByPath(candidates []fileCandidate) []fileCandidate {
	seen := make(map[string]bool, len(candidates))
	out := candidates[:0]
	for _, c := range candidates {
		if seen[c.Path] {
			continue
		}
		seen[c.Path] = true
		out = append(out, c)
	}
	return out
}

func verdictFor(c fileCandidate, resolved bool) string {
	switch c.Kind {
	case "inline_base64", "inline_data_uri":
		return "The file arrived as encoded text inside the tool arguments, which is the path this test was trying to avoid: those bytes passed through the model's context and were paid for as tokens. It means no attachment channel was available (or none was used); this server no longer accepts encoded content, so there is no upload route for it. Note that the tool description explicitly asked for no encoding, so this is a fallback, not a capability."
	case "remote_uri":
		if resolved {
			return "The file arrived as a URL that this server could fetch itself — the bytes never entered the tool arguments. This is a working non-base64 path: a real upload tool would take the reference, fetch it here, and hand it to Anytype."
		}
		return "The file arrived as a URL, so the runtime does have a reference channel, but this server could not fetch it (see reference_fetch). Whether the path is usable depends on whether the URL is reachable and authorised from this host."
	case "uri_reference", "opaque_handle", "structured_reference":
		return "Something arrived that names a file without carrying it: " + c.Kind + ". MCP defines no way for a server to resolve such a handle — there is no request from server to client for file content — so this is only usable if the handle can be dereferenced over a channel this server already has. Report the exact shape in reference_fields."
	default:
		return "An argument was present but its role could not be classified; see candidates and raw_params_summary."
	}
}

// collectCandidates walks the parsed arguments looking for anything that could
// be the file. It reads values, not parameter names, because the interesting
// case is a key this server never declared.
func collectCandidates(pathExpr string, value any, out *[]fileCandidate) {
	switch v := value.(type) {
	case map[string]any:
		// The object-level finding and the walk into it are both needed. An
		// object that describes a file may also be carrying it: told to put the
		// file under `file`, a model writes {name, mime_type, data:"<base64>"},
		// and stopping at the object would report "describes a file but carries
		// nothing" with the bytes sitting one level down. Ranking sorts the two
		// out; identical paths are deduplicated afterwards.
		if c, ok := structuredCandidate(pathExpr, lastSegment(pathExpr), v); ok {
			*out = append(*out, c)
		}
		keys := sortedKeys(v)
		for _, k := range keys {
			collectCandidates(pathExpr+"."+k, v[k], out)
		}
	case []any:
		for i, item := range v {
			collectCandidates(fmt.Sprintf("%s[%d]", pathExpr, i), item, out)
		}
	case string:
		if c, ok := stringCandidate(pathExpr, lastSegment(pathExpr), v); ok {
			*out = append(*out, c)
		}
	}
}

func structuredCandidate(pathExpr, key string, obj map[string]any) (fileCandidate, bool) {
	uri := firstStringField(obj, "uri", "url", "href", "download_url", "signed_url", "location")
	handle := firstStringField(obj, "id", "file_id", "fileId", "handle", "attachment_id", "asset_id")
	name := firstStringField(obj, "name", "filename", "file_name", "fileName")
	mimeType := firstStringField(obj, "mime_type", "mimeType", "mime", "content_type", "contentType")

	// An object that only names a file is still a finding — it says the runtime
	// has an attachment shape even if nothing in it can be dereferenced — but
	// only under a key that claims to be a file. Any other object is walked
	// into, because encoded content nested somewhere is the base64 case wearing
	// a different hat.
	if uri == "" && handle == "" {
		if len(obj) > 0 && isFileKey(key) && (name != "" || mimeType != "") {
			return fileCandidate{
				Path: pathExpr, Kind: "structured_reference", Rank: 20,
				Detail:   "an object describing a file but carrying neither its bytes nor anything that could be dereferenced",
				Filename: name, MIME: mimeType, Fields: obj,
			}, true
		}
		return fileCandidate{}, false
	}
	if uri != "" {
		if c, ok := stringCandidate(pathExpr+".uri", "uri", uri); ok {
			c.Filename = name
			c.MIME = mimeType
			c.Handle = handle
			c.Fields = obj
			return c, true
		}
	}
	return fileCandidate{
		Path:     pathExpr,
		Kind:     "structured_reference",
		Rank:     30,
		Detail:   "an object naming a file without carrying its bytes",
		Handle:   handle,
		URI:      uri,
		Filename: name,
		MIME:     mimeType,
		Fields:   obj,
	}, true
}

func stringCandidate(pathExpr, key, value string) (fileCandidate, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fileCandidate{}, false
	}
	lower := strings.ToLower(trimmed)

	// A data: URI is content, not a reference, however much it looks like one.
	if strings.HasPrefix(lower, "data:") {
		mimeType := ""
		if idx := strings.Index(lower, ";base64,"); idx >= 0 {
			mimeType = strings.TrimPrefix(lower[:idx], "data:")
			if decoded, err := decodeLooseBase64(trimmed[idx+len(";base64,"):]); err == nil {
				return fileCandidate{
					Path: pathExpr, Kind: "inline_data_uri", Rank: 90,
					Detail: "content inlined as a data: URI, which is base64 in the tool call",
					Bytes:  decoded, MIME: mimeType,
				}, true
			}
		}
		return fileCandidate{
			Path: pathExpr, Kind: "inline_data_uri", Rank: 80,
			Detail: "a data: URI whose payload could not be decoded", MIME: mimeType,
		}, true
	}

	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return fileCandidate{
			Path: pathExpr, Kind: "remote_uri", Rank: 70,
			Detail: "a URL the server may be able to fetch", URI: trimmed,
			Filename: filenameFromURL(trimmed),
		}, true
	}

	if scheme, ok := uriScheme(trimmed); ok {
		return fileCandidate{
			Path: pathExpr, Kind: "uri_reference", Rank: 50,
			Detail: "a URI in the " + scheme + " scheme, which this server has no way to resolve",
			URI:    trimmed, Handle: trimmed,
			Filename: filenameFromURL(trimmed),
		}, true
	}

	// Base64 and an opaque handle are not distinguishable by shape: a signed
	// file id long enough to matter is valid base64 too, and looksLikeBase64
	// admits the - and _ such ids are full of. Deciding by length alone would
	// report a handle as inline content — the most damaging mistake this tool
	// can make, because the verdict would then deny an attachment channel on
	// the very shape that proves one. So under a key that claims to reference a
	// file, only bytes that sniff as actual content count as content; size
	// alone decides only elsewhere.
	referenceKey := isReferenceKey(key) && !isNameKey(key)
	if len(trimmed) >= minBase64Blob && looksLikeBase64(trimmed) {
		if decoded, err := decodeLooseBase64(trimmed); err == nil && len(decoded) >= 32 {
			recognisable := sniffMIME("", decoded) != "application/octet-stream"
			if recognisable || (!referenceKey && len(decoded) >= 512) {
				detail := "raw base64 in a parameter that never declared it"
				if !recognisable {
					detail += ", decoding to unrecognisable bytes"
				}
				return fileCandidate{
					Path: pathExpr, Kind: "inline_base64", Rank: 100,
					Detail: detail, Bytes: decoded,
				}, true
			}
		}
	}

	// A bare token under a key that claims to name a file. This is the shape an
	// attachment channel would most plausibly take — an id the runtime knows
	// how to resolve and this server does not — so it must not fall through as
	// "nothing arrived". Keys that name rather than reference a file are
	// excluded: a filename is not a handle.
	if referenceKey && looksLikeToken(trimmed) {
		return fileCandidate{
			Path: pathExpr, Kind: "opaque_handle", Rank: 40,
			Detail: "a bare identifier under a key that names a file reference",
			Handle: trimmed,
		}, true
	}
	return fileCandidate{}, false
}

// uriScheme reports the scheme of a URI-shaped string. Schemes are longer than
// the obvious ones — "mcp-resource://" is twelve characters before the colon —
// so the bound is generous, and the character set is what keeps a sentence
// containing "://" from being read as a reference.
func uriScheme(value string) (string, bool) {
	idx := strings.Index(value, "://")
	if idx <= 0 || idx > 32 {
		return "", false
	}
	for _, r := range value[:idx] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '+', r == '-', r == '.':
		default:
			return "", false
		}
	}
	return value[:idx], true
}

func looksLikeToken(value string) bool {
	if len(value) < 4 || len(value) > 512 || strings.ContainsAny(value, " \t\n") {
		return false
	}
	return true
}

func lastSegment(pathExpr string) string {
	if idx := strings.LastIndexAny(pathExpr, ".["); idx >= 0 {
		return strings.TrimRight(pathExpr[idx+1:], "]")
	}
	return pathExpr
}

func isFileKey(key string) bool {
	lower := strings.ToLower(key)
	for _, needle := range []string{"file", "attachment", "asset", "image", "document", "upload"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func isReferenceKey(key string) bool {
	lower := strings.ToLower(key)
	if isFileKey(key) {
		return true
	}
	for _, needle := range []string{"reference", "handle", "uri", "url", "resource", "pointer"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

// isNameKey marks the keys that describe a file rather than point to one.
// Without it a filename lands in the report as a handle, which would turn a
// negative result into a false positive.
func isNameKey(key string) bool {
	switch strings.ToLower(key) {
	case "filename", "file_name", "filepath", "file_path", "name", "path", "mime_type", "mimetype", "file_type", "filetype":
		return true
	}
	return false
}

func looksLikeBase64(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '+', r == '/', r == '=', r == '-', r == '_':
		case r == '\n', r == '\r':
		default:
			return false
		}
	}
	return true
}

func decodeLooseBase64(value string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, value)
	var lastErr error
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		decoded, err := enc.DecodeString(cleaned)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// fetchReference resolves a http(s) reference so the real bytes can be
// measured. It refuses anything that is not publicly routable: this process
// shares a network namespace with the Anytype container, so a caller-supplied
// URL is a caller-supplied reach into loopback services if left unchecked.
func fetchReference(rawURL string, timeout time.Duration) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("not a valid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("only http and https references are fetched, got %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil, "", fmt.Errorf("host %q does not resolve: %w", host, err)
	}
	for _, ip := range addrs {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return nil, "", fmt.Errorf("refusing to fetch %q: it resolves to the non-public address %s, and this server sits inside the Anytype container's network", host, ip)
		}
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDecodedBytes+1))
	if err != nil {
		return nil, resp.Status, err
	}
	if len(data) > maxDecodedBytes {
		return nil, resp.Status, fmt.Errorf("reference is larger than %d MiB", maxDecodedBytes>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Status, fmt.Errorf("fetch returned %s", resp.Status)
	}
	return data, resp.Status, nil
}

// summariseJSON echoes a value with long strings replaced by a description of
// them. Repeating a megabyte of base64 back to the caller would cost exactly
// what this tool exists to measure.
func summariseJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = summariseJSON(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, summariseJSON(item))
		}
		return out
	case string:
		if len(v) <= maxPreviewString {
			return v
		}
		return fmt.Sprintf("<string omitted: %d chars, sha256=%s, starts %q>",
			len(v), sha256Hex([]byte(v))[:16], v[:48])
	default:
		return value
	}
}

// describeShape lists the argument keys and their JSON types. It is the part of
// the answer that survives even when every value is unrecognisable.
func describeShape(value any) any {
	obj, ok := value.(map[string]any)
	if !ok {
		return map[string]any{"type": jsonTypeOf(value)}
	}
	out := make(map[string]any, len(obj))
	for k, item := range obj {
		entry := map[string]any{"type": jsonTypeOf(item)}
		if str, isStr := item.(string); isStr {
			entry["length"] = len(str)
		}
		if nested, isObj := item.(map[string]any); isObj {
			entry["keys"] = sortedKeys(nested)
		}
		out[k] = entry
	}
	return out
}

func jsonTypeOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func sortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstStringField(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if value, ok := obj[k].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func findFilename(value any) string {
	found := ""
	var walk func(any)
	walk = func(v any) {
		if found != "" {
			return
		}
		switch node := v.(type) {
		case map[string]any:
			if name := firstStringField(node, "filename", "file_name", "fileName", "name"); name != "" && len(name) <= 255 {
				found = name
				return
			}
			for _, k := range sortedKeys(node) {
				walk(node[k])
			}
		case []any:
			for _, item := range node {
				walk(item)
			}
		}
	}
	walk(value)
	return found
}

func findDeclaredMIME(value any) string {
	found := ""
	var walk func(any)
	walk = func(v any) {
		if found != "" {
			return
		}
		switch node := v.(type) {
		case map[string]any:
			if mimeType := firstStringField(node, "mime_type", "mimeType", "mime", "content_type", "contentType"); strings.Contains(mimeType, "/") {
				found = mimeType
				return
			}
			for _, k := range sortedKeys(node) {
				walk(node[k])
			}
		case []any:
			for _, item := range node {
				walk(item)
			}
		}
	}
	walk(value)
	return found
}

func filenameFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	base := path.Base(parsed.Path)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	return base
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
