package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Understanding and tidying the options of a select/multi-select property.
//
// Both tools rest on the same scan, which is built to be complete rather than
// quick: it covers live and archived objects, and the filters of every query
// and collection. Anything less would make the cleanup unsafe — an option that
// merely looks unused is an option about to be deleted while still in use.

func (s *mcpServer) usageToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "analyze-property-usage",
			"description": "Count how often each option of a select or multi-select property is actually used, so a schema can be understood without reading every object by hand. The scan covers objects in the bin as well as live ones, and also the filters of queries and collections, where an option can be referenced without any object holding it. Returns one entry per option with its usage count, plus totals.",
			"inputSchema": restSchema([]string{"space_id", "property_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property to analyse: the bafyrei... id from list-properties. Must be a select or multi_select property."),
				"type_keys": map[string]any{
					"type":        "array",
					"description": "Narrow the REPORTED counts to objects of these types, e.g. [\"recipe\"]. The scan itself always covers the whole space, so an option used elsewhere is still shown as used.",
					"items":       map[string]any{"type": "string"},
				},
				"only_unused":     map[string]any{"type": "boolean", "description": "Return only the options nothing references.", "default": false},
				"include_samples": map[string]any{"type": "boolean", "description": "Add a few example objects per option, useful for checking a surprising count.", "default": false},
			}),
		},
		map[string]any{
			"name":        "list-archived",
			"description": "List what is in the space's Bin. There is no other way to see it: the REST API keeps isArchived off its filterable properties, so the search tools cannot select archived objects and silently return live ones instead. Use this before permanently erasing anything, and to find an object that needs restoring with object-set-archived. Each entry reports created_by, the member who created the object — the same column the desktop bin shows.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"type_ids": map[string]any{
					"type":        "array",
					"description": "Restrict to these type ids, from list-types-compact.",
					"items":       map[string]any{"type": "string"},
				},
				"limit": map[string]any{"type": "integer", "description": "Maximum number of entries to return. Defaults to 100; the reported total is the true size of the bin.", "default": 100},
			}),
		},
		map[string]any{
			"name":        "analyze-schema-usage",
			"description": "Find the parts of a space's schema that nothing uses any more: types no object has, and properties no object fills in. Counts objects in the Bin and the filters, sorts and columns of every query, so a type or property that only some saved view depends on is not reported as dead. This is the same idea as analyze-property-usage, one level up: that one looks inside a property's options, this one looks at properties and types themselves.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"kind":        enumProp("What to examine. Defaults to both.", []string{"properties", "types", "both"}),
				"only_unused": map[string]any{"type": "boolean", "description": "Report only the entries nothing uses.", "default": false},
			}),
		},
		map[string]any{
			"name": "clean-unused-tags",
			"description": "Remove the options of a select or multi-select property that nothing uses — the tidy-up for a property that has accumulated dead entries.\n\n" +
				"Safety comes first here. An option is only proposed for removal when NOTHING references it: no live object, no object in the bin, and no filter of any query or collection in the space. type_keys narrows what is reported, never what is checked, so an option used by some other type is never removed.\n\n" +
				"Runs as a dry run by default and reports what it would do. Pass dry_run=false together with confirm=true to carry it out, and only after the user has agreed to the specific list. Removal archives the options the way delete-tag does; objects keep working and the options disappear from list-tags. Archiving is undoable: object-set-archived with archived=false and a tag id puts it back, and list-archived shows what is in the bin.",
			"inputSchema": restSchema([]string{"space_id", "property_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property to tidy: the bafyrei... id from list-properties."),
				"type_keys": map[string]any{
					"type":        "array",
					"description": "Only affects the report. The safety check always covers the entire space.",
					"items":       map[string]any{"type": "string"},
				},
				"dry_run": map[string]any{"type": "boolean", "description": "Report the plan without changing anything. Defaults to true.", "default": true},
				"confirm": map[string]any{"type": "boolean", "description": "Must be true, together with dry_run=false, to actually remove anything. Set it only after the user has seen and approved the list.", "default": false},
				"keep_tag_ids": map[string]any{
					"type":        "array",
					"description": "Options to keep even though nothing references them.",
					"items":       map[string]any{"type": "string"},
				},
				"limit": map[string]any{"type": "integer", "description": "Refuse to remove more than this many options in one go, as a brake against a surprise. Defaults to 200.", "default": 200},
				"ignore_bin": map[string]any{
					"type":        "boolean",
					"description": "Treat objects and queries in the bin as gone, so options only they reference count as unused. Off by default. Without it the bin pins options for good: anything ever used by a since-deleted object could never be tidied up. The dry run lists those separately under only_referenced_from_bin, so look there before switching this on. Removal stays reversible either way, since it archives the option rather than erasing it.",
					"default":     false,
				},
			}),
		},
	}
}

func (s *mcpServer) dispatchUsageTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "analyze-property-usage":
		res, err := s.toolAnalyzePropertyUsage(args)
		return res, err, true
	case "clean-unused-tags":
		res, err := s.toolCleanUnusedTags(args)
		return res, err, true
	case "list-archived":
		res, err := s.toolListArchived(args)
		return res, err, true
	case "analyze-schema-usage":
		res, err := s.toolAnalyzeSchemaUsage(args)
		return res, err, true
	}
	return nil, nil, false
}

// tagOption is one option of a property, as the REST API reports it.
type tagOption struct {
	ID    string
	Name  string
	Color string
}

// propertyFacts is everything the analysis needs about the property itself.
type propertyFacts struct {
	ID     string
	Key    string
	Name   string
	Format string
}

// loadProperty reads a property and refuses anything that has no options.
func (s *mcpServer) loadProperty(spaceID, propertyID string) (propertyFacts, error) {
	payload, err := s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties/"+url.PathEscape(propertyID), nil, nil)
	if err != nil {
		return propertyFacts{}, fmt.Errorf("could not read the property %s: %w", propertyID, err)
	}
	object, err := objectFromPayload(payload, "property")
	if err != nil {
		return propertyFacts{}, err
	}
	facts := propertyFacts{
		ID:     asString(object["id"]),
		Key:    asString(object["key"]),
		Name:   asString(object["name"]),
		Format: asString(object["format"]),
	}
	if facts.Key == "" {
		return propertyFacts{}, fmt.Errorf("the property %s has no key; pass an id from list-properties", propertyID)
	}
	if facts.Format != "select" && facts.Format != "multi_select" {
		return propertyFacts{}, fmt.Errorf(
			"%q is a %s property and has no options to analyse; only select and multi_select do",
			facts.Name, facts.Format)
	}
	return facts, nil
}

// loadTagOptions reads every option of a property, following pagination so a
// long list is never silently truncated — a missed option would be reported as
// nonexistent rather than unused.
func (s *mcpServer) loadTagOptions(spaceID, propertyID string) ([]tagOption, error) {
	var out []tagOption
	offset := 0
	for {
		query := url.Values{}
		query.Set("offset", fmt.Sprint(offset))
		query.Set("limit", "100")
		payload, err := s.anytypeAPIRequest(http.MethodGet,
			"/v1/spaces/"+url.PathEscape(spaceID)+"/properties/"+url.PathEscape(propertyID)+"/tags",
			query, nil)
		if err != nil {
			return nil, fmt.Errorf("could not list the options of %s: %w", propertyID, err)
		}
		data, _ := payload["data"].([]any)
		for _, raw := range data {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, tagOption{
				ID: asString(item["id"]), Name: asString(item["name"]), Color: asString(item["color"]),
			})
		}
		pagination, _ := payload["pagination"].(map[string]any)
		hasMore, _ := pagination["has_more"].(bool)
		if !hasMore || len(data) == 0 {
			break
		}
		offset += len(data)
	}
	return out, nil
}

// typeIDsForKeys resolves type keys to the type ids the index stores.
func (s *mcpServer) typeIDsForKeys(spaceID string, keys []string) (map[string]bool, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[strings.ToLower(strings.TrimSpace(key))] = true
	}
	query := url.Values{}
	query.Set("limit", "200")
	payload, err := s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/types", query, nil)
	if err != nil {
		return nil, fmt.Errorf("could not list types: %w", err)
	}
	data, _ := payload["data"].([]any)
	ids := make(map[string]bool)
	seen := make(map[string]bool)
	for _, raw := range data {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := strings.ToLower(asString(item["key"]))
		seen[key] = true
		if wanted[key] {
			ids[asString(item["id"])] = true
		}
	}
	for key := range wanted {
		if !seen[key] {
			return nil, fmt.Errorf("no type with the key %q exists in this space", key)
		}
	}
	return ids, nil
}

// optionReport is one line of the analysis.
type optionReport struct {
	Option       tagOption
	Objects      int
	Archived     int
	ViewRefs     int
	LiveViewRefs int
	InScope      int
	SampleName   []string
}

// analyse performs the scan shared by both tools.
func (s *mcpServer) analyse(spaceID, propertyID string, typeKeys []string) (propertyFacts, []optionReport, int, int, error) {
	facts, err := s.loadProperty(spaceID, propertyID)
	if err != nil {
		return propertyFacts{}, nil, 0, 0, err
	}
	options, err := s.loadTagOptions(spaceID, propertyID)
	if err != nil {
		return propertyFacts{}, nil, 0, 0, err
	}
	scopeTypes, err := s.typeIDsForKeys(spaceID, typeKeys)
	if err != nil {
		return propertyFacts{}, nil, 0, 0, err
	}

	client, err := s.grpcClient()
	if err != nil {
		return propertyFacts{}, nil, 0, 0, err
	}
	defer client.Close()

	usage, err := client.AnalysePropertyUsage(context.Background(), spaceID, facts.ID)
	if err != nil {
		return propertyFacts{}, nil, 0, 0, err
	}

	reports := make([]optionReport, 0, len(options))
	for _, option := range options {
		report := optionReport{
			Option:       option,
			ViewRefs:     usage.ViewRefs[option.ID],
			LiveViewRefs: usage.LiveViewRefs[option.ID],
		}
		for _, user := range usage.ByOption[option.ID] {
			report.Objects++
			if user.Archived {
				report.Archived++
			}
			if scopeTypes == nil || scopeTypes[user.TypeID] {
				report.InScope++
			}
			if len(report.SampleName) < 3 && user.Name != "" {
				report.SampleName = append(report.SampleName, user.Name)
			}
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Objects != reports[j].Objects {
			return reports[i].Objects > reports[j].Objects
		}
		return reports[i].Option.Name < reports[j].Option.Name
	})
	return facts, reports, usage.ScannedObjects, usage.ScannedViews, nil
}

// isUnused is the strict definition: nothing references it anywhere, bin
// included.
func (r optionReport) isUnused() bool { return r.Objects == 0 && r.ViewRefs == 0 }

// isUnusedIgnoringBin asks the same question with the bin discounted. The
// filters of LIVE queries still count, because a live query is not deleted
// content.
func (r optionReport) isUnusedIgnoringBin() bool {
	return r.Objects-r.Archived == 0 && r.LiveViewRefs == 0
}

func (r optionReport) isUnusedWith(ignoreBin bool) bool {
	if ignoreBin {
		return r.isUnusedIgnoringBin()
	}
	return r.isUnused()
}

func (s *mcpServer) toolAnalyzePropertyUsage(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return nil, err
	}
	facts, reports, scannedObjects, scannedViews, err := s.analyse(
		spaceID, propertyID, optionalStringSlice(args, "type_keys"))
	if err != nil {
		return nil, err
	}

	onlyUnused := optionalBool(args, "only_unused", false)
	includeSamples := optionalBool(args, "include_samples", false)
	used, unused := 0, 0
	entries := make([]map[string]any, 0, len(reports))
	for _, report := range reports {
		if report.isUnused() {
			unused++
		} else {
			used++
		}
		if onlyUnused && !report.isUnused() {
			continue
		}
		entry := map[string]any{
			"id": report.Option.ID, "name": report.Option.Name,
			"usage_count": report.Objects,
		}
		if report.Option.Color != "" {
			entry["color"] = report.Option.Color
		}
		if report.Archived > 0 {
			entry["archived_objects"] = report.Archived
		}
		if report.ViewRefs > 0 {
			entry["view_filter_references"] = report.ViewRefs
		}
		if len(optionalStringSlice(args, "type_keys")) > 0 && report.InScope != report.Objects {
			entry["usage_in_requested_types"] = report.InScope
		}
		if includeSamples && len(report.SampleName) > 0 {
			entry["sample_objects"] = report.SampleName
		}
		entries = append(entries, entry)
	}

	return map[string]any{
		"space_id": spaceID, "property_id": facts.ID,
		"property": facts.Name, "property_key": facts.Key, "format": facts.Format,
		"total_options": len(reports), "used_options": used, "unused_options": unused,
		"options":         entries,
		"scanned_objects": scannedObjects, "scanned_views": scannedViews,
		"scan_note": "counts include objects in the bin and references from query and collection filters",
	}, nil
}

func (s *mcpServer) toolCleanUnusedTags(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return nil, err
	}
	// Anything but an explicit dry_run=false stays a rehearsal.
	dryRun := true
	if raw, ok := args["dry_run"]; ok {
		if b, ok := raw.(bool); ok {
			dryRun = b
		}
	}
	confirmed, _ := args["confirm"].(bool)
	limit := optionalInt(args, "limit", 200)
	keep := make(map[string]bool)
	for _, id := range optionalStringSlice(args, "keep_tag_ids") {
		keep[id] = true
	}

	facts, reports, scannedObjects, scannedViews, err := s.analyse(
		spaceID, propertyID, optionalStringSlice(args, "type_keys"))
	if err != nil {
		return nil, err
	}

	ignoreBin, _ := args["ignore_bin"].(bool)

	removable := make([]map[string]any, 0)
	removableIDs := make([]any, 0)
	binOnly := make([]map[string]any, 0)
	kept := 0
	for _, report := range reports {
		// Options whose only trace is in the bin get their own list, so the
		// effect of ignore_bin is visible before anyone switches it on.
		if !ignoreBin && report.isUnusedIgnoringBin() && !report.isUnused() {
			binOnly = append(binOnly, map[string]any{
				"id": report.Option.ID, "name": report.Option.Name,
				"archived_objects": report.Archived,
			})
		}
		if !report.isUnusedWith(ignoreBin) {
			continue
		}
		if keep[report.Option.ID] {
			kept++
			continue
		}
		removable = append(removable, map[string]any{
			"id": report.Option.ID, "name": report.Option.Name,
		})
		removableIDs = append(removableIDs, report.Option.ID)
	}

	scanNote := "an option counts as unused only when no live object, no object in the bin and no query or collection filter references it"
	if ignoreBin {
		scanNote = "ignore_bin is on: only live objects and live queries were counted, so options referenced solely from the bin count as unused"
	}
	out := map[string]any{
		"space_id": spaceID, "property_id": facts.ID,
		"property": facts.Name, "format": facts.Format,
		"total_options": len(reports), "unused_options": len(removable),
		"kept_by_request": kept, "ignore_bin": ignoreBin,
		"scanned_objects": scannedObjects, "scanned_views": scannedViews,
		"scan_note":         scanNote,
		"options_to_remove": removable,
	}
	if len(binOnly) > 0 {
		out["only_referenced_from_bin"] = binOnly
		out["note_bin"] = fmt.Sprintf(
			"%d further option(s) are referenced only from the bin and were kept. Re-run with ignore_bin=true to remove those as well",
			len(binOnly))
	}

	if len(removable) == 0 {
		out["dry_run"] = dryRun
		out["removed"] = 0
		out["note"] = "nothing to remove: every option is referenced somewhere"
		return out, nil
	}
	if len(removable) > limit {
		return nil, fmt.Errorf(
			"refusing to touch %d options at once, which is over the limit of %d. "+
				"Check the analysis first, then raise limit deliberately if the number is right",
			len(removable), limit)
	}
	if dryRun {
		out["dry_run"] = true
		out["removed"] = 0
		out["note"] = "dry run: nothing was changed. Show this list to the user, and only then repeat with dry_run=false and confirm=true"
		return out, nil
	}
	if !confirmed {
		return nil, fmt.Errorf(
			"refusing to remove %d options: confirm must be true as well as dry_run=false, "+
				"and only after the user has seen the list from the dry run", len(removable))
	}

	// Removal goes through the ordinary delete-tag path, batch form, so the
	// behaviour is exactly what a manual cleanup would do.
	result, err, _ := s.dispatchRestTool("delete-tag", map[string]any{
		"space_id": spaceID, "property_id": propertyID, "tag_ids": removableIDs,
	})
	if err != nil {
		return nil, err
	}
	out["dry_run"] = false
	out["removed"] = result["succeeded"]
	out["failed"] = result["failed"]
	if failures, ok := result["failures"]; ok {
		out["failures"] = failures
	}
	return out, nil
}

func (s *mcpServer) toolListArchived(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	entries, err := client.ListArchived(context.Background(), spaceID,
		optionalStringSlice(args, "type_ids"))
	if err != nil {
		return nil, err
	}
	limit := optionalInt(args, "limit", 100)
	if limit <= 0 {
		limit = 100
	}
	shown := entries
	if len(shown) > limit {
		shown = shown[:limit]
	}
	out := make([]map[string]any, 0, len(shown))
	for _, entry := range shown {
		item := map[string]any{"object_id": entry.ObjectID}
		if entry.Name != "" {
			item["name"] = entry.Name
		}
		if entry.Snippet != "" {
			item["snippet"] = entry.Snippet
		}
		if entry.CreatedByID != "" {
			item["created_by_id"] = entry.CreatedByID
		}
		if entry.CreatedBy != "" {
			item["created_by"] = entry.CreatedBy
		}
		out = append(out, item)
	}
	result := map[string]any{
		"space_id": spaceID, "total": len(entries),
		"objects": out, "shown": len(out),
	}
	if len(entries) > len(shown) {
		result["note"] = fmt.Sprintf("showing %d of %d; raise limit to see more", len(shown), len(entries))
	}
	return result, nil
}

func (s *mcpServer) toolAnalyzeSchemaUsage(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	kind := optionalString(args, "kind")
	if kind == "" {
		kind = "both"
	}
	if kind != "properties" && kind != "types" && kind != "both" {
		return nil, fmt.Errorf("kind must be properties, types or both, got %q", kind)
	}
	onlyUnused := optionalBool(args, "only_unused", false)

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	ctx := context.Background()
	usage, err := client.AnalyseSchemaUsage(ctx, spaceID)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"space_id":        spaceID,
		"scanned_objects": usage.ScannedObjects, "scanned_views": usage.ScannedViews,
		"scan_note": "objects in the bin and the filters, sorts and columns of every query are counted as usage",
	}

	if kind == "properties" || kind == "both" {
		// The index keys values by the internal relation key, so the REST list
		// is joined to it through each property's own key.
		keys, err := client.LoadRelationKeyIndex(ctx, spaceID)
		if err != nil {
			return nil, err
		}
		properties, err := s.listAllProperties(spaceID)
		if err != nil {
			return nil, err
		}
		entries := make([]map[string]any, 0, len(properties))
		unused := 0
		for _, property := range properties {
			internal := keys[property.ID]
			if internal == "" {
				internal = property.Key
			}
			objects := usage.ObjectsByRelation[internal]
			views := usage.ViewRelations[internal]
			if objects == 0 && views == 0 {
				unused++
			} else if onlyUnused {
				continue
			}
			entry := map[string]any{
				"id": property.ID, "key": property.Key, "name": property.Name,
				"format": property.Format, "usage_count": objects,
			}
			if views > 0 {
				entry["view_references"] = views
			}
			entries = append(entries, entry)
		}
		out["properties"] = entries
		out["total_properties"] = len(properties)
		out["unused_properties"] = unused
	}

	if kind == "types" || kind == "both" {
		types, err := s.listAllTypes(spaceID)
		if err != nil {
			return nil, err
		}
		entries := make([]map[string]any, 0, len(types))
		unused := 0
		for _, t := range types {
			objects := usage.ObjectsByType[t.ID]
			sources := usage.TypesInViewSources[t.ID]
			if objects == 0 && sources == 0 {
				unused++
			} else if onlyUnused {
				continue
			}
			entry := map[string]any{
				"id": t.ID, "key": t.Key, "name": t.Name, "usage_count": objects,
			}
			if sources > 0 {
				entry["query_sources"] = sources
			}
			entries = append(entries, entry)
		}
		out["types"] = entries
		out["total_types"] = len(types)
		out["unused_types"] = unused
	}
	return out, nil
}

// schemaEntry is a property or type as the REST API lists it.
type schemaEntry struct {
	ID     string
	Key    string
	Name   string
	Format string
}

func (s *mcpServer) listAllProperties(spaceID string) ([]schemaEntry, error) {
	return s.listSchema(spaceID, "properties")
}

func (s *mcpServer) listAllTypes(spaceID string) ([]schemaEntry, error) {
	return s.listSchema(spaceID, "types")
}

// listSchema pages through a schema listing so nothing is cut off silently —
// a property missing from the list would simply never be reported at all.
func (s *mcpServer) listSchema(spaceID, kind string) ([]schemaEntry, error) {
	var out []schemaEntry
	offset := 0
	for {
		query := url.Values{}
		query.Set("offset", fmt.Sprint(offset))
		query.Set("limit", "100")
		payload, err := s.anytypeAPIRequest(http.MethodGet,
			"/v1/spaces/"+url.PathEscape(spaceID)+"/"+kind, query, nil)
		if err != nil {
			return nil, fmt.Errorf("could not list %s: %w", kind, err)
		}
		data, _ := payload["data"].([]any)
		for _, raw := range data {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, schemaEntry{
				ID: asString(item["id"]), Key: asString(item["key"]),
				Name: asString(item["name"]), Format: asString(item["format"]),
			})
		}
		pagination, _ := payload["pagination"].(map[string]any)
		hasMore, _ := pagination["has_more"].(bool)
		if !hasMore || len(data) == 0 {
			break
		}
		offset += len(data)
	}
	return out, nil
}
