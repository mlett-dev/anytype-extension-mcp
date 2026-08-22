package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// Object covers.
//
// These exist because the REST API has no notion of a cover: its update request
// accepts name, icon, type and properties only, and its object model has no
// cover field, so covers can be neither written nor read through the compact
// object tools. Anytype stores a cover in the object's hidden coverId and
// coverType details, which is what these tools write.

func (s *mcpServer) coverToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "object-set-cover",
			"description": "Set the cover image of an object — the banner shown above the title, the GUI's 'Add cover'. Takes an image object that already exists in Anytype, e.g. from file-upload or unsplash-download. This writes ONLY the cover: no block is added to the page, no property is touched, and the body stays exactly as it was. An existing cover is replaced and its old image object is left in place. Pass an empty image_object_id to remove the cover.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":        spaceIDProp(),
				"object_id":       strProp("Object that should get the cover."),
				"image_object_id": strProp("Image object to use, from file-upload or unsplash-download. Empty string removes the cover."),
			}),
		},
		map[string]any{
			"name":        "object-set-cover-from-file",
			"description": "Upload a local image and set it as an object's cover in one step — the equivalent of 'Add cover → Upload image'. The file is taken from the input directory of this server (see file-list-input), becomes a normal Anytype image object, and is then written to the object's cover. An image that exists on YOUR machine is not in that directory and cannot be put there by a path — use object-set-cover-from-attachment for it. NO block is created in the page body; use block-file-create if you want the image inside the text instead. Returns the new image object id so it can be reused.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "staged_path"}, map[string]any{
				"space_id":    spaceIDProp(),
				"object_id":   strProp("Object that should get the cover."),
				"staged_path": strProp("Image inside the input directory. Use the exact name as listed by file-list-input, including spaces and umlauts. PNG, JPG, JPEG, GIF, WEBP and SVG are accepted."),
			}),
		},
		map[string]any{
			"name":        "object-get-cover",
			"description": "Read an object's cover. Needed because the REST API models no cover at all, so get-object-compact can never report one however it is asked. Returns the image object id and, for images placed by hand in the app, the scale and offset.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object to read."),
			}),
		},
	}
}

func (s *mcpServer) dispatchCoverTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "object-set-cover":
		res, err := s.toolSetCover(args)
		return res, err, true
	case "object-set-cover-from-file":
		res, err := s.toolSetCoverFromFile(args)
		return res, err, true
	case "object-get-cover":
		res, err := s.toolGetCover(args)
		return res, err, true
	}
	return nil, nil, false
}

// coverImageExtensions are the formats Anytype renders as a cover.
var coverImageExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
}

func coverResult(cover anytypefiles.Cover) map[string]any {
	out := map[string]any{"type": cover.Type, "is_set": cover.IsSet()}
	if cover.ImageObjectID != "" {
		out["image_object_id"] = cover.ImageObjectID
	}
	if cover.Scale != 0 {
		out["scale"] = cover.Scale
	}
	if cover.X != 0 || cover.Y != 0 {
		out["x"], out["y"] = cover.X, cover.Y
	}
	return out
}

// applyCover writes the cover and reads it back, so success is observed rather
// than assumed.
func (s *mcpServer) applyCover(client *anytypefiles.Client, spaceID, objectID, imageObjectID string) (map[string]any, error) {
	ctx := context.Background()
	if err := client.SetCover(ctx, objectID, imageObjectID); err != nil {
		return nil, err
	}
	cover, err := client.GetCover(ctx, spaceID, objectID)
	if err != nil {
		return nil, fmt.Errorf("the cover was written but could not be read back: %w", err)
	}
	if imageObjectID != "" && cover.ImageObjectID != imageObjectID {
		return nil, fmt.Errorf(
			"the cover did not stick: expected image %s, but the object reports %q",
			imageObjectID, cover.ImageObjectID)
	}
	return coverResult(cover), nil
}

func (s *mcpServer) toolSetCover(args map[string]any) (map[string]any, error) {
	// An empty value is meaningful — it clears the cover — so the key has to be
	// present even though it may be blank.
	if _, ok := args["image_object_id"]; !ok {
		return nil, fmt.Errorf("image_object_id is required; pass an empty string to remove the cover")
	}
	imageObjectID := optionalString(args, "image_object_id")

	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	cover, err := s.applyCover(client, spaceID, objectID, imageObjectID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"cover_set": imageObjectID != "", "cover": cover,
	}, nil
}

func (s *mcpServer) toolSetCoverFromFile(args map[string]any) (map[string]any, error) {
	stagedPath, err := requiredRawString(args, "staged_path")
	if err != nil {
		return nil, err
	}
	resolvedPath, err := resolveUnderRoot(s.cfg.inRoot, stagedPath, false)
	if err != nil {
		suggestions := suggestSimilarInputPaths(s.cfg.inRoot, stagedPath, 8)
		if len(suggestions) > 0 {
			return nil, fmt.Errorf("invalid staged_path: %w. input_root=%q. Use the name exactly as listed, spaces included. Similar files: %s",
				err, s.cfg.inRoot, strings.Join(suggestions, ", "))
		}
		return nil, fmt.Errorf("invalid staged_path: %w. input_root=%q", err, s.cfg.inRoot)
	}
	// Anytype would happily attach any file; a cover that cannot be rendered is
	// worse than a clear refusal.
	if !coverImageExtensions[strings.ToLower(filepath.Ext(resolvedPath))] {
		return nil, fmt.Errorf("%q is not an image format Anytype can use as a cover; use PNG, JPG, JPEG, GIF, WEBP or SVG", stagedPath)
	}
	serverPath, err := mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to map staged_path for server: %w", err)
	}

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
		SpaceID: spaceID, LocalPath: serverPath, Type: fileType, Style: fileStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("uploading %s failed: %w", stagedPath, err)
	}
	if strings.TrimSpace(uploaded.ObjectID) == "" {
		return nil, fmt.Errorf("the upload of %s returned no image object id", stagedPath)
	}

	cover, err := s.applyCover(client, spaceID, objectID, uploaded.ObjectID)
	if err != nil {
		// The image exists either way, so hand its id back rather than losing it.
		return nil, fmt.Errorf("%w (the uploaded image is %s and can be set with object-set-cover)",
			err, uploaded.ObjectID)
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"image_object_id": uploaded.ObjectID, "staged_path": resolvedPath,
		"cover_set": true, "cover": cover,
	}, nil
}

func (s *mcpServer) toolGetCover(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	cover, err := client.GetCover(context.Background(), spaceID, objectID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "cover": coverResult(cover),
	}, nil
}
