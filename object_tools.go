package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// Object-level GUI actions that the REST API does not cover, plus the file
// block tool.
//
// The most consequential one is object-set-archived: delete-object archives an
// object and REST offers no way back, so without this an accidental deletion
// was permanent as far as the model was concerned.
//
// There is deliberately no block-file-set-name tool. The BlockFileSetName RPC
// exists but is an unimplemented stub in anytype-heart v0.50.8 — it reports
// success and changes nothing, so exposing it would only mislead. Renaming
// works through the file object the block points at (target_object_id from
// block-list) with update-object-compact.

func (s *mcpServer) objectToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "object-set-archived",
			"description": "Move objects to the bin, or restore them from it. archived=false is the ONLY way to undo delete-object, and it also undoes delete-property, delete-tag and delete-type — it takes any object id, schema objects included. Restored objects show up in searches and lists again, but the search index lags roughly a second behind — an immediate re-read can still report the old state, which is not a failure. Read the current state with object-flags, and see what is in the bin with list-archived.",
			"inputSchema": restSchema([]string{"space_id", "object_ids", "archived"}, map[string]any{
				"space_id": spaceIDProp(),
				"object_ids": map[string]any{
					"type":        "array",
					"description": "Objects to archive or restore.",
					"items":       map[string]any{"type": "string"},
				},
				"archived": map[string]any{"type": "boolean", "description": "true moves them to the bin, false restores them."},
			}),
		},
		map[string]any{
			"name":        "object-set-favorite",
			"description": "Add objects to the Favorites section of the sidebar, or remove them from it.",
			"inputSchema": restSchema([]string{"space_id", "object_ids", "favorite"}, map[string]any{
				"space_id": spaceIDProp(),
				"object_ids": map[string]any{
					"type":        "array",
					"description": "Objects to favourite or unfavourite.",
					"items":       map[string]any{"type": "string"},
				},
				"favorite": map[string]any{"type": "boolean", "description": "true adds to Favorites, false removes. Read the current state with object-flags."},
			}),
		},
		map[string]any{
			"name":        "object-undo",
			"description": "Undo the last change to one object, or redo it with direction=redo. The history is per object and covers block edits and property changes made through this server. Use it to back out an edit that went wrong instead of reconstructing the previous state by hand. It does NOT cover archiving: an object removed with delete-object comes back via object-set-archived with archived=false, not through undo.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object whose history to step through."),
				"direction": enumProp("undo steps backwards, redo steps forwards again. Defaults to undo.", []string{"undo", "redo"}),
				"steps":     map[string]any{"type": "integer", "description": "How many steps to take. Defaults to 1. Stops early when the history runs out.", "default": 1},
			}),
		},
		map[string]any{
			"name":        "block-file-create",
			"description": "Insert a file, image, video, audio or PDF block into a page and upload its content. Pass staged_path for a file placed in the input directory (see file-list-input), or url to fetch it from the web. This differs from file-upload, which creates a standalone file object rather than a block inside a page. The upload runs asynchronously: check file_state in block-list, which becomes done when it finishes. A url that cannot be fetched (404, 403, hotlink protection) leaves the block stuck at uploading rather than error, so treat a state that never changes as a failed download.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"object_id":   strProp("Object to insert the block into."),
				"kind":        enumProp("Block type. Defaults to file; use image to render it inline.", anytypefiles.FileBlockTypeNames()),
				"staged_path": strProp("Path of a file inside the input directory. Use file-list-input to see what is staged."),
				"url":         strProp("URL to fetch the file from, instead of staged_path."),
				"target_id":   strProp("Block id to insert relative to. Omit to append at the end."),
				"position":    enumProp("Where to insert relative to target_id. Defaults to bottom.", anytypefiles.BlockPositionNames()),
			}),
		},
	}
}

func (s *mcpServer) dispatchObjectTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "object-set-archived":
		res, err := s.toolObjectSetArchived(args)
		return res, err, true
	case "object-set-favorite":
		res, err := s.toolObjectSetFavorite(args)
		return res, err, true
	case "object-undo":
		res, err := s.toolObjectUndo(args)
		return res, err, true
	case "block-file-create":
		res, err := s.toolBlockFileCreate(args)
		return res, err, true
	}
	return nil, nil, false
}

// objectFlagArgs pulls the shared arguments of the favourite/archive tools.
func (s *mcpServer) objectFlagArgs(args map[string]any, flagKey string) (*anytypefiles.Client, string, []string, bool, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, "", nil, false, err
	}
	objectIDs := optionalStringSlice(args, "object_ids")
	if len(objectIDs) == 0 {
		return nil, "", nil, false, fmt.Errorf("object_ids is required")
	}
	raw, ok := args[flagKey]
	if !ok {
		return nil, "", nil, false, fmt.Errorf("%s is required", flagKey)
	}
	flag, ok := raw.(bool)
	if !ok {
		return nil, "", nil, false, fmt.Errorf("%s must be a boolean", flagKey)
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, "", nil, false, err
	}
	return client, spaceID, objectIDs, flag, nil
}

func (s *mcpServer) toolObjectSetArchived(args map[string]any) (map[string]any, error) {
	client, spaceID, objectIDs, archived, err := s.objectFlagArgs(args, "archived")
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetArchived(context.Background(), objectIDs, archived); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_ids": objectIDs,
		"archived": archived, "updated": true,
	}, nil
}

func (s *mcpServer) toolObjectSetFavorite(args map[string]any) (map[string]any, error) {
	client, spaceID, objectIDs, favorite, err := s.objectFlagArgs(args, "favorite")
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetFavorite(context.Background(), objectIDs, favorite); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_ids": objectIDs,
		"favorite": favorite, "updated": true,
	}, nil
}

func (s *mcpServer) toolObjectUndo(args map[string]any) (map[string]any, error) {
	direction := strings.ToLower(strings.TrimSpace(optionalString(args, "direction")))
	if direction == "" {
		direction = "undo"
	}
	if direction != "undo" && direction != "redo" {
		return nil, fmt.Errorf("direction must be undo or redo, got %q", direction)
	}
	steps := optionalInt(args, "steps", 1)
	if steps <= 0 {
		steps = 1
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	ctx := context.Background()
	applied := 0
	var last anytypefiles.UndoResult
	for i := 0; i < steps; i++ {
		result, ok, err := client.UndoRedo(ctx, spaceID, objectID, direction == "redo")
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		last = result
		applied++
	}

	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"direction": direction, "steps_requested": steps, "steps_applied": applied,
	}
	if applied == 0 {
		out["note"] = "nothing left to " + direction + " for this object"
	} else {
		out["undo_steps_left"] = last.Undo
		out["redo_steps_left"] = last.Redo
	}
	return out, nil
}

func (s *mcpServer) toolBlockFileCreate(args map[string]any) (map[string]any, error) {
	stagedPath := strings.TrimSpace(rawOptionalString(args, "staged_path"))
	url := strings.TrimSpace(optionalString(args, "url"))
	if stagedPath == "" && url == "" {
		return nil, fmt.Errorf("pass staged_path or url")
	}
	if stagedPath != "" && url != "" {
		return nil, fmt.Errorf("pass either staged_path or url, not both")
	}

	// The upload happens inside anytype-heart, so the path has to be expressed
	// in the server's filesystem. This is the same mapping file-upload uses.
	serverPath := ""
	resolvedPath := ""
	if stagedPath != "" {
		var err error
		resolvedPath, err = resolveUnderRoot(s.cfg.inRoot, stagedPath, false)
		if err != nil {
			suggestions := suggestSimilarInputPaths(s.cfg.inRoot, stagedPath, 8)
			if len(suggestions) > 0 {
				return nil, fmt.Errorf("invalid staged_path: %w. input_root=%q. Similar files: %s", err, s.cfg.inRoot, strings.Join(suggestions, ", "))
			}
			return nil, fmt.Errorf("invalid staged_path: %w. input_root=%q", err, s.cfg.inRoot)
		}
		serverPath, err = mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to map staged_path for server: %w", err)
		}
	}

	kind := optionalString(args, "kind")
	if kind == "" {
		kind = "file"
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	blockID, err := client.CreateFileBlock(context.Background(), objectID,
		optionalString(args, "target_id"), optionalString(args, "position"),
		kind, serverPath, url)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": blockID, "kind": kind, "created": true,
	}
	if serverPath != "" {
		out["staged_path"] = resolvedPath
		out["server_path"] = serverPath
	}
	if url != "" {
		out["url"] = url
	}
	return out, nil
}
