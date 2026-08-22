package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// Export, import, version history and a handful of object actions the REST API
// does not reach.
//
// Export and import run inside anytype-heart, so the paths they get must be
// paths in the server's filesystem. Both reuse the input and output roots the
// file tools already share with the server, so a model only ever names a
// relative path.

// modifyShapeNote says what an add/remove/set value has to look like.
//
// The three fields are deliberately untyped in the schema — they carry a
// different shape per property format — which left the model guessing. Naming
// the shapes here is the part that was actually missing.
const modifyShapeNote = "Shape follows the property's format: multi_select and objects take a tag id / object id or an array of them, files an object id or an array, text/url/email/phone a string, number a number, checkbox true or false, date an RFC3339 datetime or YYYY-MM-DD. add and remove only make sense for the multi-value formats (multi_select, objects, files); use set for the rest. Check the format with get-type-compact or list-properties first."

func (s *mcpServer) transferToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "object-export",
			"description": "Read one object as markdown, JSON or protobuf and get the content back directly, without writing a file. Unlike get-object-compact with markdown this is Anytype's own exporter, so it renders the full document the way an export would.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object to export."),
				"format":    enumProp("Export format. Defaults to markdown.", anytypefiles.ExportFormatNames()),
			}),
		},
		map[string]any{
			"name":        "object-export-files",
			"description": "Export objects to files in the output directory, which is shared with the host. Use this for backups or to hand content to another tool; use object-export when you just want to read one object. Omit object_ids to export the whole space.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"object_ids": map[string]any{
					"type":        "array",
					"description": "Objects to export. Omit to export everything in the space.",
					"items":       map[string]any{"type": "string"},
				},
				"target_dir":        strProp("Subdirectory of the output root to write into. Defaults to an \"export\" folder."),
				"format":            enumProp("Export format. Defaults to markdown.", anytypefiles.ExportFormatNames()),
				"zip":               map[string]any{"type": "boolean", "description": "Write a single zip file instead of a directory tree.", "default": false},
				"include_nested":    map[string]any{"type": "boolean", "description": "Also export objects linked from the selected ones.", "default": false},
				"include_files":     map[string]any{"type": "boolean", "description": "Also export attached files and images.", "default": false},
				"include_archived":  map[string]any{"type": "boolean", "description": "Include archived objects.", "default": false},
				"include_backlinks": map[string]any{"type": "boolean", "description": "Also export objects that link to the selected ones.", "default": false},
				"markdown_schema":   map[string]any{"type": "boolean", "description": "For markdown: write property frontmatter and a schema folder.", "default": false},
			}),
		},
		map[string]any{
			"name":        "object-import",
			"description": "Import external content into a space: markdown, HTML, plain text, CSV, an Anytype protobuf export, an Obsidian vault, or a whole Notion workspace. Files are read from the input directory shared with the host — stage them there first and check with file-list-input. This is the efficient path for bulk content; block-paste is better for a single page. IMPORTANT: Anytype runs the import in the background and reports nothing back — not a count, not even a failure. A successful call means the import was accepted, not that it worked. Always verify afterwards with search-space-compact, allowing a second or two for the objects to appear.",
			"inputSchema": restSchema([]string{"space_id", "type"}, map[string]any{
				"space_id": spaceIDProp(),
				"type":     enumProp("What is being imported.", anytypefiles.ImportTypeNames()),
				"paths": map[string]any{
					"type":        "array",
					"description": "Files or directories inside the input root. Required for everything except a Notion import.",
					"items":       map[string]any{"type": "string"},
				},
				"notion_api_key":          strProp("Notion integration token. Only for type=notion, which pulls from Notion's API instead of reading files."),
				"update_existing":         map[string]any{"type": "boolean", "description": "Update objects that already exist instead of creating duplicates.", "default": false},
				"no_collection":           map[string]any{"type": "boolean", "description": "Do not gather the imported objects into a new collection.", "default": false},
				"collection_title":        strProp("Name for the collection the import creates."),
				"csv_first_row_is_header": map[string]any{"type": "boolean", "description": "For type=csv: treat the first row as property names.", "default": true},
				"csv_delimiter":           strProp("For type=csv: column separator. Defaults to a comma."),
				"csv_transposed":          map[string]any{"type": "boolean", "description": "For type=csv: rows and columns are swapped.", "default": false},
			}),
		},
		map[string]any{
			"name":        "object-versions",
			"description": "List an object's version history, newest first. This is Anytype's stored history and is independent of object-undo: it survives restarts and reaches back much further than the undo stack.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":          spaceIDProp(),
				"object_id":         strProp("Object whose history to list."),
				"limit":             map[string]any{"type": "integer", "description": "How many versions to return. Defaults to 30.", "default": 30},
				"before_version_id": strProp("Return versions older than this one, for paging further back."),
			}),
		},
		map[string]any{
			"name":        "object-version-show",
			"description": "Read the blocks of an object as they were in one version, without changing anything. Use it to check what a version contains before restoring it.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "version_id"}, map[string]any{
				"space_id":   spaceIDProp(),
				"object_id":  strProp("Object to inspect."),
				"version_id": strProp("Version id from object-versions."),
			}),
		},
		map[string]any{
			"name":        "object-version-restore",
			"description": "Roll an object back to an earlier version. The current state is kept in the history, so a restore can itself be undone by restoring the newer version.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "version_id"}, map[string]any{
				"space_id":   spaceIDProp(),
				"object_id":  strProp("Object to roll back."),
				"version_id": strProp("Version id from object-versions."),
			}),
		},
		map[string]any{
			"name":        "object-duplicate",
			"description": "Duplicate an object with its content and properties, returning the new object id.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object to duplicate."),
			}),
		},
		map[string]any{
			"name":        "template-create",
			"description": "Create a template from an existing object, or copy an existing template with source_template_id. The new template belongs to the source object's type and shows up when creating objects of that type. The REST template tools can only read templates.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id":           spaceIDProp(),
				"object_id":          strProp("Object to turn into a template."),
				"source_template_id": strProp("Existing template to clone instead of using object_id."),
			}),
		},
		map[string]any{
			"name":        "type-set-featured-properties",
			"description": "Choose which properties are shown directly under the title of every object of a type — the featured row in the GUI. This is a type-level setting; Anytype has no per-object equivalent (its per-object RPC accepts only the description property). Pass property IDs from list-properties, not keys. The list replaces the current selection; pass an empty list to clear it.",
			"inputSchema": restSchema([]string{"space_id", "type_id", "property_ids"}, map[string]any{
				"space_id": spaceIDProp(),
				"type_id":  strProp("Type ID (bafyrei...) from list-types-compact. The type key is not accepted."),
				"property_ids": map[string]any{
					"type":        "array",
					"description": "Property IDs (bafyrei...) from list-properties, in the order they should appear.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		map[string]any{
			"name":        "objects-modify-property",
			"description": "Add to, remove from or overwrite one property across many objects in a single call. add and remove work WITHOUT reading first, which makes them the right way to tag or untag a batch: read-modify-write on each object would be slower and could discard a concurrent change. " + relationKeySpellingNote,
			"inputSchema": restSchema([]string{"space_id", "object_ids", "operations"}, map[string]any{
				"space_id": spaceIDProp(),
				"object_ids": map[string]any{
					"type":        "array",
					"description": "Objects to change.",
					"items":       map[string]any{"type": "string"},
				},
				"operations": map[string]any{
					"type":        "array",
					"description": "Changes to apply. Each entry names a property key and AT LEAST ONE of add, remove or set. add and remove may be combined in one entry — add is applied first, then remove, which swaps a tag in a single operation. set overrides both: when set is present, add and remove in the same entry are ignored.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"property_key": strProp("Property key, e.g. \"tag\", from list-properties. " + relationKeySpellingNote),
							"add":          map[string]any{"description": "Value(s) to add to a multi-value property. " + modifyShapeNote},
							"remove":       map[string]any{"description": "Value(s) to remove from a multi-value property. " + modifyShapeNote},
							"set":          map[string]any{"description": "Value to overwrite the property with, replacing whatever is there. Unlike add/remove this works for single-value formats too: a string for text/url/email/phone/select, a number for number, true/false for checkbox, an RFC3339 datetime or YYYY-MM-DD for date, an array for multi_select/objects/files, and null to clear. " + modifyShapeNote},
						},
						"required": []any{"property_key"},
					},
				},
			}),
		},
	}
}

func (s *mcpServer) dispatchTransferTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "object-export":
		res, err := s.toolObjectExport(args)
		return res, err, true
	case "object-export-files":
		res, err := s.toolObjectExportFiles(args)
		return res, err, true
	case "object-import":
		res, err := s.toolObjectImport(args)
		return res, err, true
	case "object-versions":
		res, err := s.toolObjectVersions(args)
		return res, err, true
	case "object-version-show":
		res, err := s.toolObjectVersionShow(args)
		return res, err, true
	case "object-version-restore":
		res, err := s.toolObjectVersionRestore(args)
		return res, err, true
	case "object-duplicate":
		res, err := s.toolObjectDuplicate(args)
		return res, err, true
	case "template-create":
		res, err := s.toolTemplateCreate(args)
		return res, err, true
	case "type-set-featured-properties":
		res, err := s.toolSetFeaturedProperties(args)
		return res, err, true
	case "objects-modify-property":
		res, err := s.toolModifyProperty(args)
		return res, err, true
	}
	return nil, nil, false
}

func (s *mcpServer) toolObjectExport(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	format := optionalString(args, "format")
	if format == "" {
		format = "markdown"
	}
	content, err := client.ExportObject(context.Background(), spaceID, objectID, format)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"format": format, "content": content, "length": len(content),
	}, nil
}

func (s *mcpServer) toolObjectExportFiles(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}

	// The export lands in the shared output root so the host can reach it. The
	// server sees that directory under a different mount point, hence the two
	// paths: one to create locally, one to hand to anytype-heart.
	targetDir := strings.TrimSpace(rawOptionalString(args, "target_dir"))
	if targetDir == "" {
		targetDir = "export"
	}
	// resolveUnderRoot stats the path, so the directory has to exist before it
	// can be validated. Contain the name first, then create, then re-check that
	// what was created really sits under the output root.
	if strings.Contains(targetDir, "..") || filepath.IsAbs(targetDir) {
		return nil, fmt.Errorf("target_dir must be a relative name inside the output root %q", s.cfg.outRoot)
	}
	hostPath := filepath.Join(s.cfg.outRoot, filepath.Clean(targetDir))
	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", hostPath, err)
	}
	hostPath, err = resolveUnderRoot(s.cfg.outRoot, hostPath, true)
	if err != nil {
		return nil, fmt.Errorf("invalid target_dir: %w. output_root=%q", err, s.cfg.outRoot)
	}
	serverPath, err := mapHostPathToServerPath(s.cfg.outRoot, s.cfg.serverOutRoot, hostPath)
	if err != nil {
		return nil, fmt.Errorf("failed to map target_dir for server: %w", err)
	}

	format := optionalString(args, "format")
	if format == "" {
		format = "markdown"
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	written, succeeded, err := client.ExportObjects(context.Background(), spaceID, anytypefiles.ExportOptions{
		Path:             serverPath,
		ObjectIDs:        optionalStringSlice(args, "object_ids"),
		Format:           format,
		Zip:              optionalBool(args, "zip", false),
		IncludeNested:    optionalBool(args, "include_nested", false),
		IncludeFiles:     optionalBool(args, "include_files", false),
		IncludeArchived:  optionalBool(args, "include_archived", false),
		IncludeBacklinks: optionalBool(args, "include_backlinks", false),
		MarkdownSchema:   optionalBool(args, "markdown_schema", false),
	})
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"space_id": spaceID, "format": format,
		"exported_count": succeeded,
		"server_path":    written,
		"output_root":    s.cfg.outRoot,
	}
	if hostWritten, err := mapServerPathToHostPath(s.cfg.serverOutRoot, s.cfg.outRoot, written); err == nil {
		out["path"] = hostWritten
		if entries, err := os.ReadDir(hostWritten); err == nil {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			out["entries"] = names
		}
	}
	return out, nil
}

func (s *mcpServer) toolObjectImport(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	importType, err := requiredString(args, "type")
	if err != nil {
		return nil, err
	}

	// Paths come in relative to the shared input root and have to be translated
	// into the server's view of that directory, exactly as file-upload does.
	var serverPaths []string
	var hostPaths []string
	for i, raw := range optionalStringSlice(args, "paths") {
		resolved, err := resolveUnderRoot(s.cfg.inRoot, raw, true)
		if err != nil {
			suggestions := suggestSimilarInputPaths(s.cfg.inRoot, raw, 8)
			if len(suggestions) > 0 {
				return nil, fmt.Errorf("paths[%d]: %w. input_root=%q. Similar entries: %s", i, err, s.cfg.inRoot, strings.Join(suggestions, ", "))
			}
			return nil, fmt.Errorf("paths[%d]: %w. input_root=%q", i, err, s.cfg.inRoot)
		}
		if _, err := os.Stat(resolved); err != nil {
			return nil, fmt.Errorf("paths[%d]: %s does not exist under the input root", i, raw)
		}
		mapped, err := mapHostPathToServerPath(s.cfg.inRoot, s.cfg.serverInRoot, resolved)
		if err != nil {
			return nil, fmt.Errorf("paths[%d]: failed to map for server: %w", i, err)
		}
		hostPaths = append(hostPaths, resolved)
		serverPaths = append(serverPaths, mapped)
	}

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	result, err := client.ImportObjects(context.Background(), spaceID, anytypefiles.ImportOptions{
		Type:                  importType,
		Paths:                 serverPaths,
		NotionAPIKey:          optionalString(args, "notion_api_key"),
		UpdateExistingObjects: optionalBool(args, "update_existing", false),
		NoCollection:          optionalBool(args, "no_collection", false),
		CollectionTitle:       optionalString(args, "collection_title"),
		CsvUseFirstRowAsNames: optionalBool(args, "csv_first_row_is_header", true),
		CsvDelimiter:          optionalString(args, "csv_delimiter"),
		CsvTransposed:         optionalBool(args, "csv_transposed", false),
	})
	if err != nil {
		return nil, err
	}

	// Anytype reports neither a count nor an error for an import, so saying
	// anything about the outcome here would be an invention. Report only what
	// is actually known: that the request was accepted.
	out := map[string]any{
		"space_id": spaceID, "type": importType,
		"started": true,
		"note":    "Anytype imports in the background and reports no result, not even failures. Verify with search-space-compact after a moment; do not assume this worked.",
	}
	if result.ObjectsCount > 0 {
		out["objects_count"] = result.ObjectsCount
	}
	if result.CollectionID != "" {
		out["collection_id"] = result.CollectionID
	}
	if len(hostPaths) > 0 {
		out["paths"] = hostPaths
	}
	return out, nil
}

func (s *mcpServer) toolObjectVersions(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	versions, err := client.ListVersions(context.Background(), objectID,
		optionalString(args, "before_version_id"), int32(optionalInt(args, "limit", 30)))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		entry := map[string]any{"id": v.ID}
		if v.Time != "" {
			entry["time"] = v.Time
		}
		if v.AuthorName != "" {
			entry["author"] = v.AuthorName
		}
		out = append(out, entry)
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"versions": out, "count": len(out),
	}, nil
}

func (s *mcpServer) toolObjectVersionShow(args map[string]any) (map[string]any, error) {
	versionID, err := requiredString(args, "version_id")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	blocks, err := client.ShowVersion(context.Background(), objectID, versionID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		entry := map[string]any{"id": b.ID, "kind": b.Kind}
		if b.Style != "" {
			entry["style"] = b.Style
		}
		if b.Text != "" {
			entry["text"] = b.Text
		}
		out = append(out, entry)
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "version_id": versionID,
		"blocks": out, "count": len(out),
	}, nil
}

func (s *mcpServer) toolObjectVersionRestore(args map[string]any) (map[string]any, error) {
	versionID, err := requiredString(args, "version_id")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.RestoreVersion(context.Background(), objectID, versionID); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"version_id": versionID, "restored": true,
	}, nil
}

func (s *mcpServer) toolObjectDuplicate(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	newID, err := client.DuplicateObject(context.Background(), objectID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "source_object_id": objectID,
		"object_id": newID, "duplicated": true,
	}, nil
}

func (s *mcpServer) toolTemplateCreate(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectID := optionalString(args, "object_id")
	sourceTemplate := optionalString(args, "source_template_id")
	if objectID == "" && sourceTemplate == "" {
		return nil, fmt.Errorf("pass object_id to make a template from an object, or source_template_id to clone one")
	}
	if objectID != "" && sourceTemplate != "" {
		return nil, fmt.Errorf("pass either object_id or source_template_id, not both")
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	ctx := context.Background()
	var templateID string
	if sourceTemplate != "" {
		templateID, err = client.CloneTemplate(ctx, spaceID, sourceTemplate)
	} else {
		templateID, err = client.CreateTemplateFromObject(ctx, objectID)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "template_id": templateID, "created": true,
	}
	if sourceTemplate != "" {
		out["cloned_from"] = sourceTemplate
	} else {
		out["source_object_id"] = objectID
	}
	return out, nil
}

func (s *mcpServer) toolSetFeaturedProperties(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}
	// An empty list is meaningful here: it clears the featured row.
	if _, ok := args["property_ids"]; !ok {
		return nil, fmt.Errorf("property_ids is required; pass an empty array to clear the featured properties")
	}
	propertyIDs := optionalStringSlice(args, "property_ids")

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetTypeFeaturedProperties(context.Background(), typeID, propertyIDs); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "type_id": typeID,
		"property_ids": propertyIDs, "updated": true,
	}, nil
}

func (s *mcpServer) toolModifyProperty(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectIDs := optionalStringSlice(args, "object_ids")
	if len(objectIDs) == 0 {
		return nil, fmt.Errorf("object_ids is required")
	}
	rawOps, ok := args["operations"]
	if !ok || rawOps == nil {
		return nil, fmt.Errorf("operations is required")
	}
	items, err := asObjectSlice(rawOps)
	if err != nil {
		return nil, fmt.Errorf("operations: %w", err)
	}
	ops := make([]anytypefiles.DetailOperation, 0, len(items))
	for i, item := range items {
		key, err := requiredString(item, "property_key")
		if err != nil {
			return nil, fmt.Errorf("operations[%d]: %w", i, err)
		}
		op := anytypefiles.DetailOperation{PropertyKey: key}
		// Presence matters, not truthiness: removing a value or setting a
		// property to false are both legitimate and both look "empty".
		op.Add, op.HasAdd = item["add"]
		op.Remove, op.HasRemove = item["remove"]
		op.Set, op.HasSet = item["set"]
		ops = append(ops, op)
	}

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.ModifyDetailValues(context.Background(), spaceID, objectIDs, ops); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_ids": objectIDs,
		"operation_count": len(ops), "object_count": len(objectIDs), "updated": true,
	}, nil
}
