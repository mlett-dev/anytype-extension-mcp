package main

import (
	"context"
	"fmt"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// Query (set) and collection tools, backed by the anytype-heart gRPC API.
//
// The public REST API can create a query object but cannot give it a source,
// views, filters or sorts — those live in the object's dataview block, which is
// only reachable over gRPC. These tools close that gap so a query can be built
// and reconfigured exactly as in the GUI.
//
// Filters and sorts reference properties by *key* (property_key, e.g. "done"),
// not by the bafyrei... id that REST path parameters need. Use list-properties
// to get both.

// relationKeySpellingNote explains, in the tool descriptions themselves, the
// one thing about property keys a caller can otherwise only discover by being
// confused: Anytype keeps two spellings, and the readback uses the other one.
const relationKeySpellingNote = "Both spellings of a property key work: the one list-properties reports (due_date, linked_projects) and Anytype's internal one (dueDate, linkedProjects). They are translated automatically. Anytype stores and reports back the internal spelling, so a key can come back looking different from the one you sent — that is the translation working, not a failed change. For a property created through the API or in the app the internal spelling is a generated id such as 6a87593ad7f55319fc7b1d73, which no REST endpoint reports at all — query-inspect reports it next to the public one as api_property_key. Pass the key from list-properties and let it be translated. A key that exists in neither spelling is refused, rather than being stored as a reference to a property that does not exist."

// filterFormatNote explains why this enum is wider than the one the REST tools
// offer, so a caller is not left wondering which of two names for the same
// format to use.
const filterFormatNote = "A dataview stores its own spelling of a format, and query-inspect reports that one: object, status, longtext and tag where list-properties says objects, select, text and multi_select. Both spellings are accepted here, so a filter that was just read can be written straight back. Where the two differ they mean the same thing, with one exception: text and longtext are genuinely different formats, short and long, and are not interchangeable."

const queryFilterValueDescription = "Filter value for the property's format: true/false for checkbox, a number for number, a string for text/url/email/phone, Unix seconds or an ISO 8601 date for date (2026-10-25 is midnight UTC; with quick_option number_of_days_ago/now the number of days), tag ids or names for select/multi_select, object ids for object properties. A single id needs no list. Omit for empty and not_empty. A filter without a value filters nothing in the app and is refused.\n\n" + selfFilterNote

// filterConditionNote names the app's labels for the conditions, so a caller
// asked for "Contains any" or "Doesn't contain" does not have to guess. The
// app shows the same "Doesn't contain" for not_in and not_like.
const filterConditionNote = "Filter condition. In the app: in = Contains any, all_in = Contains all, not_in = Doesn't contain (select, multi_select, objects); like = Contains, not_like = Doesn't contain (text); equal = Is, not_equal = Is not; for dates less = before, greater = after, less_or_equal/greater_or_equal = on or before/after, in = Is within (with a quick_option period)."

const filterQuickOptionNote = "Relative date, as the app's Today, This week or Number of days ago: the filter follows the calendar. Periods (today … next_year) need no value; number_of_days_ago/now take the number of days as value. Date properties only."

// filterGroupNote describes the app's advanced filter.
const filterGroupNote = "Without a group the filters of a view combine with AND. For OR, add a group, the app's advanced filter: operator and/or with the rules in nested_filters and no property_key or condition of its own. A view holds one group."

const filterFormatShortNote = "Optional: taken from the property. If given it must fit the property's format. " + filterFormatNote

// selfFilterNote documents the one filter value that is not a value at all.
//
// The Anytype apps offer "This Object" as a filter value and store it as the
// literal string below — verified by building the filter in the app and reading
// the object back: an inline Tasks query filtered on Project = This Object
// stores {relationKey: linkedProjects, condition: In, value:
// ["_filter_template_1_"], format: object}. Writing the same string through
// these tools produces a byte-identical filter, so this needs no support code,
// only the knowledge that the constant exists.
const selfFilterNote = "SELF-REFERENCE (\"This Object\"): to make a filter point at whichever object the query is being displayed in, pass the literal string \"_filter_template_1_\" as the value, with condition=in and format=objects. " +
	"This is what a template's inline query needs: a Project template holding a Tasks query filtered on Project = _filter_template_1_ works for every project created from it, while a real object id would pin every one of them to that same project. " +
	"Two things to know. It is an undocumented constant of the Anytype apps, matched here to what the app itself writes, so it could change upstream. And only the apps resolve it: anytype-heart stores it as an ordinary string, so get-list-objects-compact on such a view returns nothing at all — that is the expected result of reading it server-side, not a broken filter. Verify it in the app instead."

func (s *mcpServer) grpcClient() (*anytypefiles.Client, error) {
	return anytypefiles.NewClient(context.Background(), anytypefiles.Config{
		GRPCAddress:  s.cfg.grpcAddr,
		SessionToken: s.cfg.token,
		Timeout:      s.cfg.timeout,
	})
}

// resolveDataviewBlock returns the dataview block id, discovering it when the
// caller did not pass one.
func resolveDataviewBlock(client *anytypefiles.Client, spaceID, objectID, given string) (string, error) {
	if given != "" {
		return given, nil
	}
	info, err := client.InspectDataview(context.Background(), spaceID, objectID, "")
	if err != nil {
		return "", err
	}
	return info.BlockID, nil
}

// filterRuleProps are the fields of one filter. The rules inside a group take
// the same fields with short descriptions: the full texts are already in the
// schema once, and every copy costs context in each request.
func filterRuleProps() map[string]any {
	return map[string]any{
		"property_key": strProp("Property key to filter on, e.g. \"done\" or \"due_date\". This is the key from list-properties, not the bafyrei... id. " + relationKeySpellingNote),
		"condition":    enumProp(filterConditionNote, anytypefiles.FilterConditionNames()),
		"value":        map[string]any{"description": queryFilterValueDescription},
		"format":       enumProp(filterFormatShortNote, anytypefiles.FilterFormatNames()),
		"quick_option": enumProp(filterQuickOptionNote, anytypefiles.FilterQuickOptionNames()),
		"operator":     enumProp("Makes this entry a group of nested_filters, combined with and or or.", []string{"and", "or"}),
	}
}

// nestedRuleSchema is one rule inside a group; a rule can be a group itself,
// and the server checks every level.
func nestedRuleSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"property_key":   strProp("Property key."),
			"condition":      enumProp("Filter condition.", anytypefiles.FilterConditionNames()),
			"value":          map[string]any{"description": "Value, as for a single filter."},
			"quick_option":   enumProp("Relative date.", anytypefiles.FilterQuickOptionNames()),
			"operator":       enumProp("Makes this rule a nested group.", []string{"and", "or"}),
			"nested_filters": map[string]any{"type": "array", "description": "Rules of a nested group.", "items": map[string]any{"type": "object"}},
		},
	}
}

func filterSpecSchema() map[string]any {
	props := filterRuleProps()
	props["id"] = strProp("Id of a stored filter, from query-inspect. Keep it when writing a list back, so the filter keeps its id.")
	props["nested_filters"] = map[string]any{
		"type":        "array",
		"description": "The rules of a group (operator and/or).",
		"items":       nestedRuleSchema(),
	}
	return map[string]any{"type": "object", "properties": props}
}

func sortSpecSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":              strProp("Id of a stored sort, from query-inspect. Keep it when writing a list back."),
			"property_key":    strProp("Property key to sort by. " + relationKeySpellingNote),
			"direction":       enumProp("Sort direction. A hand-made row order is query-order-set.", []string{"asc", "desc"}),
			"format":          enumProp("Optional: taken from the property.", anytypefiles.FilterFormatNames()),
			"include_time":    map[string]any{"type": "boolean", "description": "For date sorts: take time of day into account.", "default": false},
			"empty_placement": enumProp("Where objects with an empty value go.", []string{"not_specified", "start", "end"}),
		},
		"required": []any{"property_key"},
	}
}

func relationSpecSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"property_key": strProp("Property key shown as a column. " + relationKeySpellingNote),
			"visible":      map[string]any{"type": "boolean", "description": "Whether the column is shown. Listing a property here normally means you want it visible.", "default": true},
			"width":        map[string]any{"type": "integer", "description": "Column width in pixels. Omit it to keep the width the column already has."},
		},
		"required": []any{"property_key"},
	}
}

func viewConfigProps() map[string]any {
	return map[string]any{
		"name":   strProp("View name."),
		"layout": enumProp("View layout. The REST API reports the table layout as \"grid\". A new view without a layout is a table; on an update, omitting it keeps the layout the view has.", anytypefiles.ViewLayoutNames()),
		"filters": map[string]any{
			"type":        "array",
			"description": "Filters of the view. " + filterGroupNote,
			"items":       filterSpecSchema(),
		},
		"sorts": map[string]any{
			"type":        "array",
			"description": "Sorts of the view, applied in order.",
			"items":       sortSpecSchema(),
		},
		"relations": map[string]any{
			"type":        "array",
			"description": "The VISIBLE columns of the view, in the order they should appear. On update this list replaces the visible set: listed properties are shown, every other one is hidden, so include every column you want to keep. Omit the field entirely to leave columns alone; a column listed without a width keeps its current width. Note a view always keeps an entry for every known property — query-inspect reports them all, and \"visible\" tells you which are actually columns.",
			"items":       relationSpecSchema(),
		},
		"group_property_key": strProp("Property to group by. Required for a kanban view, and it must be a select, multi-select or checkbox property — anytype cannot group a board by an object, text, number or date property, and one that it cannot group renders as an empty board. " + relationKeySpellingNote),
		"cover_property_key": strProp("Property used as the cover image in gallery views."),
		"page_limit":         map[string]any{"type": "integer", "description": "Maximum number of objects shown."},
		// No schema default here on purpose. A client that materialises declared
		// defaults into the arguments would make every update look like a caller
		// asking for hide_icon=false, and the presence of the key is exactly what
		// decides whether the stored value is overwritten.
		"hide_icon":              map[string]any{"type": "boolean", "description": "Hide object icons in this view. A new view shows them; on an update, omitting this keeps whatever the view has."},
		"card_size":              enumProp("Card size for gallery views.", []string{"small", "medium", "large"}),
		"default_object_type_id": strProp("Type id used when creating an object from this view."),
		"default_template_id":    strProp("Template id used when creating an object from this view."),
	}
}

func (s *mcpServer) queryToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "query-create",
			"description": "Create an Anytype Query (a set): a saved, self-updating view over a type. Requires at least one source type id, because a query without a source returns nothing. Returns the new object id together with its dataview block and default view, so filters and sorts can be added right away. For a hand-curated list use a collection instead.",
			"inputSchema": restSchema([]string{"space_id", "source"}, map[string]any{
				"space_id": spaceIDProp(),
				"name":     strProp("Name of the query."),
				"source": map[string]any{
					"type":        "array",
					"description": "Type ids the query draws from (bafyrei... ids from list-types-compact, not type keys).",
					"items":       map[string]any{"type": "string"},
				},
				"icon_emoji": strProp("Optional emoji icon."),
			}),
		},
		map[string]any{
			"name": "query-inspect",
			"description": "Read the internal structure of a query, a collection, or a page holding an embedded query: the dataview block id, the source types, and every view with its filters, sorts and columns including their ids, the view's default type and template, plus any manual row order under object_orders. Call this before changing anything — filter and sort ids come from here, and they are not visible through the REST tools. Property keys are reported in Anytype's internal spelling, which for some properties differs from the one list-properties shows: due_date appears as dueDate, linked_projects as linkedProjects, and a property created in the app as something like 6a83275740bea100015faae7. Where the two differ, api_property_key carries the spelling list-properties uses, so a key can be looked up without guessing; where it is absent the two coincide and property_key is already the public one. Both spellings are accepted as input.\n\n" +
				"On a page with an embedded query, target_object_id names the query being displayed and the reported source is read from it. dataview_blocks lists every dataview block of the object: a page can hold several embedded queries, and without block_id this reads the first one. Anything stored that cannot work — a filter written with an operator, more blocks than the one being reported — comes back under warnings.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id, or a page that embeds one."),
				"block_id":  strProp("Dataview block id, from dataview_blocks or block-list. Optional; defaults to the object's first dataview block."),
			}),
		},
		map[string]any{
			"name":        "query-set-source",
			"description": "Replace the source types of a query. This is what decides which objects the query can return; it cannot be set through the REST API.\n\nAnytype rebuilds the dataview from the new source, so the views survive but their columns are re-derived from the type: column widths and which columns are visible go back to the type's defaults. The default object type and template of each view are restored afterwards, because anytype clears them here. Set the source first, then configure the views.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "source"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query object id."),
				"source": map[string]any{
					"type":        "array",
					"description": "Type ids the query draws from.",
					"items":       map[string]any{"type": "string"},
				},
				"block_id": strProp("Dataview block id. Optional; discovered automatically."),
			}),
		},
		map[string]any{
			"name":        "query-view-create",
			"description": "Add a view to a query or collection, fully configured in one call. A query can hold several views of the same objects, e.g. a table of everything plus a kanban grouped by status.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, mergeProps(map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id."),
				"block_id":  strProp("Dataview block id. Optional; discovered automatically."),
			}, viewConfigProps())),
		},
		map[string]any{
			"name":        "query-view-update",
			"description": "Change an existing view. Only the fields you pass are written: name, layout, grouping and every other setting you omit keeps its stored value, so a call that changes just the columns cannot rename the view or turn a kanban into a table. Declarative for filters and sorts: a list you pass REPLACES the current one entirely, so pass the complete desired set; a list you omit is left untouched, and an empty array clears it. Every entry is checked before anything is written, so a refused entry changes nothing. Keep each stored entry's id when writing a list back. Writing filters or sorts clears a manual row order of the view (query-order-set), as in the app. Use query-filter-add / query-sort-add to add a single entry without restating the rest.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id"}, mergeProps(map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id."),
				"view_id":   strProp("View id from query-inspect."),
				"block_id":  strProp("Dataview block id. Optional; discovered automatically."),
			}, viewConfigProps())),
		},
		map[string]any{
			"name":        "query-view-delete",
			"description": "Delete a view from a query or collection. The objects themselves are untouched.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id."),
				"view_id":   strProp("View id from query-inspect."),
				"block_id":  strProp("Dataview block id. Optional; discovered automatically."),
			}),
		},
		map[string]any{
			"name":        "query-view-arrange",
			"description": "Move a view to another position in the view bar and/or make it the active one.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id"}, map[string]any{
				"space_id":   spaceIDProp(),
				"object_id":  strProp("Query or collection object id."),
				"view_id":    strProp("View id from query-inspect."),
				"position":   map[string]any{"type": "integer", "description": "Zero-based position in the view bar."},
				"set_active": map[string]any{"type": "boolean", "description": "Make this the active view.", "default": false},
				"block_id":   strProp("Dataview block id. Optional; discovered automatically."),
			}),
		},
		map[string]any{
			"name":        "query-filter-add",
			"description": "Add one filter to a view, keeping the existing ones, and return its id and the filter as stored. Pass property_key and condition for one filter, or operator with nested_filters for a group. " + filterGroupNote + " The format is taken from the property; values are converted as the app stores them (tag names to ids, ISO dates to Unix seconds). To filter on an object relation such as linked_projects, use condition=in with the target object ids. " + selfFilterNote + "\n\n" + relationKeySpellingNote,
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id"}, mergeProps(map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id."),
				"view_id":   strProp("View id from query-inspect."),
				"nested_filters": map[string]any{
					"type":        "array",
					"description": "The rules of a group (operator and/or), each with property_key, condition and value.",
					"items":       nestedRuleSchema(),
				},
				"block_id": strProp("Dataview block id. Optional; discovered automatically."),
			}, filterRuleProps())),
		},
		map[string]any{
			"name":        "query-filter-remove",
			"description": "Remove filters from a view by their ids, which come from query-inspect.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id", "filter_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id."),
				"view_id":   strProp("View id from query-inspect."),
				"filter_ids": map[string]any{
					"type":        "array",
					"description": "Filter ids to remove.",
					"items":       map[string]any{"type": "string"},
				},
				"block_id": strProp("Dataview block id. Optional; discovered automatically."),
			}),
		},
		map[string]any{
			"name":        "query-sort-add",
			"description": "Add one sort to a view, keeping the existing sorts, and return its id. Sorts apply in the order they appear. " + relationKeySpellingNote,
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id", "property_key"}, map[string]any{
				"space_id":        spaceIDProp(),
				"object_id":       strProp("Query or collection object id."),
				"view_id":         strProp("View id from query-inspect."),
				"property_key":    strProp("Property key to sort by. " + relationKeySpellingNote),
				"direction":       enumProp("Sort direction. A hand-made row order is query-order-set.", []string{"asc", "desc"}),
				"format":          enumProp("Optional: taken from the property.", anytypefiles.FilterFormatNames()),
				"include_time":    map[string]any{"type": "boolean", "description": "For date sorts: take time of day into account.", "default": false},
				"empty_placement": enumProp("Where objects with an empty value go.", []string{"not_specified", "start", "end"}),
				"block_id":        strProp("Dataview block id. Optional; discovered automatically."),
			}),
		},
		map[string]any{
			"name":        "query-sort-remove",
			"description": "Remove sorts from a view by their ids, which come from query-inspect.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id", "sort_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id."),
				"view_id":   strProp("View id from query-inspect."),
				"sort_ids": map[string]any{
					"type":        "array",
					"description": "Sort ids to remove.",
					"items":       map[string]any{"type": "string"},
				},
				"block_id": strProp("Dataview block id. Optional; discovered automatically."),
			}),
		},
		map[string]any{
			"name":        "query-order-set",
			"description": "Set the manual order of rows or cards in one view — the same thing dragging a row does in the Anytype app. Pass the object ids in the order you want them; the list replaces any order already stored for that view. IMPORTANT: this order is rendered by the Anytype clients, not applied by the server, so get-list-objects-compact keeps returning objects in the view's sort order and is NOT a way to check the result. Verify with query-inspect, which reports the stored order under object_orders.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "view_id", "object_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Query or collection object id."),
				"view_id":   strProp("View id from query-inspect."),
				"object_ids": map[string]any{
					"type":        "array",
					"description": "Object ids in the wanted order.",
					"items":       map[string]any{"type": "string"},
				},
				"group_id": strProp("Kanban group to order within. Only for kanban views; leave empty for grid, list and gallery."),
				"block_id": strProp("Dataview block id. Optional; discovered automatically."),
			}),
		},
		map[string]any{
			"name":        "object-to-query",
			"description": "Convert an existing object into a query over the given source types. The object keeps its id and name.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "source"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object id to convert."),
				"source": map[string]any{
					"type":        "array",
					"description": "Type ids the resulting query draws from.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		map[string]any{
			"name":        "object-to-collection",
			"description": "Convert an existing object into a collection, i.e. a hand-curated list. Use add-list-objects afterwards to fill it.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object id to convert."),
			}),
		},
	}
}

func mergeProps(base map[string]any, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (s *mcpServer) dispatchQueryTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "query-create":
		res, err := s.toolQueryCreate(args)
		return res, err, true
	case "query-inspect":
		res, err := s.toolQueryInspect(args)
		return res, err, true
	case "query-set-source":
		res, err := s.toolQuerySetSource(args)
		return res, err, true
	case "query-view-create":
		res, err := s.toolQueryViewCreate(args)
		return res, err, true
	case "query-view-update":
		res, err := s.toolQueryViewUpdate(args)
		return res, err, true
	case "query-view-delete":
		res, err := s.toolQueryViewDelete(args)
		return res, err, true
	case "query-view-arrange":
		res, err := s.toolQueryViewArrange(args)
		return res, err, true
	case "query-filter-add":
		res, err := s.toolQueryFilterAdd(args)
		return res, err, true
	case "query-filter-remove":
		res, err := s.toolQueryFilterRemove(args)
		return res, err, true
	case "query-sort-add":
		res, err := s.toolQuerySortAdd(args)
		return res, err, true
	case "query-sort-remove":
		res, err := s.toolQuerySortRemove(args)
		return res, err, true
	case "object-to-query":
		res, err := s.toolObjectToQuery(args)
		return res, err, true
	case "object-to-collection":
		res, err := s.toolObjectToCollection(args)
		return res, err, true
	case "query-order-set":
		res, err := s.toolQueryOrderSet(args)
		return res, err, true
	}
	return nil, nil, false
}

// --- argument parsing ----------------------------------------------------

// filterSpecFromArgs reads one filter, or a group with its nested rules. What
// is required depends on the kind, so the checks live in filterprep.go.
func filterSpecFromArgs(args map[string]any) (anytypefiles.FilterSpec, error) {
	spec := anytypefiles.FilterSpec{
		ID:          optionalString(args, "id"),
		RelationKey: optionalString(args, "property_key"),
		Condition:   optionalString(args, "condition"),
		Operator:    optionalString(args, "operator"),
		Format:      optionalString(args, "format"),
		Value:       args["value"],
		QuickOption: optionalString(args, "quick_option"),
	}
	if raw, ok := args["nested_filters"]; ok && raw != nil {
		items, err := asObjectSlice(raw)
		if err != nil {
			return spec, fmt.Errorf("nested_filters: %w", err)
		}
		for i, item := range items {
			nested, err := filterSpecFromArgs(item)
			if err != nil {
				return spec, fmt.Errorf("nested_filters[%d]: %w", i, err)
			}
			spec.NestedFilters = append(spec.NestedFilters, nested)
		}
	}
	if spec.RelationKey == "" && len(spec.NestedFilters) == 0 && spec.Operator == "" {
		return spec, fmt.Errorf("pass property_key and condition for a filter, or operator and nested_filters for a group")
	}
	return spec, nil
}

func sortSpecFromArgs(args map[string]any) (anytypefiles.SortSpec, error) {
	key, err := requiredString(args, "property_key")
	if err != nil {
		return anytypefiles.SortSpec{}, err
	}
	return anytypefiles.SortSpec{
		ID:             optionalString(args, "id"),
		RelationKey:    key,
		Type:           optionalString(args, "direction"),
		Format:         optionalString(args, "format"),
		IncludeTime:    optionalBool(args, "include_time", false),
		EmptyPlacement: optionalString(args, "empty_placement"),
	}, nil
}

func viewSpecFromArgs(args map[string]any) (anytypefiles.ViewSpec, error) {
	spec := anytypefiles.ViewSpec{
		Name:                optionalString(args, "name"),
		Type:                optionalString(args, "layout"),
		GroupRelationKey:    optionalString(args, "group_property_key"),
		CoverRelationKey:    optionalString(args, "cover_property_key"),
		PageLimit:           int32(optionalInt(args, "page_limit", 0)),
		DefaultObjectTypeID: optionalString(args, "default_object_type_id"),
		DefaultTemplateID:   optionalString(args, "default_template_id"),
		HideIcon:            optionalBool(args, "hide_icon", false),
		CardSize:            optionalString(args, "card_size"),
	}

	// Which scalar fields were actually passed has to be recorded, not inferred
	// from their values: query-view-update patches the stored view, and a zero
	// value that arrives as "the caller wants this cleared" erases a name, a
	// layout or a kanban grouping.
	for _, field := range anytypefiles.ViewScalarFields() {
		if raw, ok := args[field]; ok && raw != nil {
			spec.MarkSet(field)
		}
	}

	if raw, ok := args["filters"]; ok && raw != nil {
		spec.FiltersSet = true
		items, err := asObjectSlice(raw)
		if err != nil {
			return spec, fmt.Errorf("filters: %w", err)
		}
		for i, item := range items {
			filter, err := filterSpecFromArgs(item)
			if err != nil {
				return spec, fmt.Errorf("filters[%d]: %w", i, err)
			}
			spec.Filters = append(spec.Filters, filter)
		}
	}
	if raw, ok := args["sorts"]; ok && raw != nil {
		spec.SortsSet = true
		items, err := asObjectSlice(raw)
		if err != nil {
			return spec, fmt.Errorf("sorts: %w", err)
		}
		for i, item := range items {
			sort, err := sortSpecFromArgs(item)
			if err != nil {
				return spec, fmt.Errorf("sorts[%d]: %w", i, err)
			}
			spec.Sorts = append(spec.Sorts, sort)
		}
	}
	if raw, ok := args["relations"]; ok && raw != nil {
		spec.RelationsSet = true
		items, err := asObjectSlice(raw)
		if err != nil {
			return spec, fmt.Errorf("relations: %w", err)
		}
		for i, item := range items {
			key, err := requiredString(item, "property_key")
			if err != nil {
				return spec, fmt.Errorf("relations[%d]: %w", i, err)
			}
			spec.Relations = append(spec.Relations, anytypefiles.RelationSpec{
				Key:       key,
				IsVisible: optionalBool(item, "visible", true),
				Width:     int32(optionalInt(item, "width", 0)),
			})
		}
	}
	return spec, nil
}

// queryTarget resolves the arguments every dataview-editing tool shares.
func (s *mcpServer) queryTarget(args map[string]any) (*anytypefiles.Client, string, string, string, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, "", "", "", err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, "", "", "", err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, "", "", "", err
	}
	blockID, err := resolveDataviewBlock(client, spaceID, objectID, optionalString(args, "block_id"))
	if err != nil {
		client.Close()
		return nil, "", "", "", err
	}
	return client, spaceID, objectID, blockID, nil
}

// annotateAPIKeys adds the list-properties spelling of every property key the
// result reports, and rebuilds the views with it.
//
// It is best-effort by design: the annotation needs a search of the space's
// properties, and a query that was inspected successfully must not turn into a
// failed tool call because that extra search did not come back. A failure is
// reported as a warning next to the answer instead.
func annotateAPIKeys(client *anytypefiles.Client, spaceID string, info *anytypefiles.DataviewInfo) {
	if err := client.AnnotateAPIKeys(context.Background(), spaceID, info); err != nil {
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"could not read the space's properties, so api_property_key is missing from this answer: %v", err))
	}
}

// setAPIKey adds the list-properties spelling of a key, and only when there is
// one to add: an absent api_property_key means the two spellings coincide, so
// property_key is already what list-properties shows.
func setAPIKey(entry map[string]any, apiKey string) {
	if apiKey != "" {
		entry["api_property_key"] = apiKey
	}
}

// filterEntry reports one filter in the spelling the write side takes, so a
// list read here can be written back unchanged. A group reports its operator
// and rules instead of a property and condition.
func filterEntry(f anytypefiles.FilterSpec) map[string]any {
	entry := map[string]any{"id": f.ID, "operator": f.Operator}
	if f.RelationKey != "" || len(f.NestedFilters) == 0 {
		entry["property_key"] = f.RelationKey
		entry["condition"] = f.Condition
		entry["format"] = f.Format
		entry["value"] = f.Value
		setAPIKey(entry, f.APIRelationKey)
	}
	if f.QuickOption != "" {
		entry["quick_option"] = f.QuickOption
	}
	if len(f.NestedFilters) > 0 {
		nested := make([]map[string]any, 0, len(f.NestedFilters))
		for _, n := range f.NestedFilters {
			nested = append(nested, filterEntry(n))
		}
		entry["nested_filters"] = nested
	}
	if f.ID == "" {
		delete(entry, "id")
	}
	return entry
}

func dataviewResult(info *anytypefiles.DataviewInfo) map[string]any {
	views := make([]map[string]any, 0, len(info.Views))
	for _, v := range info.Views {
		filters := make([]map[string]any, 0, len(v.Filters))
		for _, f := range v.Filters {
			filters = append(filters, filterEntry(f))
		}
		sorts := make([]map[string]any, 0, len(v.Sorts))
		for _, so := range v.Sorts {
			entry := map[string]any{
				"id": so.ID, "property_key": so.RelationKey, "direction": so.Type,
				"format": so.Format, "empty_placement": so.EmptyPlacement,
			}
			setAPIKey(entry, so.APIRelationKey)
			sorts = append(sorts, entry)
		}
		relations := make([]map[string]any, 0, len(v.Relations))
		for _, r := range v.Relations {
			entry := map[string]any{
				"property_key": r.Key, "visible": r.IsVisible, "width": r.Width,
			}
			setAPIKey(entry, r.APIKey)
			relations = append(relations, entry)
		}
		view := map[string]any{
			"id": v.ID, "name": v.Name, "layout": v.Type,
			"filters": filters, "sorts": sorts, "relations": relations,
			"group_property_key": v.GroupRelationKey,
			"page_limit":         v.PageLimit,
		}
		// Everything the view tools accept has to be readable again, or a
		// caller cannot tell whether a setting took effect.
		if v.DefaultObjectTypeID != "" {
			view["default_object_type_id"] = v.DefaultObjectTypeID
		}
		if v.DefaultTemplateID != "" {
			view["default_template_id"] = v.DefaultTemplateID
		}
		if v.CoverRelationKey != "" {
			view["cover_property_key"] = v.CoverRelationKey
			if v.APICoverKey != "" {
				view["api_cover_property_key"] = v.APICoverKey
			}
		}
		if v.APIGroupKey != "" {
			view["api_group_property_key"] = v.APIGroupKey
		}
		if v.CardSize != "" {
			view["card_size"] = v.CardSize
		}
		if v.HideIcon {
			view["hide_icon"] = true
		}
		views = append(views, view)
	}
	out := map[string]any{
		"object_id":     info.ObjectID,
		"block_id":      info.BlockID,
		"source":        info.Source,
		"is_collection": info.IsCollection,
		"active_view":   info.ActiveView,
		"views":         views,
		"view_count":    len(views),
	}
	// An embedded view has no source of its own — the object it points at is
	// the source — so both facts are reported rather than an empty list that
	// reads like a broken query.
	if info.TargetObjectID != "" {
		out["target_object_id"] = info.TargetObjectID
		out["is_embedded_view"] = true
	}
	if info.SourceFrom != "" {
		out["source_from"] = info.SourceFrom
	}
	if len(info.Blocks) > 1 {
		blocks := make([]map[string]any, 0, len(info.Blocks))
		for _, b := range info.Blocks {
			entry := map[string]any{"block_id": b.BlockID}
			if b.TargetObjectID != "" {
				entry["target_object_id"] = b.TargetObjectID
			}
			blocks = append(blocks, entry)
		}
		out["dataview_blocks"] = blocks
	}
	if len(info.Warnings) > 0 {
		out["warnings"] = info.Warnings
	}
	// Only present once something has been ordered manually, so an absent key
	// means "no manual order", not "unknown".
	if len(info.ObjectOrders) > 0 {
		orders := make([]map[string]any, 0, len(info.ObjectOrders))
		for _, o := range info.ObjectOrders {
			entry := map[string]any{"view_id": o.ViewID, "object_ids": o.ObjectIDs}
			if o.GroupID != "" {
				entry["group_id"] = o.GroupID
			}
			orders = append(orders, entry)
		}
		out["object_orders"] = orders
	}
	return out
}

// --- handlers ------------------------------------------------------------

func (s *mcpServer) toolQueryCreate(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	source := optionalStringSlice(args, "source")
	if len(source) == 0 {
		return nil, fmt.Errorf("source is required: a query without a source type returns nothing")
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	objectID, err := client.CreateQuery(context.Background(), spaceID,
		optionalString(args, "name"), source, optionalString(args, "icon_emoji"))
	if err != nil {
		return nil, err
	}
	info, err := client.InspectDataview(context.Background(), spaceID, objectID, "")
	if err != nil {
		// The query exists; only the read-back failed.
		return map[string]any{
			"space_id": spaceID, "object_id": objectID, "created": true,
			"warning": fmt.Sprintf("query created but could not be inspected: %v", err),
		}, nil
	}
	annotateAPIKeys(client, spaceID, info)
	out := dataviewResult(info)
	out["space_id"] = spaceID
	out["created"] = true
	return out, nil
}

func (s *mcpServer) toolQueryInspect(args map[string]any) (map[string]any, error) {
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

	info, err := client.InspectDataview(context.Background(), spaceID, objectID,
		optionalString(args, "block_id"))
	if err != nil {
		return nil, err
	}
	annotateAPIKeys(client, spaceID, info)
	out := dataviewResult(info)
	out["space_id"] = spaceID
	return out, nil
}

func (s *mcpServer) toolQuerySetSource(args map[string]any) (map[string]any, error) {
	source := optionalStringSlice(args, "source")
	if len(source) == 0 {
		return nil, fmt.Errorf("source is required")
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetQuerySource(context.Background(), spaceID, objectID, blockID, source); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_id": blockID,
		"source": source, "updated": true,
	}, nil
}

func (s *mcpServer) toolQueryViewCreate(args map[string]any) (map[string]any, error) {
	spec, err := viewSpecFromArgs(args)
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	viewID, err := client.CreateView(context.Background(), spaceID, objectID, blockID, spec)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_id": blockID,
		"view_id": viewID, "created": true,
	}, nil
}

func (s *mcpServer) toolQueryViewUpdate(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	spec, err := viewSpecFromArgs(args)
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.UpdateView(context.Background(), spaceID, objectID, blockID, viewID, spec); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "block_id": blockID,
		"view_id": viewID, "updated": true,
		"note": "everything passed in this call was written and everything omitted kept its stored value; a passed list replaced the previous one entirely",
	}, nil
}

func (s *mcpServer) toolQueryViewDelete(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.DeleteView(context.Background(), objectID, blockID, viewID); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "view_id": viewID, "deleted": true,
	}, nil
}

func (s *mcpServer) toolQueryViewArrange(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	_, hasPosition := args["position"]
	setActive := optionalBool(args, "set_active", false)
	if !hasPosition && !setActive {
		return nil, fmt.Errorf("query-view-arrange needs position and/or set_active")
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	out := map[string]any{
		"space_id": spaceID, "object_id": objectID, "view_id": viewID,
	}
	if hasPosition {
		position := optionalInt(args, "position", 0)
		if position < 0 {
			return nil, fmt.Errorf("position must be >= 0")
		}
		if err := client.SetViewPosition(context.Background(), objectID, blockID, viewID, uint32(position)); err != nil {
			return nil, err
		}
		out["position"] = position
	}
	if setActive {
		if err := client.SetActiveView(context.Background(), objectID, blockID, viewID); err != nil {
			return nil, err
		}
		out["active"] = true
	}
	return out, nil
}

func (s *mcpServer) toolQueryFilterAdd(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	spec, err := filterSpecFromArgs(args)
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	id, stored, err := client.AddFilter(context.Background(), spaceID, objectID, blockID, viewID, spec)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "view_id": viewID,
		"filter_id": id, "filter": filterEntry(stored), "added": true,
	}, nil
}

func (s *mcpServer) toolQueryFilterRemove(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	ids := optionalStringSlice(args, "filter_ids")
	if len(ids) == 0 {
		return nil, fmt.Errorf("filter_ids is required; read the ids with query-inspect")
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.RemoveFilters(context.Background(), spaceID, objectID, blockID, viewID, ids); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "view_id": viewID,
		"removed_filter_ids": ids, "removed": true,
	}, nil
}

func (s *mcpServer) toolQuerySortAdd(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	spec, err := sortSpecFromArgs(args)
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	id, err := client.AddSort(context.Background(), spaceID, objectID, blockID, viewID, spec)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "view_id": viewID,
		"sort_id": id, "property_key": spec.RelationKey, "direction": spec.Type, "added": true,
	}, nil
}

func (s *mcpServer) toolQuerySortRemove(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	ids := optionalStringSlice(args, "sort_ids")
	if len(ids) == 0 {
		return nil, fmt.Errorf("sort_ids is required; read the ids with query-inspect")
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.RemoveSorts(context.Background(), spaceID, objectID, blockID, viewID, ids); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "view_id": viewID,
		"removed_sort_ids": ids, "removed": true,
	}, nil
}

func (s *mcpServer) toolObjectToQuery(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, err
	}
	source := optionalStringSlice(args, "source")
	if len(source) == 0 {
		return nil, fmt.Errorf("source is required when converting to a query")
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.ConvertToQuery(context.Background(), objectID, source); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "source": source, "converted_to": "query",
	}, nil
}

func (s *mcpServer) toolObjectToCollection(args map[string]any) (map[string]any, error) {
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

	if err := client.ConvertToCollection(context.Background(), objectID); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID, "converted_to": "collection",
	}, nil
}

func (s *mcpServer) toolQueryOrderSet(args map[string]any) (map[string]any, error) {
	viewID, err := requiredString(args, "view_id")
	if err != nil {
		return nil, err
	}
	objectIDs := optionalStringSlice(args, "object_ids")
	if len(objectIDs) == 0 {
		return nil, fmt.Errorf("object_ids is required; pass the ids in the order you want them")
	}
	client, spaceID, objectID, blockID, err := s.queryTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	groupID := optionalString(args, "group_id")
	if err := client.SetObjectOrder(context.Background(), objectID, blockID,
		viewID, groupID, objectIDs); err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID, "view_id": viewID,
		"ordered_object_ids": objectIDs, "ordered_count": len(objectIDs),
		"updated": true,
	}
	if groupID != "" {
		out["group_id"] = groupID
	}
	return out, nil
}
