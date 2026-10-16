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
)

const (
	maxExportBytes       = 6 << 20
	exportTTL            = 15 * time.Minute
	exportCacheDirName   = ".mcp-exports"
	maxCachedExports     = 64
	maxExportManifestLen = 16 << 10
)

type exportManifest struct {
	ObjectID  string    `json:"object_id"`
	Filename  string    `json:"filename"`
	MIMEType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *mcpServer) exportToolDefs() []any {
	return []any{map[string]any{
		"name":        "file-export",
		"description": "Hand an Anytype file to the MCP client as a downloadable resource. Returns a native resource_link; the client retrieves the bytes through resources/read. Works for any file type up to 6 MiB (6291456 bytes), unchanged. The link expires after 15 minutes. Use this to return files to ChatGPT; file-download only writes paths on the server. Files above the limit are refused; no preview, compression or truncation is performed.",
		"inputSchema": restSchema([]string{"object_id"}, map[string]any{
			"object_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024, "description": "Anytype file object ID."},
			"filename":  map[string]any{"type": "string", "minLength": 1, "maxLength": 255, "description": "Optional download filename, without directories. Changes the name only, not the bytes."},
		}),
	}}
}

func (s *mcpServer) toolExport(args map[string]any) (nativeToolResult, error) {
	for key := range args {
		if key != "object_id" && key != "filename" {
			return nativeToolResult{}, fmt.Errorf("unknown argument: %s", key)
		}
	}
	objectID, ok := args["object_id"].(string)
	objectID = strings.TrimSpace(objectID)
	if !ok || objectID == "" || len(objectID) > 1024 {
		return nativeToolResult{}, errors.New("object_id must be a non-empty string of at most 1024 bytes")
	}
	filename := ""
	if value, present := args["filename"]; present {
		var ok bool
		filename, ok = value.(string)
		if !ok {
			return nativeToolResult{}, errors.New("filename must be a string")
		}
		if err := validateExportFilename(filename); err != nil {
			return nativeToolResult{}, err
		}
	}

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
	download, err := s.downloadAnytypeFile(objectID, staging, serverStaging)
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
	if active >= maxCachedExports {
		return nativeToolResult{}, errors.New("export cache is full; retry after existing exports expire (15 minutes)")
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
		SizeBytes: int64(len(data)), SHA256: sha256Hex(data), ExpiresAt: now.UTC().Add(exportTTL),
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
	uri := (&url.URL{Scheme: "anytype-export", Host: handle, Path: "/" + name}).String()
	return nativeToolResult{
		Metadata: map[string]any{
			"object_id": objectID, "uri": uri, "filename": name, "mime_type": manifest.MIMEType,
			"size_bytes": manifest.SizeBytes, "sha256": manifest.SHA256,
			"expires_at": manifest.ExpiresAt.Format(time.RFC3339),
		},
		Content: []map[string]any{{"type": "resource_link", "uri": uri, "name": name, "mimeType": manifest.MIMEType, "size": manifest.SizeBytes}},
	}, nil
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

// Expired exports and abandoned partial snapshots are removed lazily on the
// next export. Unknown names are left alone; this directory is not a catalog.
func cleanupFileExports(cache *os.Root, now time.Time) (int, error) {
	dir, err := cache.Open(".")
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	active := 0
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
		} else {
			active++
		}
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
