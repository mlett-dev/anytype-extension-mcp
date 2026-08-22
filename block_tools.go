package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// Block-level editing tools, backed by the anytype-heart gRPC API.
//
// The REST tools treat a body as one markdown string: update-object-compact
// with markdown rewrites the whole body and loses block ids, checkbox state and
// inline marks. These tools edit individual blocks the way the GUI does.
//
// Every editing tool needs block ids, which come from block-list.

const blockMarkRangeDescription = "Character range the mark covers, counted in the block's plain text. from is inclusive, to is exclusive."

func markSpecSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"from":  map[string]any{"type": "integer", "description": "Start offset, inclusive."},
			"to":    map[string]any{"type": "integer", "description": "End offset, exclusive."},
			"type":  enumProp("Mark type.", anytypefiles.MarkTypeNames()),
			"param": strProp("Extra value: URL for link, object id for object/mention, colour name for text_color/background_color, emoji for emoji."),
		},
		"required": []any{"from", "to", "type"},
	}
}

func (s *mcpServer) blockToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "block-list",
			"description": "Read the block structure of an object: every block with its id, kind, style, text, checkbox state, inline marks, callout emoji and children, plus card_style, icon_size, description and property_keys for link blocks, property_key for relation blocks, target_object_id for embedded queries, and width for page columns. Call this before any block edit — all other block tools need the ids from here. Unlike get-object-compact with markdown, this preserves block identity and checkbox state.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object whose blocks to read."),
				"text_only": map[string]any{"type": "boolean", "description": "Return only text blocks, skipping layout and structural blocks.", "default": false},
			}),
		},
		map[string]any{
			"name":        "block-create",
			"description": "Insert a block into an object. kind=text covers paragraphs, headings, quotes, code, checkboxes, bullet and numbered lists, toggles and callouts; the other kinds insert a link to another object, a web bookmark, a divider or a table. Appends to the end unless target_id and position say otherwise. position=left or right puts the new block into a page column beside target_id instead of above or below it.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":      spaceIDProp(),
				"object_id":     strProp("Object to insert into."),
				"kind":          enumProp("What to insert. Defaults to text.", []string{"text", "link", "bookmark", "divider", "table"}),
				"target_id":     strProp("Block id to insert relative to. Omit to append at the end of the object."),
				"position":      enumProp("Where to insert relative to target_id. Defaults to bottom. left and right put the new block into a page column beside target_id, side by side instead of stacked.", anytypefiles.BlockPositionNames()),
				"style":         enumProp("For kind=text: the block style. Defaults to paragraph. Divider styles do NOT belong here — this enum holds text styles only; use divider_style.", anytypefiles.TextStyleNames()),
				"divider_style": enumProp("For kind=divider: a horizontal line or a row of dots. Defaults to line.", anytypefiles.DividerStyleNames()),
				"text":          strProp("For kind=text: the block text."),
				"checked":       map[string]any{"type": "boolean", "description": "For style=checkbox: whether it starts ticked.", "default": false},
				"marks": map[string]any{
					"type":        "array",
					"description": "For kind=text: inline marks. " + blockMarkRangeDescription,
					"items":       markSpecSchema(),
				},
				"linked_object_id": strProp("For kind=link: the object to link to."),
				"url":              strProp("For kind=bookmark: the URL to bookmark and fetch a preview for."),
				"rows":             map[string]any{"type": "integer", "description": "For kind=table: number of rows."},
				"columns":          map[string]any{"type": "integer", "description": "For kind=table: number of columns."},
				"with_header_row":  map[string]any{"type": "boolean", "description": "For kind=table: make the first row a header.", "default": true},
			}),
		},
		map[string]any{
			"name":        "block-set-text",
			"description": "Replace the text of one text block, optionally with inline marks. The marks you pass REPLACE the block's current marks, so include the ones you want to keep. Offsets count characters in the plain text you are setting.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_id", "text"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the block."),
				"block_id":  strProp("Block id from block-list."),
				"text":      strProp("New plain text of the block."),
				"marks": map[string]any{
					"type":        "array",
					"description": "Inline marks to apply. " + blockMarkRangeDescription,
					"items":       markSpecSchema(),
				},
			}),
		},
		map[string]any{
			"name":        "block-turn-into",
			"description": "Change the style of text blocks, e.g. turn paragraphs into a heading or a checklist. Text and marks are kept.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids", "style"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the blocks."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Block ids to convert.",
					"items":       map[string]any{"type": "string"},
				},
				"style": enumProp("Target style.", anytypefiles.TextStyleNames()),
			}),
		},
		map[string]any{
			"name":        "block-set-checked",
			"description": "Tick or untick a checkbox block. This is the block-level equivalent of clicking the checkbox in the GUI; rewriting the markdown body would lose the block id.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_id", "checked"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the block."),
				"block_id":  strProp("Checkbox block id from block-list."),
				"checked":   map[string]any{"type": "boolean", "description": "New checkbox state."},
			}),
		},
		map[string]any{
			"name":        "block-mark",
			"description": "Apply one inline mark to a character range in one or more blocks, keeping the existing text. Use this for bold, italic, links and mentions without restating the whole text.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids", "from", "to", "type"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the blocks."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Block ids to mark.",
					"items":       map[string]any{"type": "string"},
				},
				"from":  map[string]any{"type": "integer", "description": "Start offset, inclusive."},
				"to":    map[string]any{"type": "integer", "description": "End offset, exclusive."},
				"type":  enumProp("Mark type.", anytypefiles.MarkTypeNames()),
				"param": strProp("URL for link, object id for object/mention, colour name for colour marks, emoji for emoji."),
			}),
		},
		map[string]any{
			"name":        "block-move",
			"description": "Move blocks within an object, or into a different object. Use position=inner to nest them under the drop target. Within one object the blocks keep their ids; moving into ANOTHER object re-creates them there under new ids, which are returned as moved_block_ids — the ids you passed in are dead after that.\n\nposition=left or right is how a page gets COLUMNS: anytype wraps the drop target and the moved block into a row and puts each into its own column, the same structure dragging a block beside another one produces. Repeat it to add further columns. This is the way to lay two embedded queries out side by side.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids", "drop_target_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object the blocks currently live in."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Block ids to move.",
					"items":       map[string]any{"type": "string"},
				},
				"drop_target_id":   strProp("Block id to drop next to, inside the target object."),
				"position":         enumProp("Where to place them relative to drop_target_id. Defaults to bottom. left and right put the blocks into a page column beside the target, which is how a page is split into columns.", anytypefiles.BlockPositionNames()),
				"target_object_id": strProp("Object to move into. Omit to move within the same object."),
			}),
		},
		map[string]any{
			"name":        "block-duplicate",
			"description": "Duplicate blocks next to a target block. The ids of the copies are returned as new_block_ids.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the blocks."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Block ids to duplicate.",
					"items":       map[string]any{"type": "string"},
				},
				"target_id": strProp("Block to place the copies next to. Defaults to the last duplicated block."),
				"position":  enumProp("Where to place the copies. Defaults to bottom. left and right place them in a page column beside the target.", anytypefiles.BlockPositionNames()),
			}),
		},
		map[string]any{
			"name":        "block-delete",
			"description": "Delete blocks from an object. Unlike delete-object this is not an archive: the blocks are gone. Deleting a block also deletes its children.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the blocks."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Block ids to delete.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		map[string]any{
			"name":        "block-paste",
			"description": "Insert a whole document fragment in ONE call by pasting markdown. Anytype parses it, so headings, bullet and numbered lists, checkboxes, quotes, code fences, links and tables all become real blocks. Prefer this over many block-create calls whenever you are writing more than a couple of blocks. Appends to the end of the object unless target_block_id or replace_block_ids say otherwise.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":        spaceIDProp(),
				"object_id":       strProp("Object to paste into."),
				"markdown":        strProp("Markdown to insert. This is the normal input."),
				"html":            strProp("HTML to insert instead of markdown, for content copied from a web page."),
				"target_block_id": strProp("Paste at this block. Note this behaves like a text cursor, not an anchor: the content MERGES into that block rather than being inserted after it. Omit it to append at the end of the object, which is usually what you want. Targeting a code block or a table cell inserts the text verbatim, without markdown parsing. The title and description blocks are rejected, because pasting there rewrites the object name instead of adding blocks."),
				"replace_block_ids": map[string]any{
					"type":        "array",
					"description": "Blocks to replace with the pasted content. DESTRUCTIVE: these blocks are removed. Omit to insert without deleting anything.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		map[string]any{
			"name":        "block-split",
			"description": "Split one text block in two at a character offset. The text from the offset onwards moves into a new block, whose id is returned. Use it to break a long paragraph apart without retyping either half.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_id", "at"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the block."),
				"block_id":  strProp("Text block to split."),
				"at":        map[string]any{"type": "integer", "description": "Character offset to split at, counted in the block's plain text. Everything from here on moves to the new block."},
				"style":     enumProp("Style of the new block. Defaults to the same style as the original.", anytypefiles.TextStyleNames()),
				"mode":      enumProp("Where the new block goes: bottom (after, the default), top (before) or inner (as a child).", anytypefiles.SplitModeNames()),
			}),
		},
		map[string]any{
			"name":        "block-merge",
			"description": "Join two text blocks into one: the second block's text is appended to the first and the second block disappears. This is what pressing Backspace at the start of a block does in the GUI.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "first_block_id", "second_block_id"}, map[string]any{
				"space_id":        spaceIDProp(),
				"object_id":       strProp("Object containing the blocks."),
				"first_block_id":  strProp("Block that keeps its id and receives the text."),
				"second_block_id": strProp("Block that is appended and then removed."),
			}),
		},
		map[string]any{
			"name":        "block-style",
			"description": "Change how blocks look: text and background colour, horizontal and vertical alignment, the emoji on a callout, or the style of a divider. Pass at least one of them.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the blocks."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Block ids to style.",
					"items":       map[string]any{"type": "string"},
				},
				"text_color":       enumProp("Text colour.", tagColors),
				"background_color": enumProp("Background colour.", tagColors),
				"align":            enumProp("Text alignment.", []string{"left", "center", "right", "justify"}),
				"vertical_align":   enumProp("Vertical alignment, for table cells and callouts.", anytypefiles.VerticalAlignNames()),
				"icon_emoji":       strProp("Emoji shown on a callout block, e.g. \"💡\"."),
				"divider_style":    enumProp("For divider blocks: a line or a row of dots.", anytypefiles.DividerStyleNames()),
			}),
		},
		map[string]any{
			"name": "block-column-width",
			"description": "Set how wide the columns of one page row are — the counterpart to creating them, which is block-move (or block-create) with position=left or right.\n\n" +
				"Pass any block that sits in the row: the row itself, one of its columns, or the content inside a column. The widths are shares and are normalised, so [2, 1] and [0.667, 0.333] both give a two-thirds/one-third split. Pass zeroes to go back to equal columns. Read the result back with block-list, which reports width on every column block.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_id", "widths"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object holding the row."),
				"block_id":  strProp("Any block in the row: the row, a column, or a block inside a column."),
				"widths": map[string]any{
					"type":        "array",
					"description": "One share per column, left to right. Must have exactly as many entries as the row has columns.",
					"items":       map[string]any{"type": "number"},
				},
			}),
		},
	}
}

func (s *mcpServer) dispatchBlockTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "block-list":
		res, err := s.toolBlockList(args)
		return res, err, true
	case "block-create":
		res, err := s.toolBlockCreate(args)
		return res, err, true
	case "block-set-text":
		res, err := s.toolBlockSetText(args)
		return res, err, true
	case "block-turn-into":
		res, err := s.toolBlockTurnInto(args)
		return res, err, true
	case "block-set-checked":
		res, err := s.toolBlockSetChecked(args)
		return res, err, true
	case "block-mark":
		res, err := s.toolBlockMark(args)
		return res, err, true
	case "block-move":
		res, err := s.toolBlockMove(args)
		return res, err, true
	case "block-duplicate":
		res, err := s.toolBlockDuplicate(args)
		return res, err, true
	case "block-delete":
		res, err := s.toolBlockDelete(args)
		return res, err, true
	case "block-style":
		res, err := s.toolBlockStyle(args)
		return res, err, true
	case "block-column-width":
		res, err := s.toolBlockColumnWidth(args)
		return res, err, true
	case "block-paste":
		res, err := s.toolBlockPaste(args)
		return res, err, true
	case "block-split":
		res, err := s.toolBlockSplit(args)
		return res, err, true
	case "block-merge":
		res, err := s.toolBlockMerge(args)
		return res, err, true
	}
	return nil, nil, false
}

func marksFromArgs(args map[string]any, key string) ([]anytypefiles.MarkSpec, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	items, err := asObjectSlice(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	out := make([]anytypefiles.MarkSpec, 0, len(items))
	for i, item := range items {
		markType, err := requiredString(item, "type")
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", key, i, err)
		}
		out = append(out, anytypefiles.MarkSpec{
			From:  int32(optionalInt(item, "from", 0)),
			To:    int32(optionalInt(item, "to", 0)),
			Type:  markType,
			Param: optionalString(item, "param"),
		})
	}
	return out, nil
}

// blockTarget resolves the space/object arguments and opens a gRPC client.
func (s *mcpServer) blockTarget(args map[string]any) (*anytypefiles.Client, string, string, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, "", "", err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, "", "", err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, "", "", err
	}
	return client, spaceID, objectID, nil
}

// blockIDSet reads the current block ids of an object, for diffing against a
// later read. Anytype does not report the ids an operation created, so the only
// way to name them is to see what appeared.
func blockIDSet(client *anytypefiles.Client, spaceID, objectID string) (map[string]bool, error) {
	blocks, _, err := client.ReadBlocks(context.Background(), spaceID, objectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(blocks))
	for _, b := range blocks {
		out[b.ID] = true
	}
	return out, nil
}

// newBlockIDs reports which ids of an object are not in the given snapshot.
func newBlockIDs(client *anytypefiles.Client, spaceID, objectID string, before map[string]bool) []string {
	blocks, _, err := client.ReadBlocks(context.Background(), spaceID, objectID)
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if !before[b.ID] {
			out = append(out, b.ID)
		}
	}
	return out
}

func (s *mcpServer) toolBlockList(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	blocks, rootID, err := client.ReadBlocks(context.Background(), spaceID, objectID)
	if err != nil {
		return nil, err
	}
	textOnly := optionalBool(args, "text_only", false)
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		if textOnly && b.Kind != "text" {
			continue
		}
		entry := map[string]any{"id": b.ID, "kind": b.Kind}
		if b.Style != "" {
			entry["style"] = b.Style
		}
		if b.Text != "" {
			entry["text"] = b.Text
		}
		if b.Kind == "text" && b.Style == "checkbox" {
			entry["checked"] = b.Checked
		}
		if len(b.Marks) > 0 {
			marks := make([]map[string]any, 0, len(b.Marks))
			for _, m := range b.Marks {
				marks = append(marks, map[string]any{
					"from": m.From, "to": m.To, "type": m.Type, "param": m.Param,
				})
			}
			entry["marks"] = marks
		}
		if b.Color != "" {
			entry["color"] = b.Color
		}
		if b.BackgroundColor != "" {
			entry["background_color"] = b.BackgroundColor
		}
		if b.Align != "" {
			entry["align"] = b.Align
		}
		if b.IconEmoji != "" {
			entry["icon_emoji"] = b.IconEmoji
		}
		if b.IsHeader {
			entry["is_header"] = true
		}
		if b.TargetObjectID != "" {
			entry["target_object_id"] = b.TargetObjectID
		}
		if b.FileType != "" {
			entry["file_type"] = b.FileType
		}
		if b.FileState != "" {
			entry["file_state"] = b.FileState
		}
		if b.URL != "" {
			entry["url"] = b.URL
		}
		if b.Width > 0 {
			// Only a layout column carries one, and only once somebody has set
			// it: an absent width means the columns share their row evenly,
			// which is exactly how anytype stores that case.
			entry["width"] = b.Width
		}
		if b.PropertyKey != "" {
			entry["property_key"] = b.PropertyKey
		}
		if b.Kind == "link" {
			// Reported unconditionally, zero values included: block-link-appearance
			// writes only what it is given and keeps the rest, so a caller needs to
			// be able to see what "the rest" currently is.
			entry["card_style"] = b.CardStyle
			entry["icon_size"] = b.IconSize
			entry["description"] = b.Description
			keys := b.PropertyKeys
			if keys == nil {
				keys = []string{}
			}
			entry["property_keys"] = keys
		}
		if len(b.ChildrenIDs) > 0 {
			entry["children_ids"] = b.ChildrenIDs
		}
		out = append(out, entry)
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "root_block_id": rootID,
		"blocks": out, "count": len(out),
	}, nil
}

func (s *mcpServer) toolBlockCreate(args map[string]any) (map[string]any, error) {
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	kind := optionalString(args, "kind")
	if kind == "" {
		kind = "text"
	}
	targetID := optionalString(args, "target_id")
	position := optionalString(args, "position")
	ctx := context.Background()

	var blockID string
	switch kind {
	case "text":
		marks, err := marksFromArgs(args, "marks")
		if err != nil {
			return nil, err
		}
		blockID, err = client.CreateTextBlock(ctx, objectID, targetID, position,
			optionalString(args, "style"), optionalString(args, "text"),
			optionalBool(args, "checked", false), marks)
		if err != nil {
			return nil, err
		}
	case "link":
		linked, err := requiredString(args, "linked_object_id")
		if err != nil {
			return nil, err
		}
		blockID, err = client.CreateLinkBlock(ctx, objectID, targetID, position, linked)
		if err != nil {
			return nil, err
		}
	case "bookmark":
		url, err := requiredString(args, "url")
		if err != nil {
			return nil, err
		}
		blockID, err = client.CreateBookmarkBlock(ctx, objectID, targetID, position, url)
		if err != nil {
			return nil, err
		}
	case "divider":
		// divider_style is the documented field; style is still honoured
		// because the tool description used to send divider styles through it,
		// and callers written against that must keep working. Anything
		// unrecognised stays a plain line, as before.
		style := optionalString(args, "divider_style")
		if style == "" {
			style = optionalString(args, "style")
		}
		if style != "line" && style != "dots" {
			style = "line"
		}
		blockID, err = client.CreateDivider(ctx, objectID, targetID, position, style)
		if err != nil {
			return nil, err
		}
	case "table":
		rows := optionalInt(args, "rows", 0)
		columns := optionalInt(args, "columns", 0)
		if rows <= 0 || columns <= 0 {
			return nil, fmt.Errorf("kind=table needs rows and columns greater than 0")
		}
		blockID, err = client.CreateTable(ctx, objectID, targetID, position,
			uint32(rows), uint32(columns), optionalBool(args, "with_header_row", true))
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown kind %q; use text, link, bookmark, divider or table", kind)
	}

	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": blockID, "kind": kind, "created": true,
	}, nil
}

func (s *mcpServer) toolBlockSetText(args map[string]any) (map[string]any, error) {
	blockID, err := requiredString(args, "block_id")
	if err != nil {
		return nil, err
	}
	text, err := requiredString(args, "text")
	if err != nil {
		return nil, err
	}
	marks, err := marksFromArgs(args, "marks")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetBlockText(context.Background(), spaceID, objectID, blockID, text, marks); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_id": blockID,
		"updated": true, "mark_count": len(marks),
	}, nil
}

func (s *mcpServer) toolBlockTurnInto(args map[string]any) (map[string]any, error) {
	style, err := requiredString(args, "style")
	if err != nil {
		return nil, err
	}
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.TurnBlocksInto(context.Background(), objectID, blockIDs, style); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_ids": blockIDs, "style": style, "updated": true,
	}, nil
}

func (s *mcpServer) toolBlockSetChecked(args map[string]any) (map[string]any, error) {
	blockID, err := requiredString(args, "block_id")
	if err != nil {
		return nil, err
	}
	raw, ok := args["checked"]
	if !ok {
		return nil, fmt.Errorf("checked is required")
	}
	checked, ok := raw.(bool)
	if !ok {
		return nil, fmt.Errorf("checked must be a boolean")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetBlockChecked(context.Background(), objectID, blockID, checked); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_id": blockID,
		"checked": checked, "updated": true,
	}, nil
}

func (s *mcpServer) toolBlockMark(args map[string]any) (map[string]any, error) {
	markType, err := requiredString(args, "type")
	if err != nil {
		return nil, err
	}
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	spec := anytypefiles.MarkSpec{
		From:  int32(optionalInt(args, "from", 0)),
		To:    int32(optionalInt(args, "to", 0)),
		Type:  markType,
		Param: optionalString(args, "param"),
	}
	if err := client.ApplyMark(context.Background(), objectID, blockIDs, spec); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_ids": blockIDs,
		"type": markType, "from": spec.From, "to": spec.To, "applied": true,
	}, nil
}

func (s *mcpServer) toolBlockMove(args map[string]any) (map[string]any, error) {
	dropTargetID, err := requiredString(args, "drop_target_id")
	if err != nil {
		return nil, err
	}
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	targetObjectID := optionalString(args, "target_object_id")
	crossObject := targetObjectID != "" && targetObjectID != objectID

	// Moving into another object re-creates the blocks there under fresh ids,
	// so the ids the caller passed in stop being valid. Snapshot the
	// destination first to be able to name the new ones afterwards.
	var before map[string]bool
	if crossObject {
		before, err = blockIDSet(client, spaceID, targetObjectID)
		if err != nil {
			return nil, err
		}
	}

	if err := client.MoveBlocks(context.Background(), objectID, blockIDs,
		targetObjectID, dropTargetID, optionalString(args, "position")); err != nil {
		return nil, err
	}
	if targetObjectID == "" {
		targetObjectID = objectID
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"target_object_id": targetObjectID, "moved": true,
	}
	if crossObject {
		out["moved_block_ids"] = newBlockIDs(client, spaceID, targetObjectID, before)
		out["previous_block_ids"] = blockIDs
		out["note"] = "the blocks were re-created in the target object under new ids; use moved_block_ids from now on"
	} else {
		out["block_ids"] = blockIDs
	}
	return out, nil
}

func (s *mcpServer) toolBlockDuplicate(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	before, err := blockIDSet(client, spaceID, objectID)
	if err != nil {
		return nil, err
	}
	if err := client.DuplicateBlocks(context.Background(), objectID,
		optionalString(args, "target_id"), blockIDs, optionalString(args, "position")); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_ids": blockIDs,
		"new_block_ids": newBlockIDs(client, spaceID, objectID, before),
		"duplicated":    true,
	}, nil
}

func (s *mcpServer) toolBlockDelete(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.DeleteBlocks(context.Background(), objectID, blockIDs); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"deleted_block_ids": blockIDs, "deleted": true,
	}, nil
}

func (s *mcpServer) toolBlockColumnWidth(args map[string]any) (map[string]any, error) {
	blockID, err := requiredString(args, "block_id")
	if err != nil {
		return nil, err
	}
	widths, err := numberSlice(args, "widths")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	rowID, columns, err := client.SetColumnWidths(context.Background(), spaceID, objectID, blockID, widths)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(columns))
	for _, column := range columns {
		out = append(out, map[string]any{
			"block_id": column.BlockID, "width": column.Width,
			"previous_width": column.Previous,
		})
	}
	result := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"row_id": rowID, "columns": out, "updated": true,
	}
	if len(columns) > 0 && columns[0].Width == 0 {
		result["note"] = "all widths are zero, so the columns share the row evenly"
	}
	return result, nil
}

// numberSlice reads an array of numbers, which JSON hands over as float64.
func numberSlice(args map[string]any, key string) ([]float64, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, fmt.Errorf("%s is required", key)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of numbers", key)
	}
	out := make([]float64, 0, len(items))
	for i, item := range items {
		switch v := item.(type) {
		case float64:
			out = append(out, v)
		case int:
			out = append(out, float64(v))
		default:
			return nil, fmt.Errorf("%s[%d] must be a number", key, i)
		}
	}
	return out, nil
}

func (s *mcpServer) toolBlockStyle(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	spec := anytypefiles.StyleSpec{
		TextColor:       optionalString(args, "text_color"),
		BackgroundColor: optionalString(args, "background_color"),
		Align:           optionalString(args, "align"),
		VerticalAlign:   optionalString(args, "vertical_align"),
		IconEmoji:       optionalString(args, "icon_emoji"),
		DividerStyle:    optionalString(args, "divider_style"),
	}
	if spec.Empty() {
		return nil, fmt.Errorf("block-style needs at least one of text_color, background_color, align, vertical_align, icon_emoji or divider_style")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.StyleBlocks(context.Background(), objectID, blockIDs, spec); err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_ids": blockIDs, "updated": true,
	}
	for key, value := range map[string]string{
		"text_color": spec.TextColor, "background_color": spec.BackgroundColor,
		"align": spec.Align, "vertical_align": spec.VerticalAlign,
		"icon_emoji": spec.IconEmoji, "divider_style": spec.DividerStyle,
	} {
		if value != "" {
			out[key] = value
		}
	}
	return out, nil
}

func (s *mcpServer) toolBlockPaste(args map[string]any) (map[string]any, error) {
	markdown := rawOptionalString(args, "markdown")
	if markdown == "" {
		// "text" is the parameter name a model reaches for by analogy with the
		// other block tools; accept it rather than fail on a near miss.
		markdown = rawOptionalString(args, "text")
	}
	html := rawOptionalString(args, "html")
	if strings.TrimSpace(markdown) == "" && strings.TrimSpace(html) == "" {
		return nil, fmt.Errorf("pass markdown (or html) to paste")
	}

	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	res, err := client.PasteContent(context.Background(), spaceID, objectID,
		optionalString(args, "target_block_id"),
		optionalStringSlice(args, "replace_block_ids"),
		markdown, html)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"created_block_ids": res.BlockIDs, "created_count": len(res.BlockIDs),
		"parsed_as_markdown": res.Parsed, "pasted": true,
	}, nil
}

func (s *mcpServer) toolBlockSplit(args map[string]any) (map[string]any, error) {
	blockID, err := requiredString(args, "block_id")
	if err != nil {
		return nil, err
	}
	if !hasArg(args, "at") {
		return nil, fmt.Errorf("at is required")
	}
	at := optionalInt(args, "at", 0)
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	newID, err := client.SplitBlock(context.Background(), spaceID, objectID, blockID,
		int32(at), optionalString(args, "style"), optionalString(args, "mode"))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": blockID, "new_block_id": newID, "at": at, "split": true,
	}, nil
}

func (s *mcpServer) toolBlockMerge(args map[string]any) (map[string]any, error) {
	first, err := requiredString(args, "first_block_id")
	if err != nil {
		return nil, err
	}
	second, err := requiredString(args, "second_block_id")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.MergeBlocks(context.Background(), spaceID, objectID, first, second); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": first, "removed_block_id": second, "merged": true,
	}, nil
}
