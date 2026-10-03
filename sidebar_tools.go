package main

// Reading and cleaning the sidebar: a space's derived system objects, and the
// widgets that make up the sidebar itself.

import (
	"context"
	"fmt"
	"strings"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

func (s *mcpServer) sidebarToolDefs() []any {
	return []any{
		map[string]any{
			"name": "space-system-objects",
			"description": "Report a space's derived smartblock ids: widgets_id (the sidebar), home_id (favourites), archive_id (bin), space_chat_id, plus workspace, profile, space view and tech space. No other tool can reach them, so this comes first for anything sidebar-related.\n\n" +
				"Opens the space as a side effect; writes nothing.\n\n" +
				"personal_widgets_id is per account — the tech space of whichever account this server runs as, invisible to anyone using a desktop or mobile client.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
			}),
		},
		map[string]any{
			"name": "sidebar-inspect",
			"description": "List the widgets of a space's sidebar in display order, with target, layout, limit and view.\n\n" +
				"status per widget: ok, bin (target is in the bin), deleted (target no longer exists). **Deleted widgets are invisible in every Anytype client but still sit in the sidebar object, so a user does not know they are there** — the counts cover all of them even when the filter hides them, and sidebar-prune removes them.\n\n" +
				"block_id addresses a widget in every other sidebar tool.\n\n" +
				"builtin marks a target naming a built-in listing (" + strings.Join(anytypefiles.BuiltinWidgetTargets(), ", ") + ") instead of an object. Unknown target ids are reported as stored — the clients mint their own and heart does not validate them.\n\n" +
				"scope=space is what every member sees; scope=personal is this account only.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"scope":     enumProp("space (shared, default) or personal (this account only).", []string{"space", "personal"}),
				"filter":    enumProp("Which widgets to list: live (ok and bin, default), deleted, all. Counts are unaffected.", anytypefiles.SidebarFilterNames()),
				"object_id": strProp("Widget object to read instead of resolving one from scope."),
			}),
		},
		map[string]any{
			"name": "sidebar-widget-add",
			"description": "Pin objects to a space's sidebar — the GUI's \"Pin to channel\". Every member of the space sees them.\n\n" +
				"Idempotent: a target that already has a widget is reported under already_present instead of pinned twice (heart itself does not check).\n\n" +
				"A built-in listing (" + strings.Join(anytypefiles.BuiltinWidgetTargets(), ", ") + ") can be pinned as well by passing its name as target_id.\n\n" +
				"layout=view needs a view_id from get-list-views-compact and only makes sense for a query or collection.",
			"inputSchema": restSchema([]string{"space_id", "target_ids"}, map[string]any{
				"space_id": spaceIDProp(),
				"target_ids": map[string]any{
					"type":        "array",
					"description": "Objects to pin, or built-in listing names.",
					"items":       map[string]any{"type": "string"},
				},
				"layout":    enumProp("How the widget renders. Defaults to link (a single row); tree and compact_list are the usual choices for a page or collection.", anytypefiles.WidgetLayoutNames()),
				"limit":     map[string]any{"type": "integer", "description": "How many child objects a list or tree widget shows. heart's own default applies when omitted."},
				"view_id":   strProp("Required for layout=view: which view of the query to show."),
				"position":  enumProp("top or bottom of the sidebar. Defaults to bottom.", []string{"top", "bottom"}),
				"scope":     enumProp("space (shared, default) or personal (this account only).", []string{"space", "personal"}),
				"object_id": strProp("Widget object to write to instead of resolving one from scope."),
			}),
		},
		map[string]any{
			"name": "sidebar-widget-update",
			"description": "Change one sidebar widget: how it renders, how many items it shows, which view, or what it points at.\n\n" +
				"Only the fields passed are written. Changing target_id repoints an existing widget instead of removing and re-adding it, which keeps its position.",
			"inputSchema": restSchema([]string{"space_id", "block_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"block_id":  strProp("Widget to change, from sidebar-inspect."),
				"layout":    enumProp("New layout.", anytypefiles.WidgetLayoutNames()),
				"limit":     map[string]any{"type": "integer", "description": "New item limit. 0 restores heart's default."},
				"view_id":   strProp("New view, for layout=view."),
				"target_id": strProp("Point the widget at a different object or built-in listing."),
				"scope":     enumProp("space (shared, default) or personal (this account only).", []string{"space", "personal"}),
				"object_id": strProp("Widget object to write to instead of resolving one from scope."),
			}),
		},
		map[string]any{
			"name":        "sidebar-widget-remove",
			"description": "Unpin widgets from a space's sidebar. Removes only the sidebar entry; the objects themselves are untouched. Use sidebar-prune for widgets whose target is already gone.",
			"inputSchema": restSchema([]string{"space_id", "block_ids"}, map[string]any{
				"space_id": spaceIDProp(),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Widgets to remove, from sidebar-inspect.",
					"items":       map[string]any{"type": "string"},
				},
				"scope":     enumProp("space (shared, default) or personal (this account only).", []string{"space", "personal"}),
				"object_id": strProp("Widget object to write to instead of resolving one from scope."),
			}),
		},
		map[string]any{
			"name":        "sidebar-arrange",
			"description": "Reorder a space's sidebar: the widgets named move to the top in the order given, the rest keep their relative order below. Listing all of them is therefore optional.",
			"inputSchema": restSchema([]string{"space_id", "block_ids"}, map[string]any{
				"space_id": spaceIDProp(),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Widgets in the order wanted, from sidebar-inspect.",
					"items":       map[string]any{"type": "string"},
				},
				"scope":     enumProp("space (shared, default) or personal (this account only).", []string{"space", "personal"}),
				"object_id": strProp("Widget object to write to instead of resolving one from scope."),
			}),
		},
		map[string]any{
			"name": "sidebar-prune",
			"description": "Remove every widget of a space's sidebar whose target no longer exists (status=deleted in sidebar-inspect).\n\n" +
				"Dry run unless apply=true. Widgets pointing into the bin are left alone — restoring the object makes them work again.\n\n" +
				"They accumulate because every import adds a widget for its collection and deleting an object never removes its widget, so no client ever shows them.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"scope":     enumProp("space (shared, default) or personal (this account only).", []string{"space", "personal"}),
				"object_id": strProp("Widget object to clean instead of resolving one from scope."),
				"apply":     map[string]any{"type": "boolean", "description": "true performs the removal. Default false reports only.", "default": false},
			}),
		},
	}
}

func (s *mcpServer) dispatchSidebarTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "space-system-objects":
		res, err := s.toolSystemObjects(args)
		return res, err, true
	case "sidebar-inspect":
		res, err := s.toolSidebarInspect(args)
		return res, err, true
	case "sidebar-prune":
		res, err := s.toolSidebarPrune(args)
		return res, err, true
	case "sidebar-widget-add":
		res, err := s.toolSidebarAdd(args)
		return res, err, true
	case "sidebar-widget-update":
		res, err := s.toolSidebarUpdate(args)
		return res, err, true
	case "sidebar-widget-remove":
		res, err := s.toolSidebarRemove(args)
		return res, err, true
	case "sidebar-arrange":
		res, err := s.toolSidebarArrange(args)
		return res, err, true
	}
	return nil, nil, false
}

func (s *mcpServer) toolSystemObjects(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	objects, err := client.ReadSystemObjects(context.Background(), spaceID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id":            objects.SpaceID,
		"widgets_id":          objects.WidgetsID,
		"home_id":             objects.HomeID,
		"archive_id":          objects.ArchiveID,
		"profile_id":          objects.ProfileID,
		"workspace_object_id": objects.WorkspaceObjectID,
		"space_chat_id":       objects.SpaceChatID,
		"space_view_id":       objects.SpaceViewID,
		"tech_space_id":       objects.TechSpaceID,
		"account_space_id":    objects.AccountSpaceID,
		"personal_widgets_id": objects.PersonalWidgetsID,
	}, nil
}

// widgetObjectID resolves the widget object a sidebar tool should work on,
// shared by the read and the prune path so they cannot disagree about it.
func widgetObjectID(ctx context.Context, client *anytypefiles.Client, args map[string]any, spaceID string) (objectID, scope string, err error) {
	scope = optionalString(args, "scope")
	if scope == "" {
		scope = "space"
	}
	objectID = optionalString(args, "object_id")
	if objectID != "" {
		return objectID, scope, nil
	}
	switch scope {
	case "space":
		objects, err := client.ReadSystemObjects(ctx, spaceID)
		if err != nil {
			return "", scope, err
		}
		if objects.WidgetsID == "" {
			return "", scope, fmt.Errorf("the space reports no widget object; WorkspaceOpen answered without widgetsId")
		}
		return objects.WidgetsID, scope, nil
	case "personal":
		return anytypefiles.PersonalWidgetsID(spaceID), scope, nil
	default:
		return "", scope, fmt.Errorf("unknown scope %q; use space or personal", scope)
	}
}

func sidebarWidgetMap(w anytypefiles.SidebarWidget) map[string]any {
	entry := map[string]any{
		"position":    w.Position,
		"block_id":    w.BlockID,
		"target_id":   w.TargetID,
		"target_name": w.TargetName,
		"status":      w.Status,
		"layout":      w.Layout,
	}
	if w.Builtin {
		entry["builtin"] = true
	}
	if w.Limit != 0 {
		entry["limit"] = w.Limit
	}
	if w.ViewID != "" {
		entry["view_id"] = w.ViewID
	}
	if w.AutoAdded {
		entry["auto_added"] = true
	}
	return entry
}

func (s *mcpServer) toolSidebarInspect(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	filter := optionalString(args, "filter")
	if filter == "" {
		filter = "live"
	}
	switch filter {
	case "live", "deleted", "all":
	default:
		return nil, fmt.Errorf("unknown filter %q; use live, deleted or all", filter)
	}

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx := context.Background()

	objectID, scope, err := widgetObjectID(ctx, client, args, spaceID)
	if err != nil {
		return nil, err
	}
	sidebar, err := client.ReadSidebar(ctx, spaceID, objectID, scope, filter)
	if err != nil {
		return nil, err
	}
	widgets := make([]map[string]any, 0, len(sidebar.Widgets))
	for _, w := range sidebar.Widgets {
		widgets = append(widgets, sidebarWidgetMap(w))
	}
	out := map[string]any{
		"space_id":  sidebar.SpaceID,
		"object_id": sidebar.ObjectID,
		"scope":     sidebar.Scope,
		"filter":    sidebar.Filter,
		"total":     sidebar.Total,
		"ok":        sidebar.OK,
		"bin":       sidebar.Bin,
		"deleted":   sidebar.Deleted,
		"shown":     sidebar.Shown,
		"widgets":   widgets,
	}
	if len(sidebar.Orphans) > 0 {
		out["orphan_block_ids"] = sidebar.Orphans
	}
	return out, nil
}

func (s *mcpServer) toolSidebarPrune(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	apply := optionalBool(args, "apply", false)

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx := context.Background()

	objectID, scope, err := widgetObjectID(ctx, client, args, spaceID)
	if err != nil {
		return nil, err
	}
	before, err := client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
	if err != nil {
		return nil, err
	}

	var targets []anytypefiles.SidebarWidget
	blockIDs := make([]string, 0, before.Deleted)
	for _, w := range before.Widgets {
		if w.Status == anytypefiles.WidgetStatusDeleted {
			targets = append(targets, w)
			blockIDs = append(blockIDs, w.BlockID)
		}
	}

	removing := make([]map[string]any, 0, len(targets))
	for _, w := range targets {
		removing = append(removing, sidebarWidgetMap(w))
	}
	out := map[string]any{
		"space_id":  spaceID,
		"object_id": objectID,
		"scope":     scope,
		"total":     before.Total,
		"deleted":   before.Deleted,
		"widgets":   removing,
		"applied":   false,
	}
	if len(targets) == 0 || !apply {
		return out, nil
	}
	after, err := s.removeWidgets(ctx, client, spaceID, objectID, scope, blockIDs)
	if err != nil {
		return nil, err
	}
	out["applied"] = true
	out["removed"] = before.Total - after.Total
	out["total_after"] = after.Total
	out["deleted_after"] = after.Deleted
	if len(after.Orphans) > 0 {
		out["orphan_block_ids"] = after.Orphans
	}
	return out, nil
}

// removeWidgets unlinks widgets and reads the sidebar back. Shared by prune and
// remove so the empty-wrapper check cannot exist in only one of them.
func (s *mcpServer) removeWidgets(
	ctx context.Context, client *anytypefiles.Client, spaceID, objectID, scope string, blockIDs []string,
) (anytypefiles.Sidebar, error) {
	if err := client.PruneSidebar(ctx, objectID, blockIDs); err != nil {
		return anytypefiles.Sidebar{}, err
	}
	return client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
}

// sidebarWriteTarget resolves the widget object plus the client for a write.
func (s *mcpServer) sidebarWriteTarget(args map[string]any) (*anytypefiles.Client, context.Context, string, string, string, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, nil, "", "", "", err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, nil, "", "", "", err
	}
	ctx := context.Background()
	objectID, scope, err := widgetObjectID(ctx, client, args, spaceID)
	if err != nil {
		client.Close()
		return nil, nil, "", "", "", err
	}
	return client, ctx, spaceID, objectID, scope, nil
}

func (s *mcpServer) toolSidebarAdd(args map[string]any) (map[string]any, error) {
	targets := optionalStringSlice(args, "target_ids")
	if len(targets) == 0 {
		return nil, fmt.Errorf("target_ids is required")
	}
	client, ctx, spaceID, objectID, scope, err := s.sidebarWriteTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	layout := optionalString(args, "layout")
	limit := int32(optionalInt(args, "limit", 0))
	viewID := optionalString(args, "view_id")
	position := optionalString(args, "position")
	if layout == "view" && viewID == "" {
		return nil, fmt.Errorf("layout=view needs a view_id; read one with get-list-views-compact")
	}

	specs := make([]anytypefiles.AddWidgetSpec, 0, len(targets))
	for _, target := range targets {
		specs = append(specs, anytypefiles.AddWidgetSpec{
			TargetID: target, Layout: layout, Limit: limit,
			ViewID: viewID, Position: position,
		})
	}
	added, present, err := client.AddWidgets(ctx, spaceID, objectID, specs)
	if err != nil {
		return nil, err
	}
	widgets := make([]map[string]any, 0, len(added))
	for _, w := range added {
		widgets = append(widgets, sidebarWidgetMap(w))
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID, "scope": scope,
		"added": len(added), "widgets": widgets,
	}
	if len(present) > 0 {
		out["already_present"] = present
	}
	return out, nil
}

func (s *mcpServer) toolSidebarUpdate(args map[string]any) (map[string]any, error) {
	blockID, err := requiredString(args, "block_id")
	if err != nil {
		return nil, err
	}
	client, ctx, spaceID, objectID, scope, err := s.sidebarWriteTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	sidebar, err := client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
	if err != nil {
		return nil, err
	}
	var widget *anytypefiles.SidebarWidget
	for i := range sidebar.Widgets {
		if sidebar.Widgets[i].BlockID == blockID {
			widget = &sidebar.Widgets[i]
			break
		}
	}
	if widget == nil {
		return nil, fmt.Errorf("block_id %s is not a widget of %s", blockID, objectID)
	}

	// The presentation RPCs address the wrapper, which a caller never sees —
	// resolving it here keeps one id in the interface.
	var layout, viewID, targetID *string
	var limit *int32
	if v := optionalString(args, "layout"); v != "" {
		layout = &v
	}
	if raw, ok := args["view_id"]; ok && raw != nil {
		v := optionalString(args, "view_id")
		viewID = &v
	}
	if v := optionalString(args, "target_id"); v != "" {
		targetID = &v
	}
	if raw, ok := args["limit"]; ok && raw != nil {
		v := int32(optionalInt(args, "limit", 0))
		limit = &v
	}

	changed, err := client.UpdateWidget(ctx, objectID, widget.WrapperID, layout, viewID, targetID, limit)
	if err != nil {
		return nil, err
	}
	after, err := client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID, "scope": scope,
		"block_id": blockID, "changed": changed,
	}
	for _, w := range after.Widgets {
		// A repointed widget keeps its block id, so this still finds it.
		if w.BlockID == blockID {
			out["widget"] = sidebarWidgetMap(w)
			break
		}
	}
	return out, nil
}

func (s *mcpServer) toolSidebarRemove(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, ctx, spaceID, objectID, scope, err := s.sidebarWriteTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	before, err := client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(before.Widgets))
	for _, w := range before.Widgets {
		known[w.BlockID] = struct{}{}
	}
	// An id that is not in this sidebar is a mistake worth reporting, not a
	// silent no-op — most likely a wrapper id or a stale listing.
	for _, id := range blockIDs {
		if _, ok := known[id]; !ok {
			return nil, fmt.Errorf("block_id %s is not a widget of %s", id, objectID)
		}
	}
	after, err := s.removeWidgets(ctx, client, spaceID, objectID, scope, blockIDs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID, "scope": scope,
		"removed": before.Total - after.Total, "total_after": after.Total,
	}
	if len(after.Orphans) > 0 {
		out["orphan_block_ids"] = after.Orphans
	}
	return out, nil
}

func (s *mcpServer) toolSidebarArrange(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, ctx, spaceID, objectID, scope, err := s.sidebarWriteTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	before, err := client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
	if err != nil {
		return nil, err
	}
	wrappers := make([]string, 0, len(blockIDs))
	for _, id := range blockIDs {
		found := false
		for _, w := range before.Widgets {
			if w.BlockID == id {
				wrappers = append(wrappers, w.WrapperID)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("block_id %s is not a widget of %s", id, objectID)
		}
	}
	if err := client.ArrangeWidgets(ctx, spaceID, objectID, wrappers); err != nil {
		return nil, err
	}
	after, err := client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
	if err != nil {
		return nil, err
	}
	order := make([]map[string]any, 0, len(after.Widgets))
	for _, w := range after.Widgets {
		order = append(order, map[string]any{
			"position": w.Position, "block_id": w.BlockID,
			"target_name": w.TargetName, "status": w.Status,
		})
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "scope": scope,
		"arranged": len(wrappers), "order": order,
	}, nil
}
