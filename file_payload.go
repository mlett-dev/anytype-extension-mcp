package main

// What every file entrance does once it has the bytes.
//
// This file holds no tools. It is the shared plumbing under them: identify the
// content, put it where the Anytype server can read it, upload it, make it a
// cover. The tools differ only in where the bytes came from, and keeping the
// rest here is what stops those routes from drifting apart.
//
// It used to hold the base64 trio — file-stage-bytes, file-upload-bytes,
// object-set-cover-from-bytes — which carried file content inside the tool call
// because a caller on another machine had no other way in. Those were removed
// on 2026-08-10 in favour of the -attachment tools in openai_file_tools.go, and
// the reason is worth keeping: base64 in a tool call is paid for in the calling
// model's tokens, at four thirds of the file size, against a weekly budget the
// operator cannot top up. A single 2 MB image is roughly 700k tokens. Since the
// runtime can hand over a file as a reference instead, the encoded route was
// not a slower option but a live hazard — one wrong choice by the model spends
// a week's allowance. A tool that cannot be called cannot be chosen, and that
// is the only protection that works: the tokens are gone the moment the model
// emits the call, long before this server sees it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// maxDecodedBytes caps how much content one call may bring in. It is a guard
// against a runaway allocation, not a policy — the fetch in fetchReference
// stops here rather than reading an unbounded body.
const maxDecodedBytes = 24 << 20

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha256File(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

// looksLikeSVG reports whether the bytes really are SVG markup.
//
// Go's content sniffer has no SVG signature: an SVG opening with <?xml comes
// back as text/xml, one opening with <svg as text/plain. Trusting the .svg
// extension alone would give up the truncation check that makes sniffing worth
// doing, so the markup itself is what gets looked for.
func looksLikeSVG(data []byte) bool {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	return bytes.Contains(bytes.ToLower(head), []byte("<svg"))
}

// sniffMIME reports what the bytes are, which is not the same as what the
// filename claims. The difference is the point: it is how a truncated or
// mis-encoded payload is caught before it becomes an Anytype object.
func sniffMIME(name string, data []byte) string {
	if strings.EqualFold(filepath.Ext(name), ".svg") && looksLikeSVG(data) {
		return "image/svg+xml"
	}
	limit := len(data)
	if limit > 512 {
		limit = 512
	}
	sniffed := http.DetectContentType(data[:limit])
	if sniffed != "application/octet-stream" {
		return sniffed
	}
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExt != "" {
		return byExt
	}
	return sniffed
}

// stagedBytes is a payload that has been written to the input directory and is
// ready to hand to the Anytype server.
type stagedBytes struct {
	hostPath   string
	serverPath string
	tempDir    string
	name       string
	size       int64
	sha256     string
	mimeType   string
}

// remove deletes a payload staged in a temporary directory. Files that
// file-stage-attachment put into the input root are the caller's and are never
// removed here.
func (st stagedBytes) remove() {
	if st.tempDir != "" {
		os.RemoveAll(st.tempDir)
	}
}

// stageForUpload writes the bytes into a private subdirectory of the input
// root. The subdirectory keeps the caller's filename intact — Anytype names the
// file object after it — while making a collision with a concurrent call or an
// existing staged file impossible.
func (s *mcpServer) stageForUpload(filename string, data []byte) (stagedBytes, error) {
	if err := validateTargetName(filename); err != nil {
		return stagedBytes{}, fmt.Errorf("invalid filename: %w", err)
	}
	inRoot, err := ensureDir(s.cfg.inRoot)
	if err != nil {
		return stagedBytes{}, fmt.Errorf("input root not usable: %w", err)
	}
	tempDir, err := os.MkdirTemp(inRoot, ".bytes-")
	if err != nil {
		return stagedBytes{}, fmt.Errorf("cannot stage in %s: %w", inRoot, err)
	}
	// The Anytype server reads this path as another user; the default 0700 on
	// MkdirTemp would hide the file from it.
	if err := os.Chmod(tempDir, 0o755); err != nil {
		os.RemoveAll(tempDir)
		return stagedBytes{}, err
	}
	hostPath := filepath.Join(tempDir, filename)
	if err := os.WriteFile(hostPath, data, 0o644); err != nil {
		os.RemoveAll(tempDir)
		return stagedBytes{}, fmt.Errorf("writing the staged file failed: %w", err)
	}
	serverPath, err := mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, hostPath)
	if err != nil {
		os.RemoveAll(tempDir)
		return stagedBytes{}, fmt.Errorf("failed to map the staged file for the server: %w", err)
	}
	return stagedBytes{
		hostPath: hostPath, serverPath: serverPath, tempDir: tempDir,
		name: filename, size: int64(len(data)),
		sha256: sha256Hex(data), mimeType: sniffMIME(filename, data),
	}, nil
}

// writeIntoInputRoot puts a payload into the input root under the caller's own
// name, where it stays for the staged_path tools to pick up.
func (s *mcpServer) writeIntoInputRoot(filename string, data []byte, overwrite bool) (string, string, int64, error) {
	inRoot, err := ensureDir(s.cfg.inRoot)
	if err != nil {
		return "", "", 0, fmt.Errorf("input root not usable: %w", err)
	}
	hostPath := filepath.Join(inRoot, filename)

	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(hostPath, flags, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", "", 0, fmt.Errorf("%q already exists in %s; pass overwrite=true to replace it", filename, inRoot)
		}
		return "", "", 0, fmt.Errorf("cannot write %q in %s: %w", filename, inRoot, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", "", 0, fmt.Errorf("writing %q failed: %w", filename, err)
	}
	if err := file.Close(); err != nil {
		return "", "", 0, err
	}

	// Hash what is now on disk, which is what the caller will pass on.
	fileSHA, totalSize, err := sha256File(hostPath)
	if err != nil {
		return "", "", 0, err
	}
	return hostPath, fileSHA, totalSize, nil
}

// uploadPayload turns bytes already in hand into an Anytype file object. Where
// the bytes came from — base64 in the call, or a download this server did
// itself — makes no difference from here on, so both routes share this tail and
// cannot drift apart.
func (s *mcpServer) uploadPayload(spaceID, filename string, data []byte, typeValue string) (map[string]any, error) {
	if typeValue == "" {
		typeValue = "file"
	}
	fileType, err := anytypefiles.ParseFileType(typeValue)
	if err != nil {
		return nil, err
	}
	fileStyle, err := anytypefiles.ParseFileStyle("auto")
	if err != nil {
		return nil, err
	}

	staged, err := s.stageForUpload(filename, data)
	if err != nil {
		return nil, err
	}
	// The staged copy is only a handover to the Anytype server; it must not be
	// left behind in the shared input directory.
	defer staged.remove()

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	uploaded, err := client.UploadFile(context.Background(), anytypefiles.UploadRequest{
		SpaceID: spaceID, LocalPath: staged.serverPath, Type: fileType, Style: fileStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("uploading %s failed: %w", filename, err)
	}
	if strings.TrimSpace(uploaded.ObjectID) == "" {
		return nil, fmt.Errorf("the upload of %s returned no object id", filename)
	}
	return map[string]any{
		"space_id": spaceID, "object_id": uploaded.ObjectID,
		"preload_file_id": uploaded.PreloadFileID,
		"filename":        filename, "size_bytes": staged.size,
		"sha256": staged.sha256, "mime_type": staged.mimeType,
		"type": typeValue,
	}, nil
}

// validateCoverFilename refuses a name Anytype cannot render as a cover.
// Anytype would attach anything; a cover that does not render is worse than a
// clear refusal.
func validateCoverFilename(filename string) error {
	if !coverImageExtensions[strings.ToLower(filepath.Ext(filename))] {
		return fmt.Errorf("%q is not an image format Anytype can use as a cover; use PNG, JPG, JPEG, GIF, WEBP or SVG", filename)
	}
	return nil
}

// setCoverFromPayload uploads bytes already in hand and makes them an object's
// cover. Shared tail of the base64 and file-argument routes.
func (s *mcpServer) setCoverFromPayload(args map[string]any, filename string, data []byte) (map[string]any, error) {
	if err := validateCoverFilename(filename); err != nil {
		return nil, err
	}
	// What the bytes are, not what the name claims. A payload that lost a chunk
	// still ends in .png but no longer sniffs as an image, and catching that
	// here is cheaper than a broken banner nobody notices.
	sniffed := sniffMIME(filename, data)
	if !strings.HasPrefix(sniffed, "image/") {
		return nil, fmt.Errorf("the decoded content is %s, not an image — the payload is most likely truncated or was not raw file bytes (%d bytes received)",
			sniffed, len(data))
	}

	staged, err := s.stageForUpload(filename, data)
	if err != nil {
		return nil, err
	}
	defer staged.remove()

	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fileType, err := anytypefiles.ParseFileType("image")
	if err != nil {
		return nil, err
	}
	fileStyle, err := anytypefiles.ParseFileStyle("auto")
	if err != nil {
		return nil, err
	}
	uploaded, err := client.UploadFile(context.Background(), anytypefiles.UploadRequest{
		SpaceID: spaceID, LocalPath: staged.serverPath, Type: fileType, Style: fileStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("uploading %s failed: %w", filename, err)
	}
	if strings.TrimSpace(uploaded.ObjectID) == "" {
		return nil, fmt.Errorf("the upload of %s returned no image object id", filename)
	}

	cover, err := s.applyCover(client, spaceID, objectID, uploaded.ObjectID)
	if err != nil {
		// The image exists either way, so hand its id back rather than losing it.
		return nil, fmt.Errorf("%w (the uploaded image is %s and can be set with object-set-cover)",
			err, uploaded.ObjectID)
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"image_object_id": uploaded.ObjectID,
		"filename":        filename, "size_bytes": staged.size,
		"sha256": staged.sha256, "mime_type": staged.mimeType,
		"cover_set": true, "cover": cover,
	}, nil
}
