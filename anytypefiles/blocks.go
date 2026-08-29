package anytypefiles

// Block-level editing for Anytype objects.
//
// The public REST API treats an object body as one markdown blob: it can only
// be read or overwritten wholesale, which loses block ids, checkbox state and
// inline marks. The GUI edits individual blocks, and so does this file, through
// the anytype-heart gRPC API.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// MarkSpec is an inline mark over a character range of a text block.
type MarkSpec struct {
	From  int32  `json:"from"`
	To    int32  `json:"to"`
	Type  string `json:"type"`
	Param string `json:"param,omitempty"`
}

// BlockInfo is the JSON-friendly form of a block.
type BlockInfo struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Style           string     `json:"style,omitempty"`
	Text            string     `json:"text,omitempty"`
	Checked         bool       `json:"checked,omitempty"`
	IsHeader        bool       `json:"is_header,omitempty"`
	Marks           []MarkSpec `json:"marks,omitempty"`
	Color           string     `json:"color,omitempty"`
	BackgroundColor string     `json:"background_color,omitempty"`
	Align           string     `json:"align,omitempty"`
	IconEmoji       string     `json:"icon_emoji,omitempty"`
	TargetObjectID  string     `json:"target_object_id,omitempty"`
	FileType        string     `json:"file_type,omitempty"`
	FileState       string     `json:"file_state,omitempty"`
	URL             string     `json:"url,omitempty"`
	ChildrenIDs     []string   `json:"children_ids,omitempty"`

	// Appearance of a link block, named after the block-link-appearance
	// arguments so a caller can read a value here and pass it straight back.
	// The three enums are reported for every link block, zero value included
	// (text, none, none), because otherwise the current appearance would not be
	// readable at all — and the tool that changes it has to merge against it.
	CardStyle    string   `json:"card_style,omitempty"`
	IconSize     string   `json:"icon_size,omitempty"`
	Description  string   `json:"description,omitempty"`
	PropertyKeys []string `json:"property_keys,omitempty"`

	// PropertyKey is the property a relation block shows. Reported in Anytype's
	// internal spelling, which is what the block stores — for a property of
	// one's own that is a generated id and not the key list-properties gives,
	// so this is the only way to see which property a relation block really
	// points at.
	PropertyKey string `json:"property_key,omitempty"`

	// Width is the share of its row a layout column takes. Reported so the
	// result of block-column-width can be read back; 0 means the columns share
	// the row evenly, which is also what heart writes when a column is added
	// or removed.
	Width float64 `json:"width,omitempty"`
}

var textStyles = map[string]model.BlockContentTextStyle{
	"paragraph":      model.BlockContentText_Paragraph,
	"header1":        model.BlockContentText_Header1,
	"header2":        model.BlockContentText_Header2,
	"header3":        model.BlockContentText_Header3,
	"header4":        model.BlockContentText_Header4,
	"quote":          model.BlockContentText_Quote,
	"code":           model.BlockContentText_Code,
	"title":          model.BlockContentText_Title,
	"checkbox":       model.BlockContentText_Checkbox,
	"todo":           model.BlockContentText_Checkbox,
	"bulleted":       model.BlockContentText_Marked,
	"marked":         model.BlockContentText_Marked,
	"numbered":       model.BlockContentText_Numbered,
	"toggle":         model.BlockContentText_Toggle,
	"description":    model.BlockContentText_Description,
	"callout":        model.BlockContentText_Callout,
	"toggle_header1": model.BlockContentText_ToggleHeader1,
	"toggle_header2": model.BlockContentText_ToggleHeader2,
	"toggle_header3": model.BlockContentText_ToggleHeader3,
}

// textStyleNames maps a stored style back to the ONE name the tool schemas
// advertise. block-list used to report the generated String() instead, which
// leaks protobuf spelling: a bulleted list came back as "marked" and an
// underline mark as "underscored" — names TextStyleNames/MarkTypeNames do not
// list, so a client that validates against the schema could not pass the value
// it had just read. The read side now speaks the write side's vocabulary.
//
// Written out rather than reversed with enumName: textStyles holds aliases on
// the same value (bulleted/marked, checkbox/todo), and map iteration would pick
// between them at random, so the reported name would differ from run to run.
//
// "title" is here because a title block has to be reportable, but it stays out
// of TextStyleNames on purpose: it is the object's name block, not a style to
// turn an arbitrary paragraph into.
var textStyleNames = map[model.BlockContentTextStyle]string{
	model.BlockContentText_Paragraph:     "paragraph",
	model.BlockContentText_Header1:       "header1",
	model.BlockContentText_Header2:       "header2",
	model.BlockContentText_Header3:       "header3",
	model.BlockContentText_Header4:       "header4",
	model.BlockContentText_Quote:         "quote",
	model.BlockContentText_Code:          "code",
	model.BlockContentText_Title:         "title",
	model.BlockContentText_Checkbox:      "checkbox",
	model.BlockContentText_Marked:        "bulleted",
	model.BlockContentText_Numbered:      "numbered",
	model.BlockContentText_Toggle:        "toggle",
	model.BlockContentText_Description:   "description",
	model.BlockContentText_Callout:       "callout",
	model.BlockContentText_ToggleHeader1: "toggle_header1",
	model.BlockContentText_ToggleHeader2: "toggle_header2",
	model.BlockContentText_ToggleHeader3: "toggle_header3",
}

// markTypeNames does the same for inline marks: MarkTypeNames advertises "code"
// and "underline", the protobuf calls them Keyboard and Underscored.
var markTypeNames = map[model.BlockContentTextMarkType]string{
	model.BlockContentTextMark_Strikethrough:   "strikethrough",
	model.BlockContentTextMark_Keyboard:        "code",
	model.BlockContentTextMark_Italic:          "italic",
	model.BlockContentTextMark_Bold:            "bold",
	model.BlockContentTextMark_Underscored:     "underline",
	model.BlockContentTextMark_Link:            "link",
	model.BlockContentTextMark_TextColor:       "text_color",
	model.BlockContentTextMark_BackgroundColor: "background_color",
	model.BlockContentTextMark_Mention:         "mention",
	model.BlockContentTextMark_Emoji:           "emoji",
	model.BlockContentTextMark_Object:          "object",
}

// TextStyleName reports a stored text style under its schema name. A style a
// newer heart adds falls back to the protobuf spelling, which is wrong in the
// same way as before but still better than an empty string.
func TextStyleName(style model.BlockContentTextStyle) string {
	if name, ok := textStyleNames[style]; ok {
		return name
	}
	return strings.ToLower(style.String())
}

// MarkTypeName reports a stored mark type under its schema name.
func MarkTypeName(markType model.BlockContentTextMarkType) string {
	if name, ok := markTypeNames[markType]; ok {
		return name
	}
	return strings.ToLower(markType.String())
}

// CanonicalTextStyle resolves an accepted style name to the one the read side
// reports, so a tool can answer with the name block-list will show rather than
// echoing the alias the caller happened to use. Answering "bulleted" to a write
// that block-list then reports as "marked" is what made a working turn-into
// look like a silent no-op.
func CanonicalTextStyle(style string) string {
	parsed, err := lookupEnum(textStyles, style, "block style", false)
	if err != nil {
		return style
	}
	return TextStyleName(parsed)
}

// CanonicalMarkType is the same for inline mark names.
func CanonicalMarkType(markType string) string {
	parsed, err := lookupEnum(markTypes, markType, "mark type", false)
	if err != nil {
		return markType
	}
	return MarkTypeName(parsed)
}

var blockPositions = map[string]model.BlockPosition{
	"top":         model.Block_Top,
	"bottom":      model.Block_Bottom,
	"left":        model.Block_Left,
	"right":       model.Block_Right,
	"inner":       model.Block_Inner,
	"inner_first": model.Block_InnerFirst,
	"replace":     model.Block_Replace,
}

var markTypes = map[string]model.BlockContentTextMarkType{
	"strikethrough":    model.BlockContentTextMark_Strikethrough,
	"keyboard":         model.BlockContentTextMark_Keyboard,
	"code":             model.BlockContentTextMark_Keyboard,
	"italic":           model.BlockContentTextMark_Italic,
	"bold":             model.BlockContentTextMark_Bold,
	"underscored":      model.BlockContentTextMark_Underscored,
	"underline":        model.BlockContentTextMark_Underscored,
	"link":             model.BlockContentTextMark_Link,
	"text_color":       model.BlockContentTextMark_TextColor,
	"background_color": model.BlockContentTextMark_BackgroundColor,
	"mention":          model.BlockContentTextMark_Mention,
	"emoji":            model.BlockContentTextMark_Emoji,
	"object":           model.BlockContentTextMark_Object,
}

var blockAligns = map[string]model.BlockAlign{
	"left":    model.Block_AlignLeft,
	"center":  model.Block_AlignCenter,
	"right":   model.Block_AlignRight,
	"justify": model.Block_AlignJustify,
}

var divStyles = map[string]model.BlockContentDivStyle{
	"line": model.BlockContentDiv_Line,
	"dots": model.BlockContentDiv_Dots,
}

// childCapableStyles mirrors canHaveChildren in heart's
// core/block/editor/basic/basic.go: the styles a block-move with position=inner
// or inner_first may drop onto. Kept here only to give a caller a useful answer
// before the RPC; heart stays the authority, and a move it refuses still fails.
var childCapableStyles = map[string]bool{
	"paragraph": true, "quote": true, "checkbox": true, "bulleted": true,
	"numbered": true, "toggle": true, "callout": true,
	"toggle_header1": true, "toggle_header2": true, "toggle_header3": true,
}

// toggleEquivalents pairs a heading with the collapsible heading that looks the
// same and does take children. header4 has no counterpart because Anytype has
// no toggle_header4.
var toggleEquivalents = map[string]string{
	"header1": "toggle_header1",
	"header2": "toggle_header2",
	"header3": "toggle_header3",
}

// StyleCanHaveChildren reports whether a text block of this style may hold
// nested blocks. An unknown name counts as capable, so a style a newer heart
// adds is left to heart to judge rather than pre-emptively refused here.
func StyleCanHaveChildren(style string) bool {
	if _, known := textStyles[strings.ToLower(strings.TrimSpace(style))]; !known {
		return true
	}
	return childCapableStyles[CanonicalTextStyle(style)]
}

// ToggleEquivalent gives the collapsible heading that renders like the given
// heading and can hold children.
func ToggleEquivalent(style string) (string, bool) {
	name, ok := toggleEquivalents[CanonicalTextStyle(style)]
	return name, ok
}

// TextStyleNames lists the accepted block styles, for tool schemas.
func TextStyleNames() []string {
	return []string{"paragraph", "header1", "header2", "header3", "header4",
		"quote", "code", "checkbox", "bulleted", "numbered", "toggle", "callout",
		"description", "toggle_header1", "toggle_header2", "toggle_header3"}
}

// DividerStyleNames lists the accepted divider styles, for tool schemas.
//
// Separate from TextStyleNames on purpose: a divider and a text block are told
// apart by the block kind, and overloading one style enum for both meant the
// schema advertised sixteen text styles for a divider and neither of the two it
// actually takes.
func DividerStyleNames() []string {
	return []string{"line", "dots"}
}

// MarkTypeNames lists the accepted inline mark types, for tool schemas.
func MarkTypeNames() []string {
	return []string{"bold", "italic", "strikethrough", "underline", "code",
		"link", "text_color", "background_color", "mention", "emoji", "object"}
}

// BlockPositionNames lists the accepted insert positions, for tool schemas.
// BlockPositionNames lists the positions a caller may pass.
//
// left and right belong in here, and leaving them out was not a documentation
// gap but a missing capability: the enum is what ends up in the JSON schema, so
// a client that validates arguments against it — every one of them does —
// could not send the value at all, however well blockPositions parsed it. They
// are the only way to put blocks side by side: heart turns them into a
// row/column structure in InsertTo -> moveFromSide -> wrapToRow, which is what
// dragging a block beside another one does in the GUI.
//
// They need a target: with an empty target_id heart falls back to inner, so the
// block lands at the end of the page instead of next to something.
func BlockPositionNames() []string {
	return []string{"bottom", "top", "left", "right", "inner", "inner_first", "replace"}
}

func blockFromModel(b *model.Block) BlockInfo {
	info := BlockInfo{
		ID:              b.Id,
		BackgroundColor: b.BackgroundColor,
		ChildrenIDs:     b.ChildrenIds,
	}
	if b.Align != model.Block_AlignLeft {
		info.Align = strings.TrimPrefix(strings.ToLower(b.Align.String()), "align")
	}
	switch content := b.Content.(type) {
	case *model.BlockContentOfText:
		info.Kind = "text"
		info.Style = TextStyleName(content.Text.Style)
		info.Text = content.Text.Text
		info.Checked = content.Text.Checked
		info.Color = content.Text.Color
		info.IconEmoji = content.Text.IconEmoji
		for _, m := range content.Text.GetMarks().GetMarks() {
			info.Marks = append(info.Marks, MarkSpec{
				From:  m.GetRange().GetFrom(),
				To:    m.GetRange().GetTo(),
				Type:  MarkTypeName(m.Type),
				Param: m.Param,
			})
		}
	case *model.BlockContentOfLink:
		info.Kind = "link"
		info.TargetObjectID = content.Link.TargetBlockId
		info.CardStyle = enumName(linkCardStyles, content.Link.CardStyle)
		info.IconSize = enumName(linkIconSizes, content.Link.IconSize)
		info.Description = enumName(linkDescriptions, content.Link.Description)
		info.PropertyKeys = content.Link.Relations
	case *model.BlockContentOfBookmark:
		info.Kind = "bookmark"
		info.URL = content.Bookmark.Url
		info.TargetObjectID = content.Bookmark.TargetObjectId
	case *model.BlockContentOfFile:
		info.Kind = "file"
		info.Text = content.File.Name
		info.TargetObjectID = content.File.TargetObjectId
		// An upload is asynchronous, so the state is the only way to tell a
		// finished file block from one that is still transferring or failed.
		info.FileType = strings.ToLower(content.File.Type.String())
		info.FileState = strings.ToLower(content.File.State.String())
	case *model.BlockContentOfDataview:
		info.Kind = "dataview"
		// An embedded query points at the object it shows. Without this the
		// block is just "a dataview" and there is no way to tell which query a
		// page displays, or that a page displays two of them.
		info.TargetObjectID = content.Dataview.TargetObjectId
	case *model.BlockContentOfDiv:
		info.Kind = "divider"
		info.Style = strings.ToLower(content.Div.Style.String())
	case *model.BlockContentOfLatex:
		info.Kind = "latex"
		info.Text = content.Latex.Text
	case *model.BlockContentOfTable:
		info.Kind = "table"
	case *model.BlockContentOfTableRow:
		info.Kind = "table_row"
		info.IsHeader = content.TableRow.IsHeader
	case *model.BlockContentOfTableColumn:
		info.Kind = "table_column"
	case *model.BlockContentOfLayout:
		info.Kind = "layout"
		if content.Layout.Style == model.BlockContentLayout_Column {
			info.Width = b.GetFields().GetFields()["width"].GetNumberValue()
		}
		// A table's two children are both layout blocks, distinguished only by
		// this style (table_rows vs table_columns). Without it the table
		// structure would have to be guessed from child order.
		info.Style = strings.ToLower(content.Layout.Style.String())
	case *model.BlockContentOfSmartblock:
		info.Kind = "smartblock"
	case *model.BlockContentOfRelation:
		info.Kind = "relation"
		info.PropertyKey = content.Relation.GetKey()
	case *model.BlockContentOfWidget:
		info.Kind = "widget"
	default:
		info.Kind = "other"
	}
	return info
}

func marksToModel(specs []MarkSpec) (*model.BlockContentTextMarks, error) {
	if len(specs) == 0 {
		return &model.BlockContentTextMarks{}, nil
	}
	out := &model.BlockContentTextMarks{}
	for i, spec := range specs {
		markType, err := lookupEnum(markTypes, spec.Type, "mark type", false)
		if err != nil {
			return nil, fmt.Errorf("marks[%d]: %w", i, err)
		}
		if spec.To < spec.From {
			return nil, fmt.Errorf("marks[%d]: to must be >= from", i)
		}
		out.Marks = append(out.Marks, &model.BlockContentTextMark{
			Range: &model.Range{From: spec.From, To: spec.To},
			Type:  markType,
			Param: spec.Param,
		})
	}
	return out, nil
}

// ReadBlocks returns every block of an object, in document order.
func (c *Client) ReadBlocks(ctx context.Context, spaceID, objectID string) ([]BlockInfo, string, error) {
	if strings.TrimSpace(objectID) == "" {
		return nil, "", errors.New("object_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	if err != nil {
		return nil, "", fmt.Errorf("gRPC ObjectShow failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return nil, "", fmt.Errorf("ObjectShow error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	view := resp.GetObjectView()
	blocks := make([]BlockInfo, 0, len(view.GetBlocks()))
	for _, b := range view.GetBlocks() {
		blocks = append(blocks, blockFromModel(b))
	}
	return blocks, view.GetRootId(), nil
}

// CreateTextBlock inserts a text block relative to targetID and returns its id.
// An empty targetID appends to the end of the object.
func (c *Client) CreateTextBlock(ctx context.Context, objectID, targetID, position, style, text string, checked bool, marks []MarkSpec) (string, error) {
	textStyle, err := lookupEnum(textStyles, style, "block style", true)
	if err != nil {
		return "", err
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return "", err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	modelMarks, err := marksToModel(marks)
	if err != nil {
		return "", err
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.BlockCreate(callCtx, &pb.RpcBlockCreateRequest{
		ContextId: objectID,
		TargetId:  targetID,
		Position:  pos,
		Block: &model.Block{
			Content: &model.BlockContentOfText{Text: &model.BlockContentText{
				Text:    text,
				Style:   textStyle,
				Checked: checked,
				Marks:   modelMarks,
			}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockCreate failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockCreateResponseError_NULL {
		return "", fmt.Errorf("BlockCreate error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

// SetBlockText replaces the text and inline marks of a text block.
func (c *Client) SetBlockText(ctx context.Context, spaceID, objectID, blockID, text string, marks []MarkSpec) error {
	if err := c.setBlockTextPending(ctx, objectID, blockID, text, marks); err != nil {
		return err
	}
	return c.FlushTextChanges(ctx, spaceID, objectID)
}

// setBlockTextPending writes a text block without forcing the change out.
//
// BlockTextSetText does not apply immediately: anytype-heart keeps the change
// in a pending state and only commits it three seconds later, when a different
// block is edited, or when the object is closed. Without a flush the write is
// invisible to the very next read, which for a tool server means returning a
// stale result to the caller. Batch writers use this and flush once at the end.
func (c *Client) setBlockTextPending(ctx context.Context, objectID, blockID, text string, marks []MarkSpec) error {
	modelMarks, err := marksToModel(marks)
	if err != nil {
		return err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockTextSetText(callCtx, &pb.RpcBlockTextSetTextRequest{
		ContextId: objectID, BlockId: blockID, Text: text, Marks: modelMarks,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockTextSetText failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockTextSetTextResponseError_NULL {
		return fmt.Errorf("BlockTextSetText error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// FlushTextChanges commits text edits that anytype-heart is still holding.
//
// Closing the object is what the GUI does when you navigate away from a page,
// and it runs the same hook that commits pending text. It is safe to call at
// any time: every read in this package opens the object again through
// ObjectShow.
func (c *Client) FlushTextChanges(ctx context.Context, spaceID, objectID string) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectClose(callCtx, &pb.RpcObjectCloseRequest{
		ContextId: objectID, ObjectId: objectID, SpaceId: spaceID,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectClose failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectCloseResponseError_NULL {
		return fmt.Errorf("ObjectClose error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// TurnBlocksInto changes the style of text blocks.
func (c *Client) TurnBlocksInto(ctx context.Context, objectID string, blockIDs []string, style string) error {
	if len(blockIDs) == 0 {
		return errors.New("block_ids is required")
	}
	textStyle, err := lookupEnum(textStyles, style, "block style", false)
	if err != nil {
		return err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockListTurnInto(callCtx, &pb.RpcBlockListTurnIntoRequest{
		ContextId: objectID, BlockIds: blockIDs, Style: textStyle,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockListTurnInto failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockListTurnIntoResponseError_NULL {
		return fmt.Errorf("BlockListTurnInto error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// SetBlockChecked ticks or unticks a checkbox block.
func (c *Client) SetBlockChecked(ctx context.Context, objectID, blockID string, checked bool) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockTextSetChecked(callCtx, &pb.RpcBlockTextSetCheckedRequest{
		ContextId: objectID, BlockId: blockID, Checked: checked,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockTextSetChecked failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockTextSetCheckedResponseError_NULL {
		return fmt.Errorf("BlockTextSetChecked error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// ApplyMark applies one inline mark to a range in the given blocks.
func (c *Client) ApplyMark(ctx context.Context, objectID string, blockIDs []string, spec MarkSpec) error {
	if len(blockIDs) == 0 {
		return errors.New("block_ids is required")
	}
	markType, err := lookupEnum(markTypes, spec.Type, "mark type", false)
	if err != nil {
		return err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockTextListSetMark(callCtx, &pb.RpcBlockTextListSetMarkRequest{
		ContextId: objectID, BlockIds: blockIDs,
		Mark: &model.BlockContentTextMark{
			Range: &model.Range{From: spec.From, To: spec.To},
			Type:  markType,
			Param: spec.Param,
		},
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockTextListSetMark failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockTextListSetMarkResponseError_NULL {
		return fmt.Errorf("BlockTextListSetMark error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// DeleteBlocks removes blocks from an object.
func (c *Client) DeleteBlocks(ctx context.Context, objectID string, blockIDs []string) error {
	if len(blockIDs) == 0 {
		return errors.New("block_ids is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockListDelete(callCtx, &pb.RpcBlockListDeleteRequest{
		ContextId: objectID, BlockIds: blockIDs,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockListDelete failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockListDeleteResponseError_NULL {
		return fmt.Errorf("BlockListDelete error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// MoveBlocks moves blocks within an object or into another object.
func (c *Client) MoveBlocks(ctx context.Context, objectID string, blockIDs []string, targetObjectID, dropTargetID, position string) error {
	if len(blockIDs) == 0 {
		return errors.New("block_ids is required")
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	if targetObjectID == "" {
		targetObjectID = objectID
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockListMoveToExistingObject(callCtx, &pb.RpcBlockListMoveToExistingObjectRequest{
		ContextId: objectID, BlockIds: blockIDs,
		TargetContextId: targetObjectID, DropTargetId: dropTargetID, Position: pos,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockListMoveToExistingObject failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockListMoveToExistingObjectResponseError_NULL {
		return fmt.Errorf("BlockListMoveToExistingObject error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// DuplicateBlocks copies blocks next to a target block.
func (c *Client) DuplicateBlocks(ctx context.Context, objectID, targetID string, blockIDs []string, position string) error {
	if len(blockIDs) == 0 {
		return errors.New("block_ids is required")
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	if targetID == "" {
		targetID = blockIDs[len(blockIDs)-1]
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockListDuplicate(callCtx, &pb.RpcBlockListDuplicateRequest{
		ContextId: objectID, TargetId: targetID, BlockIds: blockIDs, Position: pos,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockListDuplicate failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockListDuplicateResponseError_NULL {
		return fmt.Errorf("BlockListDuplicate error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// StyleSpec is everything block-style can change at once.
type StyleSpec struct {
	TextColor       string
	BackgroundColor string
	Align           string
	VerticalAlign   string
	IconEmoji       string
	DividerStyle    string
}

// Empty reports whether the caller asked for nothing at all.
func (s StyleSpec) Empty() bool {
	return s.TextColor == "" && s.BackgroundColor == "" && s.Align == "" &&
		s.VerticalAlign == "" && s.IconEmoji == "" && s.DividerStyle == ""
}

// StyleBlocks applies the requested presentation changes.
//
// Each aspect is a separate RPC in anytype-heart, so this fans out; a caller
// setting colour and callout icon together should not have to know that.
func (c *Client) StyleBlocks(ctx context.Context, objectID string, blockIDs []string, spec StyleSpec) error {
	if len(blockIDs) == 0 {
		return errors.New("block_ids is required")
	}
	textColor, backgroundColor, align := spec.TextColor, spec.BackgroundColor, spec.Align
	if textColor != "" {
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockTextListSetColor(callCtx, &pb.RpcBlockTextListSetColorRequest{
			ContextId: objectID, BlockIds: blockIDs, Color: textColor,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("gRPC BlockTextListSetColor failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockTextListSetColorResponseError_NULL {
			return fmt.Errorf("BlockTextListSetColor error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	if backgroundColor != "" {
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockListSetBackgroundColor(callCtx, &pb.RpcBlockListSetBackgroundColorRequest{
			ContextId: objectID, BlockIds: blockIDs, Color: backgroundColor,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("gRPC BlockListSetBackgroundColor failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockListSetBackgroundColorResponseError_NULL {
			return fmt.Errorf("BlockListSetBackgroundColor error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	if align != "" {
		blockAlign, err := lookupEnum(blockAligns, align, "align", false)
		if err != nil {
			return err
		}
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockListSetAlign(callCtx, &pb.RpcBlockListSetAlignRequest{
			ContextId: objectID, BlockIds: blockIDs, Align: blockAlign,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("gRPC BlockListSetAlign failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockListSetAlignResponseError_NULL {
			return fmt.Errorf("BlockListSetAlign error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	if spec.VerticalAlign != "" {
		value, err := lookupEnum(verticalAligns, spec.VerticalAlign, "vertical align", false)
		if err != nil {
			return err
		}
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockListSetVerticalAlign(callCtx, &pb.RpcBlockListSetVerticalAlignRequest{
			ContextId: objectID, BlockIds: blockIDs, VerticalAlign: value,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("gRPC BlockListSetVerticalAlign failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockListSetVerticalAlignResponseError_NULL {
			return fmt.Errorf("BlockListSetVerticalAlign error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	if spec.DividerStyle != "" {
		value, err := lookupEnum(divStyles, spec.DividerStyle, "divider style", false)
		if err != nil {
			return err
		}
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockDivListSetStyle(callCtx, &pb.RpcBlockDivListSetStyleRequest{
			ContextId: objectID, BlockIds: blockIDs, Style: value,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("gRPC BlockDivListSetStyle failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockDivListSetStyleResponseError_NULL {
			return fmt.Errorf("BlockDivListSetStyle error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	if spec.IconEmoji != "" {
		// One block at a time: this RPC takes a single block, unlike its List siblings.
		for _, id := range blockIDs {
			callCtx, cancel := c.contextWithAuth(ctx)
			resp, err := c.rpc.BlockTextSetIcon(callCtx, &pb.RpcBlockTextSetIconRequest{
				ContextId: objectID, BlockId: id, IconEmoji: spec.IconEmoji,
			})
			cancel()
			if err != nil {
				return fmt.Errorf("gRPC BlockTextSetIcon failed: %w", err)
			}
			if resp.Error != nil && resp.Error.Code != pb.RpcBlockTextSetIconResponseError_NULL {
				return fmt.Errorf("BlockTextSetIcon error (%s): %s", resp.Error.Code, resp.Error.Description)
			}
		}
	}
	return nil
}

// CreateLinkBlock inserts a block linking to an existing object.
func (c *Client) CreateLinkBlock(ctx context.Context, objectID, targetID, position, linkedObjectID string) (string, error) {
	if strings.TrimSpace(linkedObjectID) == "" {
		return "", errors.New("linked_object_id is required")
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return "", err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockCreate(callCtx, &pb.RpcBlockCreateRequest{
		ContextId: objectID, TargetId: targetID, Position: pos,
		Block: &model.Block{Content: &model.BlockContentOfLink{
			Link: &model.BlockContentLink{TargetBlockId: linkedObjectID},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockCreate(link) failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockCreateResponseError_NULL {
		return "", fmt.Errorf("BlockCreate(link) error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

// CreateBookmarkBlock inserts a bookmark block and fetches its preview.
func (c *Client) CreateBookmarkBlock(ctx context.Context, objectID, targetID, position, url string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", errors.New("url is required")
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return "", err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockBookmarkCreateAndFetch(callCtx, &pb.RpcBlockBookmarkCreateAndFetchRequest{
		ContextId: objectID, TargetId: targetID, Position: pos, Url: url,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockBookmarkCreateAndFetch failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockBookmarkCreateAndFetchResponseError_NULL {
		return "", fmt.Errorf("BlockBookmarkCreateAndFetch error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

// CreateDivider inserts a divider block.
func (c *Client) CreateDivider(ctx context.Context, objectID, targetID, position, style string) (string, error) {
	divStyle, err := lookupEnum(divStyles, style, "divider style", true)
	if err != nil {
		return "", err
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return "", err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockCreate(callCtx, &pb.RpcBlockCreateRequest{
		ContextId: objectID, TargetId: targetID, Position: pos,
		Block: &model.Block{Content: &model.BlockContentOfDiv{
			Div: &model.BlockContentDiv{Style: divStyle},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockCreate(divider) failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockCreateResponseError_NULL {
		return "", fmt.Errorf("BlockCreate(divider) error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

// CreateTable inserts a table block.
func (c *Client) CreateTable(ctx context.Context, objectID, targetID, position string, rows, columns uint32, withHeaderRow bool) (string, error) {
	if rows == 0 || columns == 0 {
		return "", errors.New("rows and columns must both be > 0")
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return "", err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockTableCreate(callCtx, &pb.RpcBlockTableCreateRequest{
		ContextId: objectID, TargetId: targetID, Position: pos,
		Rows: rows, Columns: columns, WithHeaderRow: withHeaderRow,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockTableCreate failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockTableCreateResponseError_NULL {
		return "", fmt.Errorf("BlockTableCreate error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

var splitModes = map[string]pb.RpcBlockSplitRequestMode{
	"bottom": pb.RpcBlockSplitRequest_BOTTOM,
	"top":    pb.RpcBlockSplitRequest_TOP,
	"inner":  pb.RpcBlockSplitRequest_INNER,
}

// SplitModeNames lists the accepted split modes, for tool schemas.
func SplitModeNames() []string { return []string{"bottom", "top", "inner"} }

// PasteResult reports what a paste produced.
type PasteResult struct {
	BlockIDs []string `json:"block_ids"`
	Parsed   bool     `json:"parsed_as_markdown"`
}

// PasteContent inserts a whole document fragment in one call.
//
// This is the cheap way to write structured content: anytype-heart runs the
// text through its markdown parser, so headings, lists, checkboxes, quotes,
// code fences and tables all arrive as real blocks. Doing the same with
// block-create would cost one round trip per block.
//
// With no target the content is appended at the end of the object, which is the
// common case and needs no anchor.
//
// targetBlockID behaves like a cursor rather than an anchor: the paste MERGES
// into that block instead of being inserted after it. Targeting a code block or
// a table cell inserts the text VERBATIM, without markdown parsing, because a
// paste into code is meant to stay literal.
func (c *Client) PasteContent(ctx context.Context, spaceID, objectID, targetBlockID string, replaceBlockIDs []string, text, html string) (PasteResult, error) {
	text = strings.TrimSpace(text)
	html = strings.TrimSpace(html)
	if text == "" && html == "" {
		return PasteResult{}, errors.New("nothing to paste: pass markdown, text or html")
	}
	// Pasting into the title or description block does not insert blocks at all
	// — it edits the object's name. Verified: pasting "# Gepastet" onto the
	// title of an object named "ORIGINALNAME" renamed it to
	// "GepastetORIGINALNAME" and inserted nothing. That is silent corruption of
	// a field the caller did not mean to touch, so it is refused here.
	switch strings.TrimSpace(targetBlockID) {
	case "title", "description":
		return PasteResult{}, fmt.Errorf(
			"refusing to paste into the %s block: this rewrites the object's %s instead of inserting blocks. "+
				"Omit target_block_id to append to the body, or change the name with the object update tools",
			targetBlockID, targetBlockID)
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockPaste(callCtx, &pb.RpcBlockPasteRequest{
		ContextId:        objectID,
		FocusedBlockId:   targetBlockID,
		SelectedBlockIds: replaceBlockIDs,
		TextSlot:         text,
		HtmlSlot:         html,
	})
	if err != nil {
		return PasteResult{}, fmt.Errorf("gRPC BlockPaste failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockPasteResponseError_NULL {
		return PasteResult{}, fmt.Errorf("BlockPaste error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	// Pasted text is written through the same deferred path as SetBlockText.
	if err := c.FlushTextChanges(ctx, spaceID, objectID); err != nil {
		return PasteResult{}, err
	}
	return PasteResult{BlockIDs: resp.BlockIds, Parsed: html == ""}, nil
}

// SplitBlock cuts one text block in two at a character offset and returns the
// id of the new block.
func (c *Client) SplitBlock(ctx context.Context, spaceID, objectID, blockID string, at int32, style, mode string) (string, error) {
	if strings.TrimSpace(blockID) == "" {
		return "", errors.New("block_id is required")
	}
	if at < 0 {
		return "", fmt.Errorf("at must be >= 0, got %d", at)
	}
	splitMode, err := lookupEnum(splitModes, mode, "split mode", true)
	if err != nil {
		return "", err
	}
	textStyle, err := lookupEnum(textStyles, style, "block style", true)
	if err != nil {
		return "", err
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	resp, err := c.rpc.BlockSplit(callCtx, &pb.RpcBlockSplitRequest{
		ContextId: objectID, BlockId: blockID,
		Range: &model.Range{From: at, To: at},
		Style: textStyle, Mode: splitMode,
	})
	cancel()
	if err != nil {
		return "", fmt.Errorf("gRPC BlockSplit failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockSplitResponseError_NULL {
		return "", fmt.Errorf("BlockSplit error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	if err := c.FlushTextChanges(ctx, spaceID, objectID); err != nil {
		return "", err
	}
	return resp.BlockId, nil
}

// MergeBlocks joins the second block onto the end of the first.
func (c *Client) MergeBlocks(ctx context.Context, spaceID, objectID, firstBlockID, secondBlockID string) error {
	if strings.TrimSpace(firstBlockID) == "" || strings.TrimSpace(secondBlockID) == "" {
		return errors.New("first_block_id and second_block_id are required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	resp, err := c.rpc.BlockMerge(callCtx, &pb.RpcBlockMergeRequest{
		ContextId: objectID, FirstBlockId: firstBlockID, SecondBlockId: secondBlockID,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("gRPC BlockMerge failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockMergeResponseError_NULL {
		return fmt.Errorf("BlockMerge error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return c.FlushTextChanges(ctx, spaceID, objectID)
}
