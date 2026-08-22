package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// Space information, inline dataviews, version diffing, date objects, schema
// ordering and copying blocks back out as markdown.

func (s *mcpServer) spaceOpsToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "space-file-usage",
			"description": "Report how much storage a space uses: number of files, bytes used, and the remaining allowance. Useful before a large import or upload, and on a self-hosted instance to see what the node is carrying.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
			}),
		},
		map[string]any{
			"name": "space-set-homepage",
			"description": "Choose what a space opens on — the GUI's homepage setting. Point it at a dashboard page or a collection so the space starts somewhere useful, or at one of the built-in screens. " +
				"A newly created space is set to widgets, so \"no homepage\" usually means that constant rather than nothing. Read the current setting back with space-get-homepage.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object to show when the space is opened, or one of the built-in screens: " + strings.Join(anytypefiles.HomepageConstants(), ", ") + ". Anytype rejects an object id that does not exist in the space."),
			}),
		},
		map[string]any{
			"name": "space-get-homepage",
			"description": "Report what a space opens on, the counterpart of space-set-homepage. The setting is a hidden relation on the space that get-space does not carry, so this is the only way to read it. " +
				"value is what is stored; it is an object id when object_id is filled in, and one of the built-in screens (" + strings.Join(anytypefiles.HomepageConstants(), ", ") + ") when constant is. A new space starts on widgets, so a constant is the normal answer, not a missing setting.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
			}),
		},
		map[string]any{
			"name":        "get-type-layout",
			"description": "Read the layout switches of a type — full width, header position and how the properties under the title are laid out — the counterpart of type-set-layout. These are hidden relations that no listing shows, so use this before changing one to see what you would be replacing.",
			"inputSchema": restSchema([]string{"space_id", "type_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"type_id":  strProp("Type id from list-types-compact (the bafyrei... id, not the type key)."),
			}),
		},
		map[string]any{
			"name":        "object-flags",
			"description": "Report whether objects are favorited and whether they are in the bin — the counterpart of object-set-favorite and object-set-archived. Both are hidden relations that the REST object payload does not carry, and isArchived is not filterable there either, so this is the only way to check a flag before or after setting it. found=false means no object with that id exists in the space, which is not the same as a flag being off.",
			"inputSchema": restSchema([]string{"space_id", "object_ids"}, map[string]any{
				"space_id": spaceIDProp(),
				"object_ids": map[string]any{
					"type":        "array",
					"description": "Objects to report on. Answered in the order given.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		map[string]any{
			"name": "block-embed-query",
			"description": "Embed an existing query or collection INSIDE a page, as the GUI's inline set does: the page then shows that query's rows in place. To build a standalone query use query-create instead.\n\n" +
				"Half reference, half copy — know which half is which. WHICH OBJECTS appear keeps following the query: change the query's source and the page follows. The PRESENTATION — views, layout, filters, sorts, columns — is copied at the moment of embedding and then belongs to the page. Later edits to the query do NOT reach the embed, and edits to the embed do not reach the query. Pass block_id to re-copy the query's current configuration over an existing embed and bring the two back in line.\n\n" +
				"The source query's ACTIVE VIEW is not part of that copy — neither when embedding nor when refreshing. A new or refreshed embed keeps its own active-view state and falls back to the first view. Set it on the embed with query-view-arrange (set_active=true) against the page.\n\n" +
				"To reconfigure the embed itself rather than the query, use the query-* tools against the PAGE, passing this page's object_id together with the block_id returned here — query-inspect on the page reports it under dataview_blocks.\n\n" +
				"Side by side instead of stacked: embed both queries, then move one next to the other with block-move using position=left or right, which puts them into page columns.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "query_object_id"}, map[string]any{
				"space_id":        spaceIDProp(),
				"object_id":       strProp("Page to embed into."),
				"query_object_id": strProp("Query or collection object to display."),
				"target_id":       strProp("Block id to insert relative to. Omit to append at the end."),
				"position":        enumProp("Where to insert relative to target_id. Defaults to bottom. Use right (or left) with target_id set to another embed to put two queries side by side in page columns.", anytypefiles.BlockPositionNames()),
				"block_id":        strProp("Existing embed block to refresh from the query, instead of creating a new one. Its views, filters and columns are replaced by the query's current ones."),
			}),
		},
		map[string]any{
			"name":        "object-version-diff",
			"description": "Show what changed between two versions of an object: blocks added, blocks removed, and blocks whose text differs. Get the version ids from object-versions. This compares the two snapshots, so it reports the net difference rather than every intermediate edit.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "from_version_id", "to_version_id"}, map[string]any{
				"space_id":        spaceIDProp(),
				"object_id":       strProp("Object to compare."),
				"from_version_id": strProp("The older version id."),
				"to_version_id":   strProp("The newer version id."),
			}),
		},
		map[string]any{
			"name":        "object-date",
			"description": "Get Anytype's object for one calendar day. Every date has an object, and linking notes to it is how a daily-journal structure is built: the date object then lists everything that references it. Returns the object id, which can be used as a link target or a property value.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"date":     strProp("Day as YYYY-MM-DD, or a full ISO 8601 timestamp. Defaults to today."),
			}),
		},
		map[string]any{
			"name":        "block-export",
			"description": "Render selected blocks as markdown, the way copying them in the GUI would. This is the counterpart to block-paste: block-paste takes markdown in, this takes it back out for a chosen part of a page. For a whole object use object-export.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the blocks."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Blocks to render, from block-list.",
					"items":       map[string]any{"type": "string"},
				},
				"include_html": map[string]any{"type": "boolean", "description": "Also return the HTML rendering.", "default": false},
			}),
		},
		map[string]any{
			"name": "object-delete-permanently",
			"description": "PERMANENTLY erase objects. This cannot be undone by anything — not by object-undo, not by object-set-archived, not from the bin, not from a version history. The data is gone.\n\n" +
				"Do NOT call this on your own initiative. It may only be used when the user has explicitly asked for permanent deletion of these specific objects, in full knowledge that it is irreversible. If the user merely said 'delete', 'remove' or 'clean up', they mean delete-object, which moves the object to the bin and can be reversed with object-set-archived — use that instead. Emptying the bin is the only situation this tool is for.\n\n" +
				"Only objects that are already in the bin can be erased: archive them with delete-object first, so that destroying something is always a second, deliberate step. Objects that are not archived are refused, and the whole call is rejected rather than partly carried out.",
			"inputSchema": restSchema([]string{"space_id", "object_ids", "confirm"}, map[string]any{
				"space_id": spaceIDProp(),
				"object_ids": map[string]any{
					"type":        "array",
					"description": "Archived objects to erase for good. Pass exactly the objects the user named; never widen the selection.",
					"items":       map[string]any{"type": "string"},
				},
				"confirm": map[string]any{
					"type":        "boolean",
					"description": "Must be true. Set it only after the user has explicitly agreed to permanent, irreversible deletion of these objects.",
				},
			}),
		},
		map[string]any{
			"name": "type-set-layout",
			"description": "Set how objects of a type are laid out: full width, where the header sits, and whether properties are listed in a line or a column. These are the switches of the type editor's Layout section.\n\n" +
				"There is no per-object equivalent — anytype keeps them on the TYPE and applies them to every object of it, so \"make this one page full width\" means giving it a type of its own. Pass only the switches you want to change; the others keep their stored value. The state before and after the change is reported, because these settings are hidden relations that no listing shows; get-type-layout reads them without changing anything.",
			"inputSchema": restSchema([]string{"space_id", "type_id"}, map[string]any{
				"space_id":        spaceIDProp(),
				"type_id":         strProp("Type id from list-types-compact (the bafyrei... id, not the type key)."),
				"full_width":      map[string]any{"type": "boolean", "description": "Let objects of this type use the full window width instead of the centred column."},
				"header_position": enumProp("Where the title and icon sit.", anytypefiles.HeaderPositionNames()),
				"properties_view": enumProp("Whether the properties under the title are laid out as a line or a list.", anytypefiles.PropertiesViewNames()),
			}),
		},
		map[string]any{
			"name":        "schema-set-order",
			"description": "Fix the order things appear in: either the object types of a space, or the options of one select/multi-select property. Pass the ids in the order you want them.",
			"inputSchema": restSchema([]string{"space_id", "kind", "ids"}, map[string]any{
				"space_id": spaceIDProp(),
				"kind":     enumProp("What to order: the space's types, or the tag options of one property.", []string{"types", "tags"}),
				"ids": map[string]any{
					"type":        "array",
					"description": "For kind=types: type ids from list-types-compact. For kind=tags: tag ids from list-tags.",
					"items":       map[string]any{"type": "string"},
				},
				"property_key": strProp("For kind=tags: the property whose options are being ordered, e.g. \"tag\". " + relationKeySpellingNote),
			}),
		},
	}
}

func (s *mcpServer) dispatchSpaceOpsTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "space-file-usage":
		res, err := s.toolFileUsage(args)
		return res, err, true
	case "space-set-homepage":
		res, err := s.toolSetHomepage(args)
		return res, err, true
	case "space-get-homepage":
		res, err := s.toolGetHomepage(args)
		return res, err, true
	case "get-type-layout":
		res, err := s.toolGetTypeLayout(args)
		return res, err, true
	case "object-flags":
		res, err := s.toolObjectFlags(args)
		return res, err, true
	case "block-embed-query":
		res, err := s.toolEmbedQuery(args)
		return res, err, true
	case "object-version-diff":
		res, err := s.toolVersionDiff(args)
		return res, err, true
	case "object-date":
		res, err := s.toolObjectDate(args)
		return res, err, true
	case "block-export":
		res, err := s.toolBlockExport(args)
		return res, err, true
	case "type-set-layout":
		res, err := s.toolTypeSetLayout(args)
		return res, err, true
	case "schema-set-order":
		res, err := s.toolSchemaSetOrder(args)
		return res, err, true
	case "object-delete-permanently":
		res, err := s.toolDeletePermanently(args)
		return res, err, true
	}
	return nil, nil, false
}

// humanBytes renders a byte count the way a person reads it, so the model does
// not have to do arithmetic to answer "how full is it".
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (s *mcpServer) toolFileUsage(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	usage, err := client.SpaceFileUsage(context.Background(), spaceID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id":         spaceID,
		"files_count":      usage.FilesCount,
		"bytes_used":       usage.BytesUsage,
		"bytes_left":       usage.BytesLeft,
		"bytes_limit":      usage.BytesLimit,
		"local_bytes_used": usage.LocalBytesUsage,
		"used":             humanBytes(usage.BytesUsage),
		"left":             humanBytes(usage.BytesLeft),
	}
	if usage.BytesLimit > 0 {
		out["limit"] = humanBytes(usage.BytesLimit)
		out["percent_used"] = float64(usage.BytesUsage) * 100 / float64(usage.BytesLimit)
	}
	return out, nil
}

func (s *mcpServer) toolSetHomepage(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetSpaceHomepage(context.Background(), spaceID, objectID); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "updated": true,
	}, nil
}

func (s *mcpServer) toolEmbedQuery(args map[string]any) (map[string]any, error) {
	queryID, err := requiredString(args, "query_object_id")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	existing := optionalString(args, "block_id")
	blockID, err := client.EmbedQueryBlock(context.Background(), objectID,
		optionalString(args, "target_id"), optionalString(args, "position"), queryID, existing)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": blockID, "query_object_id": queryID,
	}
	if existing != "" {
		out["resynced"] = true
		out["note"] = "the embed now shows the query's current views, filters and columns; anything configured on the embed alone has been overwritten"
	} else {
		out["created"] = true
		out["note"] = "which objects appear follows the query; the views, filters and columns are a copy taken now. Call again with this block_id to re-copy them later"
	}
	return out, nil
}

func (s *mcpServer) toolGetHomepage(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	homepage, err := client.ReadSpaceHomepage(context.Background(), spaceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "value": homepage.Value,
		"object_id": homepage.ObjectID, "constant": homepage.Constant,
		"name": homepage.Name, "is_set": homepage.Value != "",
	}, nil
}

func (s *mcpServer) toolGetTypeLayout(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	layout, err := client.ReadTypeLayout(context.Background(), spaceID, typeID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "type_id": typeID,
		"full_width":      layout.FullWidth,
		"header_position": layout.HeaderPosition,
		"properties_view": layout.PropertiesView,
	}, nil
}

func (s *mcpServer) toolObjectFlags(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectIDs := optionalStringSlice(args, "object_ids")
	if len(objectIDs) == 0 {
		return nil, fmt.Errorf("object_ids is required")
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	flags, err := client.ReadObjectFlags(context.Background(), spaceID, objectIDs)
	if err != nil {
		return nil, err
	}
	objects := make([]map[string]any, 0, len(flags))
	for _, f := range flags {
		objects = append(objects, map[string]any{
			"object_id": f.ObjectID, "name": f.Name,
			"favorite": f.Favorite, "archived": f.Archived, "found": f.Found,
		})
	}
	return map[string]any{
		"space_id": spaceID, "count": len(objects), "objects": objects,
	}, nil
}

func (s *mcpServer) toolTypeSetLayout(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}
	spec := anytypefiles.TypeLayoutSpec{
		HeaderPosition: optionalString(args, "header_position"),
		PropertiesView: optionalString(args, "properties_view"),
	}
	if raw, ok := args["full_width"]; ok && raw != nil {
		if value, ok := raw.(bool); ok {
			spec.FullWidth = &value
		} else {
			return nil, fmt.Errorf("full_width must be a boolean")
		}
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	before, after, err := client.SetTypeLayout(context.Background(), spaceID, typeID, spec)
	if err != nil {
		return nil, err
	}
	state := func(l anytypefiles.TypeLayout) map[string]any {
		return map[string]any{
			"full_width": l.FullWidth, "header_position": l.HeaderPosition,
			"properties_view": l.PropertiesView,
		}
	}
	return map[string]any{
		"space_id": spaceID, "type_id": typeID, "updated": true,
		"before": state(before), "after": state(after),
		"note": "this applies to every object of the type; anytype has no per-object version of these settings",
	}, nil
}

func (s *mcpServer) toolVersionDiff(args map[string]any) (map[string]any, error) {
	fromID, err := requiredString(args, "from_version_id")
	if err != nil {
		return nil, err
	}
	toID, err := requiredString(args, "to_version_id")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	diff, err := client.DiffVersions(context.Background(), objectID, fromID, toID)
	if err != nil {
		return nil, err
	}
	brief := func(blocks []anytypefiles.BlockInfo) []map[string]any {
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
		return out
	}
	changed := make([]map[string]any, 0, len(diff.Changed))
	for _, pair := range diff.Changed {
		changed = append(changed, map[string]any{
			"id": pair.ID, "before": pair.Before, "after": pair.After,
		})
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"from_version_id": fromID, "to_version_id": toID,
		"added": brief(diff.Added), "removed": brief(diff.Removed), "changed": changed,
		"added_count": len(diff.Added), "removed_count": len(diff.Removed),
		"changed_count": len(changed),
		"unchanged":     len(diff.Added) == 0 && len(diff.Removed) == 0 && len(changed) == 0,
	}, nil
}

func (s *mcpServer) toolObjectDate(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	timestamp, err := anytypefiles.ParseDate(optionalString(args, "date"))
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	objectID, name, err := client.DateObject(context.Background(), spaceID, timestamp)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID, "timestamp": timestamp,
	}
	if name != "" {
		out["name"] = name
	}
	return out, nil
}

func (s *mcpServer) toolBlockExport(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	markdown, html, err := client.CopyBlocksAsMarkdown(context.Background(), spaceID, objectID, blockIDs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_ids": blockIDs, "markdown": markdown, "length": len(markdown),
	}
	if optionalBool(args, "include_html", false) {
		out["html"] = html
	}
	return out, nil
}

func (s *mcpServer) toolSchemaSetOrder(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	kind, err := requiredString(args, "kind")
	if err != nil {
		return nil, err
	}
	ids := optionalStringSlice(args, "ids")
	if len(ids) == 0 {
		return nil, fmt.Errorf("ids is required; pass them in the order you want them")
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	ctx := context.Background()
	switch kind {
	case "types":
		if err := client.SetTypeOrder(ctx, spaceID, ids); err != nil {
			return nil, err
		}
	case "tags":
		propertyKey, err := requiredString(args, "property_key")
		if err != nil {
			return nil, fmt.Errorf("kind=tags needs property_key: %w", err)
		}
		if err := client.SetTagOrder(ctx, spaceID, propertyKey, ids); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown kind %q; use types or tags", kind)
	}
	return map[string]any{
		"space_id": spaceID, "kind": kind, "ids": ids, "updated": true,
	}, nil
}

func (s *mcpServer) toolDeletePermanently(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectIDs := optionalStringSlice(args, "object_ids")
	if len(objectIDs) == 0 {
		return nil, fmt.Errorf("object_ids is required")
	}
	// Guard one: an explicit act, so this cannot be reached by a tool call that
	// merely looks plausible.
	confirmed, _ := args["confirm"].(bool)
	if !confirmed {
		return nil, fmt.Errorf(
			"refusing to erase %d object(s): confirm must be true, and may only be set once the user has "+
				"explicitly agreed to permanent, irreversible deletion. If they only asked to delete or clean up, "+
				"use delete-object instead — that archives and can be undone with object-set-archived",
			len(objectIDs))
	}

	// Guard two: only the bin can be emptied. Checking every object first means
	// a mistaken id cannot destroy a live object, and the call either applies to
	// everything named or to nothing.
	var live, missing []string
	for _, id := range objectIDs {
		payload, err := s.anytypeAPIRequest(http.MethodGet,
			"/v1/spaces/"+url.PathEscape(spaceID)+"/objects/"+url.PathEscape(id), nil, nil)
		if err != nil {
			missing = append(missing, id)
			continue
		}
		object, err := objectFromPayload(payload, "object")
		if err != nil {
			missing = append(missing, id)
			continue
		}
		if archived, _ := object["archived"].(bool); !archived {
			live = append(live, id)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("refusing to erase anything: %d of the given ids could not be read (%s). "+
			"Check the ids before retrying", len(missing), strings.Join(missing, ", "))
	}
	if len(live) > 0 {
		return nil, fmt.Errorf("refusing to erase anything: %d object(s) are not in the bin (%s). "+
			"Archive them with delete-object first, so that erasing is a separate, deliberate step",
			len(live), strings.Join(live, ", "))
	}

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.DeleteObjectsPermanently(context.Background(), objectIDs); err != nil {
		return nil, err
	}

	// heart reports success as soon as ONE id succeeded, so the outcome is
	// verified rather than assumed. The REST index trails writes by about a
	// second, so give it that before reading back, or a successful deletion
	// looks like a failure.
	//
	// The check is "has it left the bin", not "can it still be fetched by id".
	// An erased property, tag, type or template disappears from every listing
	// but keeps answering a direct GET with a tombstone — verified against a
	// property that was gone from list-properties, from the bin and from search
	// while still readable by id. Probing by id therefore reported deletions
	// that had in fact succeeded as failures. Every id was confirmed to be in
	// the bin above, so anything still there was genuinely not erased.
	time.Sleep(1500 * time.Millisecond)
	stillBinned, binErr := client.ListArchived(context.Background(), spaceID, nil)
	if binErr != nil {
		return nil, fmt.Errorf("erase was requested but the result could not be verified: %w", binErr)
	}
	inBin := make(map[string]bool, len(stillBinned))
	for _, entry := range stillBinned {
		inBin[entry.ObjectID] = true
	}
	var survivors []string
	for _, id := range objectIDs {
		if inBin[id] {
			survivors = append(survivors, id)
		}
	}
	out := map[string]any{
		"space_id": spaceID, "requested": objectIDs,
		"deleted_count": len(objectIDs) - len(survivors),
		"deleted":       len(survivors) == 0,
	}
	if len(survivors) > 0 {
		out["still_present"] = survivors
		out["warning"] = "Anytype reports success once any object is erased; these are still in the bin and were not deleted"
	}
	return out, nil
}
