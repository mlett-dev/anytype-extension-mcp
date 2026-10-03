package anytypefiles

// The sidebar: a space's derived system objects, and the widgets that make it
// up. Both exist because the sidebar is not a feature in anytype-heart but an
// object — the space's widget smartblock — whose id is derived from the space
// key and therefore cannot be guessed.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

// personalWidgetsPrefix and the dot-to-underscore replacement mirror
// core/domain/id.go's NewPersonalWidgetsId. Reimplemented rather than imported
// to keep core/domain out of this module's dependency graph; the encoding is
// two lines and heart's own tests pin it.
const personalWidgetsPrefix = "_personalWidgets_"

// SystemObjects are the derived smartblocks a space is built around. Every one
// of them is reachable only through this call: they are hidden from search,
// absent from object-graph and not carried by an export.
type SystemObjects struct {
	SpaceID           string `json:"space_id"`
	WidgetsID         string `json:"widgets_id,omitempty"`
	HomeID            string `json:"home_id,omitempty"`
	ArchiveID         string `json:"archive_id,omitempty"`
	ProfileID         string `json:"profile_id,omitempty"`
	WorkspaceObjectID string `json:"workspace_object_id,omitempty"`
	SpaceChatID       string `json:"space_chat_id,omitempty"`
	SpaceViewID       string `json:"space_view_id,omitempty"`
	TechSpaceID       string `json:"tech_space_id,omitempty"`
	AccountSpaceID    string `json:"account_space_id,omitempty"`
	PersonalWidgetsID string `json:"personal_widgets_id,omitempty"`
}

// PersonalWidgetsID derives the id of a space's personal-widgets object, the
// per-account counterpart of the shared widget smartblock. Unlike the others
// this one is computable, because it is a virtual object keyed by space id.
func PersonalWidgetsID(spaceID string) string {
	if strings.TrimSpace(spaceID) == "" {
		return ""
	}
	return personalWidgetsPrefix + strings.Replace(spaceID, ".", "_", 1)
}

// ReadSystemObjects reports the derived smartblock ids of a space.
//
// WorkspaceOpen is the only RPC that answers with AccountInfo for a chosen
// space (WorkspaceGetCurrent only ever describes the active one). It loads the
// space as a side effect, which is what any client does on opening it, and
// withChat stays off so no chat subscription is created.
func (c *Client) ReadSystemObjects(ctx context.Context, spaceID string) (SystemObjects, error) {
	if strings.TrimSpace(spaceID) == "" {
		return SystemObjects{}, errors.New("space_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.WorkspaceOpen(callCtx, &pb.RpcWorkspaceOpenRequest{SpaceId: spaceID})
	if err != nil {
		return SystemObjects{}, fmt.Errorf("gRPC WorkspaceOpen failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcWorkspaceOpenResponseError_NULL {
		return SystemObjects{}, fmt.Errorf("WorkspaceOpen error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	info := resp.GetInfo()
	if info == nil {
		return SystemObjects{}, errors.New("WorkspaceOpen returned no account info")
	}
	return SystemObjects{
		SpaceID:           spaceID,
		WidgetsID:         info.GetWidgetsId(),
		HomeID:            info.GetHomeObjectId(),
		ArchiveID:         info.GetArchiveObjectId(),
		ProfileID:         info.GetProfileObjectId(),
		WorkspaceObjectID: info.GetWorkspaceObjectId(),
		SpaceChatID:       info.GetSpaceChatId(),
		SpaceViewID:       info.GetSpaceViewId(),
		TechSpaceID:       info.GetTechSpaceId(),
		AccountSpaceID:    info.GetAccountSpaceId(),
		PersonalWidgetsID: PersonalWidgetsID(spaceID),
	}, nil
}

var widgetLayouts = map[string]model.BlockContentWidgetLayout{
	"link":         model.BlockContentWidget_Link,
	"tree":         model.BlockContentWidget_Tree,
	"list":         model.BlockContentWidget_List,
	"compact_list": model.BlockContentWidget_CompactList,
	"view":         model.BlockContentWidget_View,
}

// WidgetLayoutNames lists the accepted widget layouts, for tool schemas.
func WidgetLayoutNames() []string {
	return []string{"link", "tree", "list", "compact_list", "view"}
}

// builtinWidgetTargets are the target ids that name a built-in listing rather
// than an object. heart does not validate target ids — widget/widget.go's
// IsPredefinedWidgetTargetId is consulted on import only, and the clients mint
// ids of their own (chat and bin never appear in heart-side code at all). This
// list therefore labels what is recognised; it is not a whitelist.
var builtinWidgetTargets = map[string]string{
	"favorite":   "Favorites",
	"set":        "Sets",
	"collection": "Collections",
	"recent":     "Recently edited",
	"recentOpen": "Recently opened",
	"allObjects": "All objects",
	"chat":       "Chat",
	"bin":        "Bin",
}

// BuiltinWidgetTargets lists the known built-in target ids, for tool schemas.
func BuiltinWidgetTargets() []string {
	return []string{
		"favorite", "set", "collection", "recent",
		"recentOpen", "allObjects", "chat", "bin",
	}
}

// Widget status values. deleted is the one that matters: nothing in heart
// prunes a widget whose target was deleted, and no client shows it.
const (
	WidgetStatusOK      = "ok"
	WidgetStatusBin     = "bin"
	WidgetStatusDeleted = "deleted"
)

// SidebarWidget is one entry of the sidebar, in display order.
//
// A widget is two blocks: a wrapper carrying layout/limit/view and a link block
// inside it carrying the target. BlockWidgetSet* and a reorder address the
// wrapper; removing one goes through the inner block_id, which is what
// heart's UnlinkWithWrapper expects.
type SidebarWidget struct {
	Position   int    `json:"position"`
	WrapperID  string `json:"wrapper_id"`
	BlockID    string `json:"block_id"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name,omitempty"`
	Status     string `json:"status"`
	Builtin    bool   `json:"builtin,omitempty"`
	Layout     string `json:"layout"`
	Limit      int32  `json:"limit,omitempty"`
	ViewID     string `json:"view_id,omitempty"`
	AutoAdded  bool   `json:"auto_added,omitempty"`
}

// Sidebar is a widget object's contents. The counts cover every widget in the
// object, whatever the filter let through, so a trimmed listing still reveals
// that there is something to clean up.
type Sidebar struct {
	SpaceID  string          `json:"space_id"`
	ObjectID string          `json:"object_id"`
	Scope    string          `json:"scope"`
	Filter   string          `json:"filter"`
	Total    int             `json:"total"`
	OK       int             `json:"ok"`
	Bin      int             `json:"bin"`
	Deleted  int             `json:"deleted"`
	Shown    int             `json:"shown"`
	Widgets  []SidebarWidget `json:"widgets"`
	Orphans  []string        `json:"orphan_block_ids,omitempty"`
}

// SidebarFilterNames lists the accepted listing filters, for tool schemas.
func SidebarFilterNames() []string { return []string{"live", "deleted", "all"} }

// ReadSidebar reports the widgets of a widget object in display order.
//
// filter trims the listing (live, deleted, all) without touching the counts:
// a space whose sidebar is mostly dead widgets would otherwise cost kilobytes
// of useless rows, while hiding them outright would hide the problem.
func (c *Client) ReadSidebar(ctx context.Context, spaceID, objectID, scope, filter string) (Sidebar, error) {
	if strings.TrimSpace(spaceID) == "" {
		return Sidebar{}, errors.New("space_id is required")
	}
	if strings.TrimSpace(objectID) == "" {
		return Sidebar{}, errors.New("object_id is required")
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	resp, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	cancel()
	if err != nil {
		return Sidebar{}, fmt.Errorf("gRPC ObjectShow(widgets) failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return Sidebar{}, fmt.Errorf("ObjectShow(widgets) error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	view := resp.GetObjectView()

	blocks := make(map[string]*model.Block, len(view.GetBlocks()))
	for _, b := range view.GetBlocks() {
		blocks[b.GetId()] = b
	}
	root := blocks[view.GetRootId()]
	if root == nil {
		return Sidebar{}, fmt.Errorf("widget object %s has no root block", objectID)
	}

	names := detailNames(view.GetDetails())
	out := Sidebar{SpaceID: spaceID, ObjectID: objectID, Scope: scope, Filter: filter}

	var all []SidebarWidget
	for _, childID := range root.GetChildrenIds() {
		child := blocks[childID]
		if child == nil {
			out.Orphans = append(out.Orphans, childID)
			continue
		}
		wrapper, ok := child.Content.(*model.BlockContentOfWidget)
		if !ok {
			// The header and title blocks live here too; anything else that is
			// not a widget is reported rather than silently dropped.
			if _, isLayout := child.Content.(*model.BlockContentOfLayout); !isLayout && childID != "title" {
				out.Orphans = append(out.Orphans, childID)
			}
			continue
		}

		entry := SidebarWidget{
			Position:  len(all),
			WrapperID: childID,
			Layout:    enumName(widgetLayouts, wrapper.Widget.GetLayout()),
			Limit:     wrapper.Widget.GetLimit(),
			ViewID:    wrapper.Widget.GetViewId(),
			AutoAdded: wrapper.Widget.GetAutoAdded(),
		}
		for _, innerID := range child.GetChildrenIds() {
			inner := blocks[innerID]
			if inner == nil {
				continue
			}
			if link, ok := inner.Content.(*model.BlockContentOfLink); ok {
				entry.BlockID = innerID
				entry.TargetID = link.Link.GetTargetBlockId()
				break
			}
			// A widget wrapper is documented to hold a link; anything else is
			// worth surfacing rather than hiding behind an empty target.
			entry.BlockID = innerID
		}
		if label, ok := builtinWidgetTargets[entry.TargetID]; ok {
			entry.Builtin = true
			entry.TargetName = label
		} else if name, ok := names[entry.TargetID]; ok {
			entry.TargetName = name
		}
		entry.Status = WidgetStatusOK
		all = append(all, entry)
	}

	// Resolve what the ObjectShow payload did not name, and classify. A widget
	// whose target no longer exists is invisible in every client but still sits
	// in the sidebar object, so it has to be named, not dropped.
	var unresolved []string
	for _, w := range all {
		if !w.Builtin && w.TargetID != "" {
			unresolved = append(unresolved, w.TargetID)
		}
	}
	if len(unresolved) > 0 {
		resolved, err := c.resolveTargets(ctx, spaceID, unresolved)
		if err != nil {
			return Sidebar{}, err
		}
		for i := range all {
			w := &all[i]
			if w.Builtin || w.TargetID == "" {
				continue
			}
			target, ok := resolved[w.TargetID]
			if !ok {
				w.Status = WidgetStatusDeleted
				continue
			}
			if w.TargetName == "" {
				w.TargetName = target.Name
			}
			if target.Archived {
				w.Status = WidgetStatusBin
			}
		}
	}

	out.Total = len(all)
	for _, w := range all {
		switch w.Status {
		case WidgetStatusBin:
			out.Bin++
		case WidgetStatusDeleted:
			out.Deleted++
		default:
			out.OK++
		}
		switch filter {
		case "deleted":
			if w.Status != WidgetStatusDeleted {
				continue
			}
		case "all":
		default: // live
			if w.Status == WidgetStatusDeleted {
				continue
			}
		}
		out.Widgets = append(out.Widgets, w)
	}
	out.Shown = len(out.Widgets)
	return out, nil
}

// PruneSidebar removes widgets by their inner block id.
//
// BlockListDelete lands on WidgetObject.Unlink, which calls UnlinkWithWrapper:
// passing the inner link takes the wrapper with it, passing the wrapper alone
// would not. Hence block_id, not wrapper_id.
func (c *Client) PruneSidebar(ctx context.Context, objectID string, blockIDs []string) error {
	if strings.TrimSpace(objectID) == "" {
		return errors.New("object_id is required")
	}
	if len(blockIDs) == 0 {
		return errors.New("no blocks to remove")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockListDelete(callCtx, &pb.RpcBlockListDeleteRequest{
		ContextId: objectID, BlockIds: blockIDs,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockListDelete(widgets) failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockListDeleteResponseError_NULL {
		return fmt.Errorf("BlockListDelete(widgets) error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// detailNames indexes an ObjectShow details payload by object id.
func detailNames(sets []*model.ObjectViewDetailsSet) map[string]string {
	names := make(map[string]string, len(sets))
	for _, set := range sets {
		if set.GetId() == "" {
			continue
		}
		if name, _ := fromProtoValue(structField(set.GetDetails(), "name")).(string); name != "" {
			names[set.GetId()] = name
		}
	}
	return names
}

// widgetTarget is what a widget's target id resolves to.
type widgetTarget struct {
	Name     string
	Archived bool
}

// resolveTargets looks up widget targets, bin included.
//
// Two searches, for the reason ReadObjectFlags documents: heart appends
// "isArchived != true" to any search that does not mention isArchived itself
// (pkg/lib/database, addDefaultFilters), so one pass sees live objects only. A
// widget pointing into the bin is a real and common state — the sidebar keeps
// the widget when the object is binned — and reporting it as unresolvable
// would read as "object gone" when it is one restore away.
func (c *Client) resolveTargets(ctx context.Context, spaceID string, objectIDs []string) (map[string]widgetTarget, error) {
	values := make([]*types.Value, 0, len(objectIDs))
	for _, id := range objectIDs {
		values = append(values, &types.Value{Kind: &types.Value_StringValue{StringValue: id}})
	}
	idFilter := &model.BlockContentDataviewFilter{
		RelationKey: "id",
		Condition:   model.BlockContentDataviewFilter_In,
		Value:       &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: values}}},
	}
	archivedFilter := &model.BlockContentDataviewFilter{
		RelationKey: "isArchived",
		Condition:   model.BlockContentDataviewFilter_Equal,
		Value:       &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
	}

	found := make(map[string]widgetTarget, len(objectIDs))
	collect := func(filters []*model.BlockContentDataviewFilter) error {
		records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
			SpaceId: spaceID, Filters: filters,
			Keys: []string{"id", "name", "isArchived"},
		}, "the widget targets")
		if err != nil {
			return err
		}
		for _, record := range records {
			id, _ := fromProtoValue(structField(record, "id")).(string)
			if id == "" {
				continue
			}
			name, _ := fromProtoValue(structField(record, "name")).(string)
			archived, _ := fromProtoValue(structField(record, "isArchived")).(bool)
			found[id] = widgetTarget{Name: name, Archived: archived}
		}
		return nil
	}

	if err := collect([]*model.BlockContentDataviewFilter{idFilter}); err != nil {
		return nil, err
	}
	if err := collect([]*model.BlockContentDataviewFilter{idFilter, archivedFilter}); err != nil {
		return nil, err
	}
	return found, nil
}

// RemoveImportWidget drops the widget heart adds for an import's root
// collection, returning what it removed.
//
// The widget has to be found by diffing, because ObjectImport reports neither
// the collection id nor a completion: heart's middleware discards the result
// and the import runs asynchronously. So: remember the block ids beforehand,
// then watch for a new one. Waiting is bounded — a large import can finish
// after any sensible timeout, and a caller that gets "pending" can still run
// sidebar-prune later.
func (c *Client) RemoveImportWidget(
	ctx context.Context, spaceID, widgetsID string, known map[string]struct{}, wait time.Duration,
) ([]SidebarWidget, error) {
	deadline := time.Now().Add(wait)
	for {
		sidebar, err := c.ReadSidebar(ctx, spaceID, widgetsID, "space", "all")
		if err != nil {
			return nil, err
		}
		var fresh []SidebarWidget
		var blockIDs []string
		for _, w := range sidebar.Widgets {
			if _, seen := known[w.BlockID]; !seen {
				fresh = append(fresh, w)
				blockIDs = append(blockIDs, w.BlockID)
			}
		}
		if len(fresh) > 0 {
			if err := c.PruneSidebar(ctx, widgetsID, blockIDs); err != nil {
				return nil, err
			}
			return fresh, nil
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// AddWidgetSpec is one widget to create.
type AddWidgetSpec struct {
	TargetID string
	Layout   string
	Limit    int32
	ViewID   string
	Position string // top or bottom
}

// AddWidgets creates widgets and returns the sidebar afterwards, together with
// the targets that were already there.
//
// Idempotent on purpose: BlockCreateWidget does not check for a duplicate
// target (core/widget.go passes checkDuplicatedTarget=false), so asking twice
// would pin the same object twice. A caller that cannot tell whether it already
// ran is the normal case here, so an existing target is reported, not created.
func (c *Client) AddWidgets(
	ctx context.Context, spaceID, widgetsID string, specs []AddWidgetSpec,
) (added []SidebarWidget, present []string, err error) {
	before, err := c.ReadSidebar(ctx, spaceID, widgetsID, "space", "all")
	if err != nil {
		return nil, nil, err
	}
	known := make(map[string]struct{}, len(before.Widgets))
	targets := make(map[string]struct{}, len(before.Widgets))
	for _, w := range before.Widgets {
		known[w.BlockID] = struct{}{}
		targets[w.TargetID] = struct{}{}
	}

	var created bool
	for i, spec := range specs {
		if strings.TrimSpace(spec.TargetID) == "" {
			return nil, nil, fmt.Errorf("specs[%d]: target_id is required", i)
		}
		if _, exists := targets[spec.TargetID]; exists {
			present = append(present, spec.TargetID)
			continue
		}
		layout, err := lookupEnum(widgetLayouts, spec.Layout, "layout", true)
		if err != nil {
			return nil, nil, fmt.Errorf("specs[%d]: %w", i, err)
		}
		// An empty target in InsertTo means the root, and the root accepts only
		// inner insertions: Block_Top would silently become Block_Inner, i.e.
		// the bottom. InnerFirst is what "top" has to mean here.
		pos := model.Block_Inner
		if strings.EqualFold(strings.TrimSpace(spec.Position), "top") {
			pos = model.Block_InnerFirst
		}

		callCtx, cancel := c.contextWithAuth(ctx)
		resp, rpcErr := c.rpc.BlockCreateWidget(callCtx, &pb.RpcBlockCreateWidgetRequest{
			ContextId:    widgetsID,
			Position:     pos,
			WidgetLayout: layout,
			ObjectLimit:  spec.Limit,
			ViewId:       spec.ViewID,
			Block: &model.Block{Content: &model.BlockContentOfLink{
				Link: &model.BlockContentLink{TargetBlockId: spec.TargetID},
			}},
		})
		cancel()
		if rpcErr != nil {
			return nil, nil, fmt.Errorf("gRPC BlockCreateWidget failed: %w", rpcErr)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockCreateWidgetResponseError_NULL {
			return nil, nil, fmt.Errorf("BlockCreateWidget error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		targets[spec.TargetID] = struct{}{}
		created = true
	}

	if !created {
		return nil, present, nil
	}
	after, err := c.ReadSidebar(ctx, spaceID, widgetsID, "space", "all")
	if err != nil {
		return nil, present, err
	}
	// heart reports the wrapper id, not the inner link, and nothing reports the
	// inner one — so the new widgets are found by diffing block ids.
	for _, w := range after.Widgets {
		if _, seen := known[w.BlockID]; !seen {
			added = append(added, w)
		}
	}
	return added, present, nil
}

// UpdateWidget changes the presentation or the target of one widget. Only the
// non-nil fields are written; each is a separate RPC in heart.
func (c *Client) UpdateWidget(
	ctx context.Context, widgetsID, wrapperID string, layout, viewID, targetID *string, limit *int32,
) (changed []string, err error) {
	if strings.TrimSpace(wrapperID) == "" {
		return nil, errors.New("wrapper id is required")
	}
	if layout != nil {
		value, err := lookupEnum(widgetLayouts, *layout, "layout", false)
		if err != nil {
			return nil, err
		}
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, rpcErr := c.rpc.BlockWidgetSetLayout(callCtx, &pb.RpcBlockWidgetSetLayoutRequest{
			ContextId: widgetsID, BlockId: wrapperID, Layout: value,
		})
		cancel()
		if rpcErr != nil {
			return changed, fmt.Errorf("gRPC BlockWidgetSetLayout failed: %w", rpcErr)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockWidgetSetLayoutResponseError_NULL {
			return changed, fmt.Errorf("BlockWidgetSetLayout error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		changed = append(changed, "layout")
	}
	if limit != nil {
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, rpcErr := c.rpc.BlockWidgetSetLimit(callCtx, &pb.RpcBlockWidgetSetLimitRequest{
			ContextId: widgetsID, BlockId: wrapperID, Limit: *limit,
		})
		cancel()
		if rpcErr != nil {
			return changed, fmt.Errorf("gRPC BlockWidgetSetLimit failed: %w", rpcErr)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockWidgetSetLimitResponseError_NULL {
			return changed, fmt.Errorf("BlockWidgetSetLimit error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		changed = append(changed, "limit")
	}
	if viewID != nil {
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, rpcErr := c.rpc.BlockWidgetSetViewId(callCtx, &pb.RpcBlockWidgetSetViewIdRequest{
			ContextId: widgetsID, BlockId: wrapperID, ViewId: *viewID,
		})
		cancel()
		if rpcErr != nil {
			return changed, fmt.Errorf("gRPC BlockWidgetSetViewId failed: %w", rpcErr)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockWidgetSetViewIdResponseError_NULL {
			return changed, fmt.Errorf("BlockWidgetSetViewId error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		changed = append(changed, "view_id")
	}
	if targetID != nil {
		callCtx, cancel := c.contextWithAuth(ctx)
		resp, rpcErr := c.rpc.BlockWidgetSetTargetId(callCtx, &pb.RpcBlockWidgetSetTargetIdRequest{
			ContextId: widgetsID, BlockId: wrapperID, TargetId: *targetID,
		})
		cancel()
		if rpcErr != nil {
			return changed, fmt.Errorf("gRPC BlockWidgetSetTargetId failed: %w", rpcErr)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockWidgetSetTargetIdResponseError_NULL {
			return changed, fmt.Errorf("BlockWidgetSetTargetId error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		changed = append(changed, "target_id")
	}
	if len(changed) == 0 {
		return nil, errors.New("nothing to change: pass layout, limit, view_id or target_id")
	}
	return changed, nil
}

// ArrangeWidgets moves the named widgets to the top of the sidebar in the order
// given. Widgets not named keep their relative order below them, the same
// contract query-order-set uses, so a caller can reorder two widgets without
// having to list all of them.
func (c *Client) ArrangeWidgets(
	ctx context.Context, spaceID, widgetsID string, wrapperIDs []string,
) error {
	if len(wrapperIDs) == 0 {
		return errors.New("no widgets to arrange")
	}
	// The first one goes to the root as InnerFirst, because a widget has no
	// sibling to be placed above when it should become the first.
	if err := c.MoveBlocks(ctx, widgetsID, wrapperIDs[:1], "", widgetsID, "inner_first"); err != nil {
		return err
	}
	for i := 1; i < len(wrapperIDs); i++ {
		if err := c.MoveBlocks(ctx, widgetsID, wrapperIDs[i:i+1], "", wrapperIDs[i-1], "bottom"); err != nil {
			return err
		}
	}
	return nil
}
