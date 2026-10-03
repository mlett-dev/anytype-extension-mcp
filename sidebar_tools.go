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
				"block_id is the inner link, wrapper_id the block carrying the presentation; removals go through block_id.\n\n" +
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
			"name": "sidebar-prune",
			"description": "Remove widgets whose target no longer exists (status=deleted in sidebar-inspect) from a space's sidebar.\n\n" +
				"Defaults to a dry run: it reports what it would remove and changes nothing until apply=true. Widgets pointing into the bin are never touched — restoring the object makes them work again.\n\n" +
				"Why they accumulate: every import adds a widget for its root collection (heart's addRootCollectionWidget), and deleting an object does not remove its widget. Pass block_ids to remove specific widgets instead of every dead one.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"scope":     enumProp("space (shared, default) or personal (this account only).", []string{"space", "personal"}),
				"object_id": strProp("Widget object to clean instead of resolving one from scope."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Inner block ids from sidebar-inspect. Omit to remove every widget with status=deleted.",
					"items":       map[string]any{"type": "string"},
				},
				"apply": map[string]any{"type": "boolean", "description": "true performs the removal. Default false reports only.", "default": false},
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
		"wrapper_id":  w.WrapperID,
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
	requested := optionalStringSlice(args, "block_ids")
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

	byBlock := make(map[string]anytypefiles.SidebarWidget, len(before.Widgets))
	for _, w := range before.Widgets {
		byBlock[w.BlockID] = w
	}

	var targets []anytypefiles.SidebarWidget
	if len(requested) > 0 {
		// An explicit id is honoured whatever its status, but an id that is not
		// in this sidebar is an error rather than a silent no-op.
		for _, id := range requested {
			w, ok := byBlock[id]
			if !ok {
				return nil, fmt.Errorf("block_id %s is not a widget of %s", id, objectID)
			}
			targets = append(targets, w)
		}
	} else {
		for _, w := range before.Widgets {
			if w.Status == anytypefiles.WidgetStatusDeleted {
				targets = append(targets, w)
			}
		}
	}

	removing := make([]map[string]any, 0, len(targets))
	blockIDs := make([]string, 0, len(targets))
	for _, w := range targets {
		removing = append(removing, sidebarWidgetMap(w))
		blockIDs = append(blockIDs, w.BlockID)
	}
	out := map[string]any{
		"space_id":  spaceID,
		"object_id": objectID,
		"scope":     scope,
		"total":     before.Total,
		"deleted":   before.Deleted,
		"selected":  len(targets),
		"widgets":   removing,
		"applied":   false,
	}
	if len(targets) == 0 || !apply {
		return out, nil
	}

	if err := client.PruneSidebar(ctx, objectID, blockIDs); err != nil {
		return nil, err
	}
	after, err := client.ReadSidebar(ctx, spaceID, objectID, scope, "all")
	if err != nil {
		return nil, err
	}
	out["applied"] = true
	out["total_after"] = after.Total
	out["deleted_after"] = after.Deleted
	out["removed"] = before.Total - after.Total
	// Empty wrappers would show up here, which is the failure mode of removing
	// the wrong block id.
	if len(after.Orphans) > 0 {
		out["orphan_block_ids"] = after.Orphans
	}
	return out, nil
}
