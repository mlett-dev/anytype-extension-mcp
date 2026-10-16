package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

const (
	maxExportBytes       = 6 << 20
	exportTTL            = 15 * time.Minute
	exportCacheDirName   = ".mcp-exports"
	maxExportManifestLen = 16 << 10

	// The cache is bounded by bytes, so many small images fit; the entry cap
	// only bounds the directory scan on every export. The byte budget is the
	// old worst case of 64 full-size exports.
	maxCachedExports    = 512
	maxExportCacheBytes = 64 * maxExportBytes

	// An identical export is answered with the existing link only while that
	// link still has this long to live.
	minExportReuseLifetime = 5 * time.Minute

	maxExportManyItems = 20
)

type exportManifest struct {
	ObjectID  string    `json:"object_id"`
	Filename  string    `json:"filename"`
	MIMEType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
}

func exportItemProperties() map[string]any {
	return map[string]any{
		"object_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024, "description": "Anytype file object ID."},
		"filename":  map[string]any{"type": "string", "minLength": 1, "maxLength": 255, "description": "Optional download filename, without directories. Changes the name only, not the bytes."},
	}
}

func (s *mcpServer) exportToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "file-export",
			"description": "Hand an Anytype file to the MCP client as a downloadable resource. Returns a native resource_link; the client retrieves the bytes through resources/read. Works for any file type up to 6 MiB (6291456 bytes), unchanged. The link expires 15 minutes after it was created; exporting the same unchanged file again returns the existing link. Use this to return files to ChatGPT; file-download only writes paths on the server. Files above the limit are refused; no preview, compression or truncation is performed.",
			"inputSchema": restSchema([]string{"object_id"}, exportItemProperties()),
		},
		map[string]any{
			"name":        "file-export-many",
			"description": fmt.Sprintf("Export up to %d Anytype files in one call, each as file-export does. Returns one resource_link per exported file, in item order. A file that cannot be exported is listed with its error in results; the others are still exported.", maxExportManyItems),
			"inputSchema": restSchema([]string{"items"}, map[string]any{
				"items": map[string]any{
					"type": "array", "minItems": 1, "maxItems": maxExportManyItems,
					"items": restSchema([]string{"object_id"}, exportItemProperties()),
				},
			}),
		},
	}
}

func parseExportArgs(args map[string]any) (objectID, filename string, err error) {
	for key := range args {
		if key != "object_id" && key != "filename" {
			return "", "", fmt.Errorf("unknown argument: %s", key)
		}
	}
	objectID, ok := args["object_id"].(string)
	objectID = strings.TrimSpace(objectID)
	if !ok || objectID == "" || len(objectID) > 1024 {
		return "", "", errors.New("object_id must be a non-empty string of at most 1024 bytes")
	}
	if value, present := args["filename"]; present {
		if filename, ok = value.(string); !ok {
			return "", "", errors.New("filename must be a string")
		}
		if err := validateExportFilename(filename); err != nil {
			return "", "", err
		}
	}
	return objectID, filename, nil
}

func (s *mcpServer) toolExport(args map[string]any) (nativeToolResult, error) {
	objectID, filename, err := parseExportArgs(args)
	if err != nil {
		return nativeToolResult{}, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nativeToolResult{}, err
	}
	defer client.Close()
	return s.exportFile(client, objectID, filename)
}

// toolExportMany validates every item before exporting any, so a malformed
// call changes nothing. Download, size and cache errors are per item.
func (s *mcpServer) toolExportMany(args map[string]any) (nativeToolResult, error) {
	for key := range args {
		if key != "items" {
			return nativeToolResult{}, fmt.Errorf("unknown argument: %s", key)
		}
	}
	items, err := asObjectSlice(args["items"])
	if err != nil {
		return nativeToolResult{}, fmt.Errorf("items: %w", err)
	}
	if len(items) == 0 || len(items) > maxExportManyItems {
		return nativeToolResult{}, fmt.Errorf("items must hold 1 to %d files", maxExportManyItems)
	}
	type exportItem struct{ objectID, filename string }
	parsed := make([]exportItem, len(items))
	for i, item := range items {
		objectID, filename, err := parseExportArgs(item)
		if err != nil {
			return nativeToolResult{}, fmt.Errorf("items[%d]: %w", i, err)
		}
		parsed[i] = exportItem{objectID, filename}
	}
	client, err := s.grpcClient()
	if err != nil {
		return nativeToolResult{}, err
	}
	defer client.Close()

	results := make([]map[string]any, 0, len(parsed))
	var links []map[string]any
	var failures []string
	for i, item := range parsed {
		exported, err := s.exportFile(client, item.objectID, item.filename)
		if err != nil {
			results = append(results, map[string]any{"index": i, "object_id": item.objectID, "error": err.Error()})
			failures = append(failures, fmt.Sprintf("items[%d]: %v", i, err))
			continue
		}
		entry := map[string]any{"index": i}
		for key, value := range exported.Metadata {
			entry[key] = value
		}
		results = append(results, entry)
		links = append(links, exported.Content...)
	}
	if len(links) == 0 {
		return nativeToolResult{}, errors.New("no file was exported: " + strings.Join(failures, "; "))
	}
	return nativeToolResult{
		Metadata: map[string]any{"exported": len(links), "failed": len(failures), "results": results},
		Content:  links,
	}, nil
}

func (s *mcpServer) exportFile(client *anytypefiles.Client, objectID, filename string) (nativeToolResult, error) {
	// Download into a unique shared directory, rather than renaming or deleting
	// an existing file-download output. Heart may run as a different user.
	outRoot, err := ensureDir(s.cfg.outRoot)
	if err != nil {
		return nativeToolResult{}, err
	}
	staging, err := os.MkdirTemp(outRoot, ".mcp-export-download-")
	if err != nil {
		return nativeToolResult{}, err
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0755); err != nil {
		return nativeToolResult{}, err
	}
	serverStaging, err := mapHostPathToServerPath(outRoot, s.cfg.serverOutRoot, staging)
	if err != nil {
		return nativeToolResult{}, err
	}
	download, err := downloadAnytypeFileWith(client, objectID, staging, serverStaging)
	if err != nil {
		return nativeToolResult{}, err
	}
	if filename == "" {
		filename = filepath.Base(download.localPath)
	}
	return s.createFileExport(objectID, filename, staging, download.localPath, time.Now())
}

func validateExportFilename(name string) error {
	if err := validateTargetName(name); err != nil {
		return fmt.Errorf("invalid filename: %w", err)
	}
	if len(name) > 255 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("filename must have at most 255 bytes and no control characters")
	}
	return nil
}

// createFileExport reads at most 6 MiB + 1 byte through a confined file handle.
// The snapshot and manifest survive process/container restarts on the shared
// output volume. No in-memory registration is required by resources/read.
func (s *mcpServer) createFileExport(objectID, name, sourceRoot, sourcePath string, now time.Time) (nativeToolResult, error) {
	if err := validateExportFilename(name); err != nil {
		return nativeToolResult{}, err
	}
	root, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return nativeToolResult{}, err
	}
	defer root.Close()
	rel, err := filepath.Rel(sourceRoot, sourcePath)
	if err != nil {
		return nativeToolResult{}, err
	}
	file, err := root.Open(rel)
	if err != nil {
		return nativeToolResult{}, fmt.Errorf("cannot open downloaded file within export staging: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nativeToolResult{}, err
	}
	if !info.Mode().IsRegular() {
		return nativeToolResult{}, errors.New("only regular files can be exported")
	}
	if info.Size() > maxExportBytes {
		return nativeToolResult{}, exportSizeError(info.Size())
	}
	data, err := io.ReadAll(io.LimitReader(file, maxExportBytes+1))
	if err != nil {
		return nativeToolResult{}, err
	}
	if len(data) > maxExportBytes {
		return nativeToolResult{}, exportSizeError(int64(len(data)))
	}

	cache, err := s.openExportCache()
	if err != nil {
		return nativeToolResult{}, err
	}
	defer cache.Close()
	active, err := cleanupFileExports(cache, now)
	if err != nil {
		return nativeToolResult{}, err
	}
	sum := sha256Hex(data)
	var used int64
	var nextExpiry time.Time
	for _, export := range active {
		m := export.manifest
		if m.ObjectID == objectID && m.Filename == name && m.SHA256 == sum && m.SizeBytes == int64(len(data)) &&
			m.ExpiresAt.Sub(now) >= minExportReuseLifetime && exportDataIntact(cache, export.handle, m) {
			return exportResult(export.handle, m), nil
		}
		used += m.SizeBytes
		if !m.ExpiresAt.IsZero() && (nextExpiry.IsZero() || m.ExpiresAt.Before(nextExpiry)) {
			nextExpiry = m.ExpiresAt
		}
	}
	if len(active) >= maxCachedExports || used+int64(len(data)) > maxExportCacheBytes {
		return nativeToolResult{}, exportCacheFullError(len(active), used, nextExpiry, now)
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nativeToolResult{}, err
	}
	handle := hex.EncodeToString(random[:])
	if err := cache.Mkdir(handle, 0700); err != nil {
		return nativeToolResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = cache.RemoveAll(handle)
		}
	}()
	manifest := exportManifest{
		ObjectID: objectID, Filename: name, MIMEType: sniffMIME(name, data),
		SizeBytes: int64(len(data)), SHA256: sum, ExpiresAt: now.UTC().Add(exportTTL),
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nativeToolResult{}, err
	}
	if err := cache.WriteFile(filepath.Join(handle, "data"), data, 0600); err != nil {
		return nativeToolResult{}, err
	}
	if err := cache.WriteFile(filepath.Join(handle, "manifest.json"), encoded, 0600); err != nil {
		return nativeToolResult{}, err
	}
	committed = true
	return exportResult(handle, manifest), nil
}

func exportResult(handle string, manifest exportManifest) nativeToolResult {
	uri := (&url.URL{Scheme: "anytype-export", Host: handle, Path: "/" + manifest.Filename}).String()
	return nativeToolResult{
		Metadata: map[string]any{
			"object_id": manifest.ObjectID, "uri": uri, "filename": manifest.Filename, "mime_type": manifest.MIMEType,
			"size_bytes": manifest.SizeBytes, "sha256": manifest.SHA256,
			"expires_at": manifest.ExpiresAt.UTC().Format(time.RFC3339),
		},
		Content: []map[string]any{{"type": "resource_link", "uri": uri, "name": manifest.Filename, "mimeType": manifest.MIMEType, "size": manifest.SizeBytes}},
	}
}

// exportDataIntact is a cheap check before a link is handed out again;
// resources/read still verifies the SHA-256.
func exportDataIntact(cache *os.Root, handle string, manifest exportManifest) bool {
	info, err := cache.Lstat(filepath.Join(handle, "data"))
	return err == nil && info.Mode().IsRegular() && info.Size() == manifest.SizeBytes
}

func exportCacheFullError(count int, used int64, nextExpiry, now time.Time) error {
	msg := fmt.Sprintf("export cache is full: %d of %d exports, %.1f of %d MiB in use", count, maxCachedExports, float64(used)/(1<<20), maxExportCacheBytes>>20)
	if !nextExpiry.IsZero() {
		msg += fmt.Sprintf("; the next export expires at %s (in %s)", nextExpiry.UTC().Format(time.RFC3339), nextExpiry.Sub(now).Round(time.Second))
	}
	return errors.New(msg)
}

func exportSizeError(size int64) error {
	return fmt.Errorf("file is %d bytes; file-export supports at most 6 MiB (%d bytes). The original was not exported or modified; previews and larger-file transfers are not implemented", size, maxExportBytes)
}

func (s *mcpServer) openExportCache() (*os.Root, error) {
	outRoot, err := ensureDir(s.cfg.outRoot)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(outRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir(exportCacheDirName, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := root.Lstat(exportCacheDirName)
	if err != nil || !info.IsDir() {
		return nil, errors.New("export cache must be a directory, not a symlink")
	}
	return root.OpenRoot(exportCacheDirName)
}

func validExportHandle(handle string) bool {
	if len(handle) != 48 || handle != strings.ToLower(handle) {
		return false
	}
	_, err := hex.DecodeString(handle)
	return err == nil
}

func readExportManifest(cache *os.Root, handle string) (exportManifest, error) {
	var manifest exportManifest
	file, err := cache.Open(filepath.Join(handle, "manifest.json"))
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxExportManifestLen+1))
	if err != nil {
		return manifest, err
	}
	if len(data) > maxExportManifestLen {
		return manifest, errors.New("invalid export manifest size")
	}
	err = json.Unmarshal(data, &manifest)
	if err == nil && (manifest.ExpiresAt.IsZero() || manifest.SizeBytes < 0 || manifest.SizeBytes > maxExportBytes || validateExportFilename(manifest.Filename) != nil || manifest.MIMEType == "" || len(manifest.SHA256) != 64) {
		err = errors.New("invalid export manifest")
	}
	return manifest, err
}

// cachedExport is an unexpired cache entry. manifest is zero while a snapshot
// is still being written or when its manifest is unreadable.
type cachedExport struct {
	handle   string
	manifest exportManifest
}

// Expired exports and abandoned partial snapshots are removed lazily on the
// next export. Unknown names are left alone; this directory is not a catalog.
func cleanupFileExports(cache *os.Root, now time.Time) ([]cachedExport, error) {
	dir, err := cache.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var active []cachedExport
	for _, entry := range entries {
		if !validExportHandle(entry.Name()) {
			continue
		}
		manifest, err := readExportManifest(cache, entry.Name())
		expired := err == nil && !now.Before(manifest.ExpiresAt)
		if err != nil {
			info, statErr := entry.Info()
			expired = statErr == nil && !now.Before(info.ModTime().Add(exportTTL))
		}
		if expired {
			if err := cache.RemoveAll(entry.Name()); err != nil {
				return active, err
			}
			continue
		}
		if err != nil {
			manifest = exportManifest{}
		}
		active = append(active, cachedExport{handle: entry.Name(), manifest: manifest})
	}
	return active, nil
}

func (s *mcpServer) readExportResource(req rpcRequest) rpcResponse {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.URI == "" {
		return s.errorResponse(req.ID, -32602, "resources/read requires a resource URI")
	}
	uri, err := url.Parse(params.URI)
	if err != nil || uri.Scheme != "anytype-export" || !validExportHandle(uri.Host) || uri.User != nil || uri.RawQuery != "" || uri.ForceQuery || uri.Fragment != "" || validateExportFilename(strings.TrimPrefix(uri.Path, "/")) != nil || !strings.HasPrefix(uri.Path, "/") {
		return s.errorResponse(req.ID, -32602, "invalid anytype-export resource URI")
	}
	cache, err := s.openExportCache()
	if err != nil {
		return s.errorResponse(req.ID, -32603, "export cache is not accessible")
	}
	defer cache.Close()
	manifest, err := readExportManifest(cache, uri.Host)
	if errors.Is(err, os.ErrNotExist) {
		return s.errorResponse(req.ID, -32002, "export resource not found or expired; call file-export again")
	}
	if err != nil {
		return s.errorResponse(req.ID, -32603, "export resource metadata is not readable")
	}
	if !time.Now().Before(manifest.ExpiresAt) {
		_ = cache.RemoveAll(uri.Host)
		return s.errorResponse(req.ID, -32002, "export resource expired; call file-export again")
	}
	if uri.Path != "/"+manifest.Filename {
		return s.errorResponse(req.ID, -32002, "export resource not found")
	}
	file, err := cache.Open(filepath.Join(uri.Host, "data"))
	if err != nil {
		return s.errorResponse(req.ID, -32603, "export resource data is not readable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != manifest.SizeBytes {
		return s.errorResponse(req.ID, -32603, "export resource data is not a valid snapshot")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxExportBytes+1))
	if err != nil || int64(len(data)) != manifest.SizeBytes || sha256Hex(data) != manifest.SHA256 {
		return s.errorResponse(req.ID, -32603, "export resource integrity check failed")
	}
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"contents": []map[string]any{{"uri": params.URI, "mimeType": manifest.MIMEType, "blob": base64.StdEncoding.EncodeToString(data)}},
	}}
}
