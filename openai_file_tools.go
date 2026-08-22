package main

// Files the caller's runtime already holds.
//
// With "openai/fileParams" in a tool's _meta, the runtime resolves one of its
// own files BEFORE the call and substitutes {download_url, file_id, mime_type,
// file_name}. The bytes never enter the conversation; this server fetches them.
//
// These are the only file entrances left for content that is not already on
// this machine. The base64 tools they replaced put the file inside the tool
// call, which spends the caller's tokens at four thirds of the file size — a
// 2 MB image is roughly 700k tokens against a weekly budget that cannot be
// topped up. See the note in file_payload.go for why removing them, rather
// than merely discouraging them, was the only protection that works.
//
// The schema below is written out literally instead of through restSchema and
// strProp. The field names are not ours (file_id, file_name, download_url,
// mime_type — not id, name, uri), and the tool scan checks the shape: all four
// declared, download_url and file_id required, mime_type and file_name not.
// A helper that quietly added a description or an additionalProperties would be
// invisible here and fatal, so this one schema says exactly what it is.

import (
	"fmt"
	"mime"
	"path/filepath"
	"strings"
)

// openAIFileToolMeta marks `file` as a real file argument. This single line is
// the entire mechanism — everything else is an ordinary tool.
func openAIFileToolMeta() map[string]any {
	return map[string]any{"openai/fileParams": []string{"file"}}
}

// openAIFileSchema builds the input schema for a tool whose `file` argument is
// a runtime-resolved file, plus whatever ordinary parameters it needs.
func openAIFileSchema(extraRequired []string, extraProps map[string]any) map[string]any {
	props := map[string]any{"file": map[string]any{"$ref": "#/$defs/OpenAIFile"}}
	for k, v := range extraProps {
		props[k] = v
	}
	return map[string]any{
		"type":       "object",
		"$defs":      openAIFileParamDefs(),
		"properties": props,
		"required":   append([]string{"file"}, extraRequired...),
	}
}

const attachmentIntro = "Takes a file your runtime already holds — an image you just generated, or a file from the conversation — as a real file argument. Select the file as `file`; do not build the object by hand, do not paste a path, and never base64-encode anything. The runtime supplies a temporary download URL and this server fetches the bytes itself, so nothing about the file is paid for as tokens. This is the right tool whenever the file exists on your side."

const attachmentFilenameDesc = "Optional name for the file in Anytype. Defaults to the name the runtime supplies. Keep a real extension: Anytype derives the file type from it."

func (s *mcpServer) attachmentToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "file-upload-attachment",
			"description": attachmentIntro + " Uploads it into Anytype as a file object and returns the object id, which is what icons ({\"format\":\"file\",\"file\":\"<id>\"}), object-set-cover and file properties take. Nothing is left staged.",
			"inputSchema": openAIFileSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"filename": strProp(attachmentFilenameDesc),
				"type":     enumProp("How Anytype should treat the content. Defaults to file, which is also what a PDF is.", []string{"file", "image", "video", "audio", "none"}),
			}),
			"_meta": openAIFileToolMeta(),
		},
		map[string]any{
			"name":        "object-set-cover-from-attachment",
			"description": attachmentIntro + " Uploads it and sets it as an object's cover in one step. NO block is added to the page, no property is touched, and the body stays as it was. The downloaded bytes are checked to actually be an image before anything is uploaded.",
			"inputSchema": openAIFileSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object that should get the cover."),
				"filename":  strProp("Optional name for the image object. Defaults to the runtime's name. Needs a PNG, JPG, JPEG, GIF, WEBP or SVG extension."),
			}),
			"_meta": openAIFileToolMeta(),
		},
		map[string]any{
			"name":        "file-stage-attachment",
			"description": attachmentIntro + fmt.Sprintf(" Writes it into the input directory %q instead of uploading, so that every tool taking a staged_path can use it — file-upload, block-file-create, object-import, object-set-cover-from-file. This is the way to put a generated image into a page as a block. Returns the relative_path to pass on. There is no size limit worth worrying about: the content never passes through the conversation.", s.cfg.inRoot),
			"inputSchema": openAIFileSchema(nil, map[string]any{
				"filename":  strProp(attachmentFilenameDesc),
				"overwrite": map[string]any{"type": "boolean", "description": "Replace an existing file of that name. Without this, an existing name is an error.", "default": false},
			}),
			"_meta": openAIFileToolMeta(),
		},
	}
}

func (s *mcpServer) dispatchAttachmentTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "file-upload-attachment":
		res, err := s.toolUploadAttachment(args)
		return res, err, true
	case "object-set-cover-from-attachment":
		res, err := s.toolSetCoverFromAttachment(args)
		return res, err, true
	case "file-stage-attachment":
		res, err := s.toolStageAttachment(args)
		return res, err, true
	}
	return nil, nil, false
}

// resolvedAttachment is a runtime-held file after this server has fetched it.
type resolvedAttachment struct {
	FileID   string
	Filename string
	MIMEType string
	Data     []byte
	Status   string
}

// resolveAttachment turns the file argument into bytes on this side.
//
// The download URL is a bearer credential with a short life: whoever holds it
// can read the file. It is used here and deliberately never returned — the
// debug tool redacts it for the same reason, and a production result has even
// less business carrying one.
func (s *mcpServer) resolveAttachment(args map[string]any) (resolvedAttachment, error) {
	file, ok := args["file"].(map[string]any)
	if !ok || len(file) == 0 {
		return resolvedAttachment{}, fmt.Errorf("no file argument arrived. Select a file as `file` rather than describing it. If your runtime cannot attach a file at all, there is no way into this server for it: put the file in the input directory of the Anytype host and use the staged_path tools instead")
	}
	downloadURL := firstStringField(file, "download_url")
	fileID := firstStringField(file, "file_id")
	if downloadURL == "" {
		return resolvedAttachment{}, fmt.Errorf("the file argument has no download_url, so there is nothing to fetch. A file_id alone cannot be resolved: MCP has no way for a server to ask a client for file content")
	}

	data, status, err := fetchReference(downloadURL, s.cfg.timeout)
	if err != nil {
		return resolvedAttachment{}, fmt.Errorf("could not download the file: %w. These URLs are short-lived, so a stale one is the first thing to suspect — select the file again", err)
	}
	if len(data) == 0 {
		return resolvedAttachment{}, fmt.Errorf("the download returned no content (%s)", status)
	}

	declaredMIME := firstStringField(file, "mime_type")
	filename, err := attachmentFilename(
		strings.TrimSpace(rawOptionalString(args, "filename")),
		firstStringField(file, "file_name"),
		downloadURL, declaredMIME, data,
	)
	if err != nil {
		return resolvedAttachment{}, err
	}
	return resolvedAttachment{
		FileID: fileID, Filename: filename, MIMEType: sniffMIME(filename, data),
		Data: data, Status: status,
	}, nil
}

// attachmentFilename decides what the file is called here. file_name may be
// absent by contract, and a name without an extension would leave Anytype
// guessing at the type, so the media type fills the gap — declared if there is
// one, sniffed from the content otherwise.
func attachmentFilename(override, runtimeName, downloadURL, declaredMIME string, data []byte) (string, error) {
	name := override
	if name == "" {
		name = runtimeName
	}
	if name == "" {
		name = filenameFromURL(downloadURL)
	}
	// A name from the far side is untrusted input: take the last element only,
	// so nothing can point outside the input directory.
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "attachment"
	}
	if filepath.Ext(name) == "" {
		mimeType := declaredMIME
		if mimeType == "" {
			mimeType = sniffMIME("", data)
		}
		// A URL ending in /image/png leaves "png" as the stem, and appending the
		// extension for that same type would name the file png.png. When the
		// stem is only the media subtype it carries no information, so drop it.
		if subtype := mimeSubtype(mimeType); subtype != "" && strings.EqualFold(name, subtype) {
			name = "attachment"
		}
		if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
			name += exts[0]
		}
	}
	if err := validateTargetName(name); err != nil {
		return "", fmt.Errorf("the name %q cannot be used here: %w. Pass a plain filename as `filename`", name, err)
	}
	return name, nil
}

// mimeSubtype returns the part after the slash, without any parameters.
func mimeSubtype(mimeType string) string {
	_, subtype, found := strings.Cut(mimeType, "/")
	if !found {
		return ""
	}
	if idx := strings.IndexAny(subtype, "; "); idx >= 0 {
		subtype = subtype[:idx]
	}
	return strings.TrimSpace(subtype)
}

// attachmentSource is the provenance block every one of these tools returns. It
// names the file on the caller's side without repeating the download URL.
func (a resolvedAttachment) source() map[string]any {
	return map[string]any{
		"via":         "runtime file argument (openai/fileParams)",
		"file_id":     nullableString(a.FileID),
		"http_status": a.Status,
		"base64_used": false,
	}
}

func (s *mcpServer) toolUploadAttachment(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	attachment, err := s.resolveAttachment(args)
	if err != nil {
		return nil, err
	}
	out, err := s.uploadPayload(spaceID, attachment.Filename, attachment.Data,
		strings.TrimSpace(rawOptionalString(args, "type")))
	if err != nil {
		return nil, err
	}
	out["source"] = attachment.source()
	return out, nil
}

func (s *mcpServer) toolSetCoverFromAttachment(args map[string]any) (map[string]any, error) {
	attachment, err := s.resolveAttachment(args)
	if err != nil {
		return nil, err
	}
	out, err := s.setCoverFromPayload(args, attachment.Filename, attachment.Data)
	if err != nil {
		return nil, err
	}
	out["source"] = attachment.source()
	return out, nil
}

func (s *mcpServer) toolStageAttachment(args map[string]any) (map[string]any, error) {
	attachment, err := s.resolveAttachment(args)
	if err != nil {
		return nil, err
	}
	hostPath, fileSHA, size, err := s.writeIntoInputRoot(
		attachment.Filename, attachment.Data, optionalBool(args, "overwrite", false))
	if err != nil {
		return nil, err
	}
	serverPath, err := mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, hostPath)
	if err != nil {
		return nil, fmt.Errorf("failed to map the staged file for the server: %w", err)
	}
	return map[string]any{
		"relative_path": attachment.Filename,
		"filename":      attachment.Filename,
		"host_path":     hostPath,
		"server_path":   serverPath,
		"input_root":    s.cfg.inRoot,
		"size_bytes":    size,
		"sha256":        fileSHA,
		"mime_type":     attachment.MIMEType,
		"source":        attachment.source(),
		"next":          "Pass relative_path as staged_path to file-upload, block-file-create, object-set-cover-from-file or object-import.",
	}, nil
}
