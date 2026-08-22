package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// This file adds plain REST wrappers for the official Anytype MCP tools that the
// compact wrappers in compact_tools.go do not cover. Together they make this
// server a full replacement for @anyproto/anytype-mcp, which matters because
// some connectors expose only a single MCP endpoint: everything the model should
// see has to live in one server.
//
// Naming follows the official tool names 1:1 so existing prompts keep working.
// Responses here are small by nature, so unlike compact_tools.go these mostly
// pass the API payload through; the two list endpoints that can grow long
// (properties, tags) return essential fields unless full=true.

func restSchema(required []string, props map[string]any) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

func strProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func enumProp(description string, values []string) map[string]any {
	vals := make([]any, 0, len(values))
	for _, v := range values {
		vals = append(vals, v)
	}
	return map[string]any{"type": "string", "description": description, "enum": vals}
}

func spaceIDProp() map[string]any {
	return strProp("Anytype space ID, as returned by list-spaces.")
}

func offsetProp() map[string]any {
	return map[string]any{"type": "integer", "description": "Number of items to skip.", "default": 0}
}

func limitProp() map[string]any {
	return map[string]any{"type": "integer", "description": "Maximum number of items to return.", "default": 50}
}

func fullProp(what string) map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": fmt.Sprintf("Return the raw API payload for each %s instead of the essential fields only.", what),
		"default":     false,
	}
}

var propertyFormats = []string{"text", "number", "select", "multi_select", "date", "files", "checkbox", "url", "email", "phone", "objects"}

// deleteIsArchiveNote is appended to the delete-* descriptions: all three
// archive rather than erase, and the effect is only visible through list-*.
//
// The restore half of that is worth spelling out. Archiving a schema object is
// a plain add to the space's archive collection (anytype-heart,
// core/block/detailservice/set_details.go: setIsArchivedForObjects), with no
// branch on the kind of object and only the bundled system types and relations
// excluded, and un-archiving skips the restriction check entirely. So the
// generic object-set-archived undoes all three of these — but nothing in the
// toolset said so, and a caller who does not know it reads "archived" as "gone".
const deleteIsArchiveNote = " Deletion is an archive, not an erase, and it is only observable through the list tools: after roughly a second the item disappears from list-properties/list-tags/list-types-compact, while get- by id keeps returning it as though nothing happened. Verify with the list tool, never with get-." + restoreNote

// restoreNote names the undo for every archiving tool, schema ones included.
const restoreNote = " To undo: object-set-archived with archived=false and this item's id — it takes any object id, properties, tags and types included. list-archived shows what is in the bin."

// typePropertyLinksProp describes the properties list of create-type and
// update-type. Two things about it are worth stating where the caller reads
// them, because neither is guessable and both are destructive when guessed
// wrong (anytype-heart, core/api/service/type.go: buildUpdatedTypeDetails and
// buildRelationIds):
//
//   - the list REPLACES recommendedRelations wholesale. Properties already on
//     the type and missing from the list are unlinked. Only the type's featured
//     properties live in a different relation and survive.
//   - a key that matches no property is not refused by Anytype. It is CREATED
//     as a new property of the space, so a typo silently grows the schema.
//
// The guarded flag says which of the two tools this is for. update-type checks
// the keys against the space first and refuses the unknown ones unless
// allow_new_properties is set; create-type does not, because listing properties
// that do not exist yet is the point of it. The sentence has to differ, or one
// of the two tools is described wrongly.
func typePropertyLinksProp(verb string, guarded bool) map[string]any {
	unknownKeyNote := "A key that matches no existing property is NOT rejected — Anytype creates a new property for it, so a typo adds one to the space. "
	if guarded {
		unknownKeyNote = "A key that matches no existing property is refused here, because on an existing type it is nearly always a misspelling: it would fail to link the property you meant AND add a stray one to the space. Pass allow_new_properties=true to create them on purpose. "
	}
	return map[string]any{
		"type": "array",
		"description": verb + " e.g. [{\"key\":\"due_date\",\"format\":\"date\"}]. " +
			"This list REPLACES the type's linked properties: read the current set with get-type-compact and pass it back in full, " +
			"or anything you leave out is unlinked (the type's featured properties are kept separately and survive). " +
			"Omit the field entirely to leave the links alone. " +
			unknownKeyNote +
			"Anytype adds its own system properties on top. Read the result back with get-type-compact.",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":    strProp("Property key from list-properties, e.g. \"due_date\"."),
				"name":   strProp("Name for the property, used only when the key does not exist yet and Anytype has to create it."),
				"format": enumProp("Property format, used only when the key does not exist yet.", propertyFormats),
			},
			"required": []any{"key"},
		},
	}
}

var tagColors = []string{"grey", "yellow", "orange", "red", "pink", "purple", "blue", "ice", "teal", "lime"}
var typeLayouts = []string{"basic", "profile", "action", "note"}

// leanToolset drops these from tools/list when ANYTYPE_MCP_TOOLSET=lean.
//
// They are NOT hidden by default. Hiding a delete tool does not prevent damage,
// it only prevents repair: a model that just created the wrong tag or property
// can no longer clean up after itself. The goal is parity with what the GUI can
// do, so the default advertises everything. This server also carries no
// duplicates to trim — the tools with a -compact variant were never added a
// second time under their official name.
var leanToolset = map[string]bool{
	"create-space": true,
	"update-space": true,
	"list-members": true,
	"get-member":   true,
}

// restToolDefs returns the tool definitions appended to the tools/list response.
func (s *mcpServer) restToolDefs() []any {
	all := withBatchParams(allRestToolDefs())
	if !s.cfg.leanToolset {
		return all
	}
	filtered := make([]any, 0, len(all))
	for _, def := range all {
		entry, ok := def.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		if leanToolset[name] {
			continue
		}
		filtered = append(filtered, def)
	}
	return filtered
}

func allRestToolDefs() []any {
	return []any{
		// --- spaces -------------------------------------------------------
		map[string]any{
			"name":        "list-spaces",
			"description": "List all spaces the connected Anytype account can access. Start here: every other tool needs a space_id from this list.",
			"inputSchema": restSchema(nil, map[string]any{
				"offset": offsetProp(),
				"limit":  limitProp(),
				"full":   fullProp("space"),
			}),
		},
		map[string]any{
			"name":        "get-space",
			"description": "Get details for a single space.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
			}),
		},
		map[string]any{
			"name":        "create-space",
			"description": "Create a new space. Rarely needed; prefer working inside existing spaces.",
			"inputSchema": restSchema([]string{"name"}, map[string]any{
				"name":        strProp("Name of the new space."),
				"description": strProp("Optional description."),
			}),
		},
		map[string]any{
			"name":        "update-space",
			"description": "Update a space's name or description.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"name":        strProp("New name."),
				"description": strProp("New description."),
			}),
		},

		// --- objects ------------------------------------------------------
		map[string]any{
			"name":        "delete-object",
			"description": "Archive (soft-delete) ONE object. The object stays retrievable and reports archived=true; it is not permanently erased, and object-set-archived brings it back. For several objects use object-set-archived with archived=true instead — it takes a list and saves one call per object.",
			"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("ID of the object to archive."),
			}),
		},

		// --- collections / lists ------------------------------------------
		map[string]any{
			"name":        "add-list-objects",
			"description": "Add objects to a collection. Works for manually curated collections, not for rule-based queries (sets), whose contents come from their source and filters.",
			"inputSchema": restSchema([]string{"space_id", "list_id", "objects"}, map[string]any{
				"space_id": spaceIDProp(),
				"list_id":  strProp("Collection object ID."),
				"objects": map[string]any{
					"type":        "array",
					"description": "Object IDs to add.",
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		map[string]any{
			"name":        "remove-list-object",
			"description": "Remove one object from a collection. This only unlinks it from the collection; the object itself is untouched.",
			"inputSchema": restSchema([]string{"space_id", "list_id", "object_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"list_id":   strProp("Collection object ID."),
				"object_id": strProp("Object ID to remove from the collection."),
			}),
		},

		// --- members ------------------------------------------------------
		map[string]any{
			"name":        "list-members",
			"description": "List members of a space.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"offset":   offsetProp(),
				"limit":    limitProp(),
			}),
		},
		map[string]any{
			"name":        "get-member",
			"description": "Get details for one member of a space.",
			"inputSchema": restSchema([]string{"space_id", "member_id"}, map[string]any{
				"space_id":  spaceIDProp(),
				"member_id": strProp("Member ID."),
			}),
		},

		// --- properties ---------------------------------------------------
		map[string]any{
			"name":        "list-properties",
			"description": "List the properties defined in a space. Use this to discover the property_key values needed for object updates and for query filters. Returns id, key, name and format unless full=true.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"offset":   offsetProp(),
				"limit":    map[string]any{"type": "integer", "description": "Maximum number of items to return.", "default": 100},
				"full":     fullProp("property"),
			}),
		},
		map[string]any{
			"name":        "get-property",
			"description": "Get one property definition, including its format. Note this also returns archived properties, with nothing in the response to mark them as such — use list-properties to tell live from deleted.",
			"inputSchema": restSchema([]string{"space_id", "property_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID: the bafyrei... id from list-properties. The property key is NOT accepted here and returns 404/500."),
			}),
		},
		map[string]any{
			"name":        "create-property",
			"description": "Create a property in a space. For select/multi_select you can create the tag options inline via tags.",
			"inputSchema": restSchema([]string{"space_id", "name", "format"}, map[string]any{
				"space_id": spaceIDProp(),
				"name":     strProp("Display name of the property."),
				"format":   enumProp("Property format. Determines which value field objects must use.", propertyFormats),
				"key":      strProp("Optional explicit property key."),
				"tags": map[string]any{
					"type":        "array",
					"description": "Tag options to create for select/multi_select properties.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":  strProp("Tag name."),
							"color": enumProp("Tag color.", tagColors),
							"key":   strProp("Optional explicit tag key."),
						},
						"required": []any{"name", "color"},
					},
				},
			}),
		},
		map[string]any{
			"name":        "update-property",
			"description": "Update a property. The Anytype API requires name on every update, so pass the current name if you only want to change the key.",
			"inputSchema": restSchema([]string{"space_id", "property_id", "name"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID: the bafyrei... id from list-properties. The property key is NOT accepted here and returns 404/500."),
				"name":        strProp("Property name (required by the API even when unchanged)."),
				"key":         strProp("New property key."),
			}),
		},
		map[string]any{
			"name":        "delete-property",
			"description": "Archive a property. Destructive for the space schema; confirm before use." + deleteIsArchiveNote,
			"inputSchema": restSchema([]string{"space_id", "property_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID: the bafyrei... id from list-properties. The property key is NOT accepted here and returns 404/500."),
			}),
		},

		// --- tags ---------------------------------------------------------
		map[string]any{
			"name":        "list-tags",
			"description": "List the tag options of a select/multi_select property. Query filters and object updates reference tags by id, so fetch them here first. Returns id, key, name and color unless full=true.",
			"inputSchema": restSchema([]string{"space_id", "property_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID (bafyrei... from list-properties) of a select/multi_select property. The property key is NOT accepted here."),
				"offset":      offsetProp(),
				"limit":       map[string]any{"type": "integer", "description": "Maximum number of items to return.", "default": 100},
				"full":        fullProp("tag"),
			}),
		},
		map[string]any{
			"name":        "get-tag",
			"description": "Get one tag option of a property. Note this also returns archived tags, with nothing in the response to mark them as such — use list-tags to tell live from deleted.",
			"inputSchema": restSchema([]string{"space_id", "property_id", "tag_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID: the bafyrei... id from list-properties. The property key is NOT accepted here and returns 404/500."),
				"tag_id":      strProp("Tag ID from list-tags."),
			}),
		},
		map[string]any{
			"name":        "create-tag",
			"description": "Create a tag option for a select/multi_select property.",
			"inputSchema": restSchema([]string{"space_id", "property_id", "name", "color"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID: the bafyrei... id from list-properties. The property key is NOT accepted here and returns 404/500."),
				"name":        strProp("Tag name."),
				"color":       enumProp("Tag color.", tagColors),
				"key":         strProp("Optional explicit tag key."),
			}),
		},
		map[string]any{
			"name":        "update-tag",
			"description": "Update a tag option's name or color.",
			"inputSchema": restSchema([]string{"space_id", "property_id", "tag_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID: the bafyrei... id from list-properties. The property key is NOT accepted here and returns 404/500."),
				"tag_id":      strProp("Tag ID from list-tags."),
				"name":        strProp("New tag name."),
				"color":       enumProp("New tag color.", tagColors),
				"key":         strProp("New tag key."),
			}),
		},
		map[string]any{
			"name":        "delete-tag",
			"description": "Archive a tag option. Objects still referencing it keep the stale reference; confirm before use." + deleteIsArchiveNote,
			"inputSchema": restSchema([]string{"space_id", "property_id", "tag_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"property_id": strProp("Property ID: the bafyrei... id from list-properties. The property key is NOT accepted here and returns 404/500."),
				"tag_id":      strProp("Tag ID from list-tags."),
			}),
		},

		// --- types --------------------------------------------------------
		map[string]any{
			"name":        "create-type",
			"description": "Create an object type. Note the layout enum has no set/collection value: queries and collections are system types and cannot be created this way.",
			"inputSchema": restSchema([]string{"space_id", "name", "plural_name", "layout"}, map[string]any{
				"space_id":    spaceIDProp(),
				"name":        strProp("Singular type name."),
				"plural_name": strProp("Plural type name."),
				"layout":      enumProp("Type layout.", typeLayouts),
				"key":         strProp("Optional explicit type key."),
				"icon": map[string]any{
					"type":        "object",
					"description": "Optional icon, e.g. {\"format\":\"emoji\",\"emoji\":\"📘\"}.",
				},
				"properties": typePropertyLinksProp("Properties to link to the type,", false),
			}),
		},
		map[string]any{
			"name":        "update-type",
			"description": "Update an object type's name, layout, icon or linked properties. Unlike create-type, a properties key that names no existing property is refused here rather than creating one, because on an existing type that is nearly always a misspelling; pass allow_new_properties=true when a new property really is meant.",
			"inputSchema": restSchema([]string{"space_id", "type_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"type_id":     strProp("Type ID: the bafyrei... id from list-types-compact. The type key is NOT accepted here and returns 500."),
				"name":        strProp("New singular name."),
				"plural_name": strProp("New plural name."),
				"layout":      enumProp("New type layout.", typeLayouts),
				"key":         strProp("New type key."),
				"icon": map[string]any{
					"type":        "object",
					"description": "New icon.",
				},
				"properties": typePropertyLinksProp("New set of properties for the type,", true),
				"allow_new_properties": map[string]any{
					"type":        "boolean",
					"description": "Allow properties keys that do not exist yet, letting Anytype create a space-wide property for each. Off by default: a typo here would both fail to link the property you meant and leave a stray property behind. Use create-property first when you can, and this flag when the type update and the new property belong together.",
					"default":     false,
				},
			}),
		},
		map[string]any{
			"name":        "delete-type",
			"description": "Archive an object type. Destructive for the space schema; confirm before use." + deleteIsArchiveNote,
			"inputSchema": restSchema([]string{"space_id", "type_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"type_id":  strProp("Type ID: the bafyrei... id from list-types-compact. The type key is NOT accepted here and returns 500."),
			}),
		},

		// --- templates ----------------------------------------------------
		map[string]any{
			"name":        "list-templates",
			"description": "List the templates available for an object type. Pass a template_id to create-objects-compact-many to apply one.",
			"inputSchema": restSchema([]string{"space_id", "type_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"type_id":  strProp("Type ID: the bafyrei... id from list-types-compact. The type key is NOT accepted here and returns 500."),
				"offset":   offsetProp(),
				"limit":    limitProp(),
			}),
		},
		map[string]any{
			"name":        "get-template",
			"description": "Get one template of an object type, including its body.",
			"inputSchema": restSchema([]string{"space_id", "type_id", "template_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"type_id":     strProp("Type ID: the bafyrei... id from list-types-compact. The type key is NOT accepted here and returns 500."),
				"template_id": strProp("Template object ID."),
			}),
		},
	}
}

// dispatchRestTool handles the tools added by this file. The third return value
// reports whether the tool name belonged here at all, so callTool can fall
// through to its own "unknown tool" error for anything else.
func (s *mcpServer) dispatchRestTool(name string, args map[string]any) (map[string]any, error, bool) {
	// A batch is only taken when the caller actually supplied the plural
	// parameter, so the single-item form keeps its familiar response shape.
	if spec, ok := restBatchable[name]; ok {
		if raw, present := args[spec.listKey]; present && raw != nil {
			elements, ok := raw.([]any)
			if !ok {
				return nil, fmt.Errorf("%s must be an array", spec.listKey), true
			}
			if len(elements) == 0 {
				return nil, fmt.Errorf("%s is empty; pass at least one %s", spec.listKey, spec.label), true
			}
			res, err := s.runRestBatch(name, spec, args, elements)
			return res, err, true
		}
	}
	return s.dispatchRestSingle(name, args)
}

func (s *mcpServer) dispatchRestSingle(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "list-spaces":
		res, err := s.toolListSpaces(args)
		return res, err, true
	case "get-space":
		res, err := s.toolGetSpace(args)
		return res, err, true
	case "create-space":
		res, err := s.toolCreateSpace(args)
		return res, err, true
	case "update-space":
		res, err := s.toolUpdateSpace(args)
		return res, err, true
	case "delete-object":
		res, err := s.toolDeleteObject(args)
		return res, err, true
	case "add-list-objects":
		res, err := s.toolAddListObjects(args)
		return res, err, true
	case "remove-list-object":
		res, err := s.toolRemoveListObject(args)
		return res, err, true
	case "list-members":
		res, err := s.toolListMembers(args)
		return res, err, true
	case "get-member":
		res, err := s.toolGetMember(args)
		return res, err, true
	case "list-properties":
		res, err := s.toolListProperties(args)
		return res, err, true
	case "get-property":
		res, err := s.toolGetProperty(args)
		return res, err, true
	case "create-property":
		res, err := s.toolCreateProperty(args)
		return res, err, true
	case "update-property":
		res, err := s.toolUpdateProperty(args)
		return res, err, true
	case "delete-property":
		res, err := s.toolDeleteProperty(args)
		return res, err, true
	case "list-tags":
		res, err := s.toolListTags(args)
		return res, err, true
	case "get-tag":
		res, err := s.toolGetTag(args)
		return res, err, true
	case "create-tag":
		res, err := s.toolCreateTag(args)
		return res, err, true
	case "update-tag":
		res, err := s.toolUpdateTag(args)
		return res, err, true
	case "delete-tag":
		res, err := s.toolDeleteTag(args)
		return res, err, true
	case "create-type":
		res, err := s.toolCreateType(args)
		return res, err, true
	case "update-type":
		res, err := s.toolUpdateType(args)
		return res, err, true
	case "delete-type":
		res, err := s.toolDeleteType(args)
		return res, err, true
	case "list-templates":
		res, err := s.toolListTemplates(args)
		return res, err, true
	case "get-template":
		res, err := s.toolGetTemplate(args)
		return res, err, true
	}
	return nil, nil, false
}

// --- helpers -------------------------------------------------------------

func paginationQuery(args map[string]any, defaultLimit int) (url.Values, int, int, error) {
	offset, limit, err := paginationArgs(args, defaultLimit)
	if err != nil {
		return nil, 0, 0, err
	}
	query := url.Values{}
	query.Set("offset", strconv.Itoa(offset))
	query.Set("limit", strconv.Itoa(limit))
	return query, offset, limit, nil
}

// pickFields reduces a list payload to the named fields unless full is set.
func pickFields(payload map[string]any, full bool, fields ...string) []map[string]any {
	rawData, _ := payload["data"].([]any)
	out := make([]map[string]any, 0, len(rawData))
	for _, raw := range rawData {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if full {
			out = append(out, item)
			continue
		}
		reduced := map[string]any{}
		for _, f := range fields {
			copyIfPresent(reduced, item, f)
		}
		out = append(out, reduced)
	}
	return out
}

// listEnvelope wraps a reduced list with the usual pagination context.
func listEnvelope(key string, items []map[string]any, payload map[string]any, offset, limit int, extra map[string]any) map[string]any {
	out := map[string]any{
		key:      items,
		"count":  len(items),
		"offset": offset,
		"limit":  limit,
	}
	for k, v := range extra {
		out[k] = v
	}
	copyIfPresent(out, payload, "pagination")
	return out
}

// setIfPresent copies an optional string argument into a request body.
func setIfPresent(body map[string]any, args map[string]any, key string) {
	if v := optionalString(args, key); v != "" {
		body[key] = v
	}
}

// setRawIfPresent copies an optional non-string argument (object/array) through.
func setRawIfPresent(body map[string]any, args map[string]any, key string) {
	if v, ok := args[key]; ok && v != nil {
		body[key] = v
	}
}

// --- spaces --------------------------------------------------------------

func (s *mcpServer) toolListSpaces(args map[string]any) (map[string]any, error) {
	query, offset, limit, err := paginationQuery(args, 50)
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodGet, "/v1/spaces", query, nil)
	if err != nil {
		return nil, err
	}
	full := optionalBool(args, "full", false)
	spaces := pickFields(payload, full, "id", "name", "description", "object")
	return listEnvelope("spaces", spaces, payload, offset, limit, nil), nil
}

func (s *mcpServer) toolGetSpace(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	return s.anytypeAPIRequest(http.MethodGet, "/v1/spaces/"+url.PathEscape(spaceID), nil, nil)
}

func (s *mcpServer) toolCreateSpace(args map[string]any) (map[string]any, error) {
	name, err := requiredString(args, "name")
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name}
	setIfPresent(body, args, "description")
	return s.anytypeAPIRequest(http.MethodPost, "/v1/spaces", nil, body)
}

func (s *mcpServer) toolUpdateSpace(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	setIfPresent(body, args, "name")
	setIfPresent(body, args, "description")
	if len(body) == 0 {
		return nil, fmt.Errorf("update-space needs at least one of name or description")
	}
	return s.anytypeAPIRequest(http.MethodPatch, "/v1/spaces/"+url.PathEscape(spaceID), nil, body)
}

// --- objects -------------------------------------------------------------

func (s *mcpServer) toolDeleteObject(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodDelete,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/objects/"+url.PathEscape(objectID), nil, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id":  spaceID,
		"object_id": objectID,
		"archived":  true,
		"note":      "Object archived (soft delete). It remains retrievable with archived=true.",
	}
	copyIfPresent(out, payload, "object")
	return out, nil
}

// --- collections ---------------------------------------------------------

func (s *mcpServer) toolAddListObjects(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	listID, err := requiredString(args, "list_id")
	if err != nil {
		return nil, err
	}
	objects := optionalStringSlice(args, "objects")
	if len(objects) == 0 {
		return nil, fmt.Errorf("add-list-objects needs a non-empty objects array of object IDs")
	}
	// The endpoint expects {"objects": [...]}, not a bare array.
	payload, err := s.anytypeAPIRequest(http.MethodPost,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/lists/"+url.PathEscape(listID)+"/objects",
		nil, map[string]any{"objects": objects})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID,
		"list_id":  listID,
		"added":    objects,
		"count":    len(objects),
	}
	copyIfPresent(out, payload, "object")
	return out, nil
}

func (s *mcpServer) toolRemoveListObject(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	listID, err := requiredString(args, "list_id")
	if err != nil {
		return nil, err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodDelete,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/lists/"+url.PathEscape(listID)+"/objects/"+url.PathEscape(objectID), nil, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id":  spaceID,
		"list_id":   listID,
		"object_id": objectID,
		"removed":   true,
		"note":      "Object removed from the collection only; the object itself still exists.",
	}
	copyIfPresent(out, payload, "object")
	return out, nil
}

// --- members -------------------------------------------------------------

func (s *mcpServer) toolListMembers(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	query, offset, limit, err := paginationQuery(args, 50)
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/members", query, nil)
	if err != nil {
		return nil, err
	}
	members := pickFields(payload, true)
	return listEnvelope("members", members, payload, offset, limit,
		map[string]any{"space_id": spaceID}), nil
}

func (s *mcpServer) toolGetMember(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	memberID, err := requiredString(args, "member_id")
	if err != nil {
		return nil, err
	}
	return s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/members/"+url.PathEscape(memberID), nil, nil)
}

// --- properties ----------------------------------------------------------

func (s *mcpServer) toolListProperties(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	query, offset, limit, err := paginationQuery(args, 100)
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties", query, nil)
	if err != nil {
		return nil, err
	}
	full := optionalBool(args, "full", false)
	props := pickFields(payload, full, "id", "key", "name", "format")
	return listEnvelope("properties", props, payload, offset, limit,
		map[string]any{"space_id": spaceID}), nil
}

func (s *mcpServer) toolGetProperty(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return nil, err
	}
	return s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties/"+url.PathEscape(propertyID), nil, nil)
}

func (s *mcpServer) toolCreateProperty(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	name, err := requiredString(args, "name")
	if err != nil {
		return nil, err
	}
	format, err := requiredString(args, "format")
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name, "format": format}
	setIfPresent(body, args, "key")
	setRawIfPresent(body, args, "tags")
	return s.anytypeAPIRequest(http.MethodPost,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties", nil, body)
}

func (s *mcpServer) toolUpdateProperty(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return nil, err
	}
	// The API rejects updates without name, even when only the key changes.
	name, err := requiredString(args, "name")
	if err != nil {
		return nil, fmt.Errorf("update-property requires name (the Anytype API rejects updates without it); fetch the current name with get-property if you only want to change the key")
	}
	body := map[string]any{"name": name}
	setIfPresent(body, args, "key")
	return s.anytypeAPIRequest(http.MethodPatch,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties/"+url.PathEscape(propertyID), nil, body)
}

func (s *mcpServer) toolDeleteProperty(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodDelete,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties/"+url.PathEscape(propertyID), nil, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"space_id": spaceID, "property_id": propertyID, "archived": true}
	copyIfPresent(out, payload, "property")
	return out, nil
}

// --- tags ----------------------------------------------------------------

func (s *mcpServer) toolListTags(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return nil, err
	}
	query, offset, limit, err := paginationQuery(args, 100)
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties/"+url.PathEscape(propertyID)+"/tags", query, nil)
	if err != nil {
		return nil, err
	}
	full := optionalBool(args, "full", false)
	tags := pickFields(payload, full, "id", "key", "name", "color")
	return listEnvelope("tags", tags, payload, offset, limit,
		map[string]any{"space_id": spaceID, "property_id": propertyID}), nil
}

func (s *mcpServer) toolGetTag(args map[string]any) (map[string]any, error) {
	spaceID, propertyID, tagID, err := tagPathArgs(args)
	if err != nil {
		return nil, err
	}
	return s.anytypeAPIRequest(http.MethodGet, tagPath(spaceID, propertyID, tagID), nil, nil)
}

func (s *mcpServer) toolCreateTag(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return nil, err
	}
	name, err := requiredString(args, "name")
	if err != nil {
		return nil, err
	}
	color, err := requiredString(args, "color")
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name, "color": color}
	setIfPresent(body, args, "key")
	return s.anytypeAPIRequest(http.MethodPost,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/properties/"+url.PathEscape(propertyID)+"/tags", nil, body)
}

func (s *mcpServer) toolUpdateTag(args map[string]any) (map[string]any, error) {
	spaceID, propertyID, tagID, err := tagPathArgs(args)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	setIfPresent(body, args, "name")
	setIfPresent(body, args, "color")
	setIfPresent(body, args, "key")
	if len(body) == 0 {
		return nil, fmt.Errorf("update-tag needs at least one of name, color or key")
	}
	return s.anytypeAPIRequest(http.MethodPatch, tagPath(spaceID, propertyID, tagID), nil, body)
}

func (s *mcpServer) toolDeleteTag(args map[string]any) (map[string]any, error) {
	spaceID, propertyID, tagID, err := tagPathArgs(args)
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodDelete, tagPath(spaceID, propertyID, tagID), nil, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id":    spaceID,
		"property_id": propertyID,
		"tag_id":      tagID,
		"archived":    true,
	}
	copyIfPresent(out, payload, "tag")
	return out, nil
}

func tagPathArgs(args map[string]any) (string, string, string, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return "", "", "", err
	}
	propertyID, err := requiredString(args, "property_id")
	if err != nil {
		return "", "", "", err
	}
	tagID, err := requiredString(args, "tag_id")
	if err != nil {
		return "", "", "", err
	}
	return spaceID, propertyID, tagID, nil
}

func tagPath(spaceID, propertyID, tagID string) string {
	return "/v1/spaces/" + url.PathEscape(spaceID) +
		"/properties/" + url.PathEscape(propertyID) +
		"/tags/" + url.PathEscape(tagID)
}

// --- types ---------------------------------------------------------------

func (s *mcpServer) toolCreateType(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	name, err := requiredString(args, "name")
	if err != nil {
		return nil, err
	}
	pluralName, err := requiredString(args, "plural_name")
	if err != nil {
		return nil, err
	}
	layout, err := requiredString(args, "layout")
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name, "plural_name": pluralName, "layout": layout}
	setIfPresent(body, args, "key")
	setRawIfPresent(body, args, "icon")
	setRawIfPresent(body, args, "properties")
	return s.anytypeAPIRequest(http.MethodPost,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/types", nil, body)
}

func (s *mcpServer) toolUpdateType(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	setIfPresent(body, args, "name")
	setIfPresent(body, args, "plural_name")
	setIfPresent(body, args, "layout")
	setIfPresent(body, args, "key")
	setRawIfPresent(body, args, "icon")
	setRawIfPresent(body, args, "properties")
	if len(body) == 0 {
		return nil, fmt.Errorf("update-type needs at least one field to change")
	}
	if err := s.guardNewTypeProperties(spaceID, args); err != nil {
		return nil, err
	}
	return s.anytypeAPIRequest(http.MethodPatch,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/types/"+url.PathEscape(typeID), nil, body)
}

func (s *mcpServer) toolDeleteType(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodDelete,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/types/"+url.PathEscape(typeID), nil, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"space_id": spaceID, "type_id": typeID, "archived": true}
	copyIfPresent(out, payload, "type")
	return out, nil
}

// --- templates -----------------------------------------------------------

func (s *mcpServer) toolListTemplates(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}
	query, offset, limit, err := paginationQuery(args, 50)
	if err != nil {
		return nil, err
	}
	payload, err := s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/types/"+url.PathEscape(typeID)+"/templates", query, nil)
	if err != nil {
		return nil, err
	}
	templates := pickFields(payload, false, "id", "name", "icon", "object")
	return listEnvelope("templates", templates, payload, offset, limit,
		map[string]any{"space_id": spaceID, "type_id": typeID}), nil
}

func (s *mcpServer) toolGetTemplate(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	typeID, err := requiredString(args, "type_id")
	if err != nil {
		return nil, err
	}
	templateID, err := requiredString(args, "template_id")
	if err != nil {
		return nil, err
	}
	return s.anytypeAPIRequest(http.MethodGet,
		"/v1/spaces/"+url.PathEscape(spaceID)+"/types/"+url.PathEscape(typeID)+
			"/templates/"+url.PathEscape(templateID), nil, nil)
}

// withBatchParams adds the plural parameter and the batch controls to every
// tool that can take a list, and says so in its description. Doing it in one
// place keeps the wording and the shape identical across all of them.
func withBatchParams(defs []any) []any {
	for _, def := range defs {
		tool, ok := def.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		spec, batchable := restBatchable[name]
		if !batchable {
			continue
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			continue
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			continue
		}

		if spec.idKey != "" {
			props[spec.listKey] = map[string]any{
				"type":        "array",
				"description": fmt.Sprintf("Several %ss at once, instead of %s. Strongly preferred over calling this tool repeatedly.", spec.label, spec.idKey),
				"items":       map[string]any{"type": "string"},
			}
		} else {
			props[spec.listKey] = map[string]any{
				"type":        "array",
				"description": fmt.Sprintf("Several %ss at once. Each entry takes the same fields as a single call; anything set outside the list (such as space_id or property_id) applies to all of them. Strongly preferred over calling this tool repeatedly.", spec.label),
				"items":       batchItemSchema(props, spec),
			}
		}
		props["stop_on_error"] = map[string]any{
			"type":        "boolean",
			"description": "Stop at the first failure instead of carrying on with the rest.",
			"default":     false,
		}
		props["include_results"] = map[string]any{
			"type":        "boolean",
			"description": "Return the full result of every item. Off by default because a long list crowds out everything else; failures are always reported.",
			"default":     false,
		}

		// Fields that move into the batch entries stop being mandatory at the
		// top level; the handlers still demand them per item.
		//
		// That leaves a rule the schema cannot state: exactly one of the two
		// shapes has to be complete. JSON Schema could say it with oneOf, but
		// not every MCP client handles oneOf at the root of a tool schema, and
		// getting it wrong there costs the whole tool rather than one argument.
		// So the constraint is spelled out in the text instead — in the tool
		// description and again on each field that was relaxed, which is where
		// a model actually reads it — and the handlers keep enforcing it with
		// an error that names the missing field.
		if len(spec.relaxes) > 0 {
			relaxed := make(map[string]bool, len(spec.relaxes))
			for _, field := range spec.relaxes {
				relaxed[field] = true
			}
			if required, ok := schema["required"].([]string); ok {
				kept := make([]string, 0, len(required))
				for _, field := range required {
					if !relaxed[field] {
						kept = append(kept, field)
					}
				}
				schema["required"] = kept
			}
			for _, field := range spec.relaxes {
				entry, ok := props[field].(map[string]any)
				if !ok {
					continue
				}
				text, _ := entry["description"].(string)
				entry["description"] = strings.TrimSpace(text) + fmt.Sprintf(
					" REQUIRED unless you pass %s — it is optional here only because a batch carries it per entry instead.", spec.listKey)
			}
		}
		if description, ok := tool["description"].(string); ok {
			tool["description"] = description + fmt.Sprintf(
				" Takes a batch: pass %s to handle many in one call and get back how many succeeded and failed."+
					" Pass EITHER the single-item arguments OR %s, never neither: the schema cannot express that choice, so a call carrying only space_id validates and then fails with an error naming what is missing.",
				spec.listKey, spec.listKey)
		}
	}
	return defs
}

// --- batching ---------------------------------------------------------------
//
// Schema work is inherently repetitive: cleaning up a property means deleting
// forty tag options, seeding a type means creating a dozen. One MCP round trip
// per item is the wrong shape for that, so the tools below accept a list as
// well as a single item.
//
// Rather than adding a parallel set of *-many tools, the existing tool takes an
// optional plural parameter. That keeps one obvious tool per operation, and the
// batch path reuses the single-item handler verbatim, so the two can never
// drift apart.

type batchSpec struct {
	// listKey is the parameter carrying the batch.
	listKey string
	// idKey is set when the list holds plain ids: each one is written to this
	// parameter. When empty, the list holds objects that are merged into the
	// call instead.
	idKey string
	// label names one element in messages.
	label string
	// relaxes lists the parameters that stop being mandatory once a batch is
	// given, because they move into the individual entries.
	relaxes []string
}

var restBatchable = map[string]batchSpec{
	"delete-tag":         {listKey: "tag_ids", idKey: "tag_id", label: "tag", relaxes: []string{"tag_id"}},
	"update-tag":         {listKey: "items", label: "tag update", relaxes: []string{"tag_id", "name"}},
	"create-tag":         {listKey: "tags", label: "tag", relaxes: []string{"name", "color"}},
	"delete-property":    {listKey: "property_ids", idKey: "property_id", label: "property", relaxes: []string{"property_id"}},
	"update-property":    {listKey: "items", label: "property update", relaxes: []string{"property_id", "name"}},
	"create-property":    {listKey: "properties", label: "property", relaxes: []string{"name", "format"}},
	"delete-type":        {listKey: "type_ids", idKey: "type_id", label: "type", relaxes: []string{"type_id"}},
	"update-type":        {listKey: "items", label: "type update", relaxes: []string{"type_id"}},
	"create-type":        {listKey: "types", label: "type", relaxes: []string{"name", "plural_name", "layout"}},
	"remove-list-object": {listKey: "object_ids", idKey: "object_id", label: "object", relaxes: []string{"object_id"}},
}

// batchItemSchema types one entry of a batch instead of leaving it as a bare
// object, which told the model nothing about what an entry may contain.
//
// It is derived from the tool's own parameters rather than written out by hand,
// because that is literally what an entry is: batchArgs merges the entry over
// the top-level arguments and hands the result to the single-item handler, so
// every parameter of the tool is a valid field of an entry. Deriving it also
// means the two cannot drift apart when a parameter is added.
//
// The required set is spec.relaxes — the fields that stopped being mandatory at
// the top level precisely because they moved in here.
func batchItemSchema(props map[string]any, spec batchSpec) map[string]any {
	// Each entry is copied, not aliased. The caller goes on to append
	// "REQUIRED unless you pass <list>" to the relaxed top-level fields, and
	// that sentence is wrong inside a batch entry — there is no nested list
	// there and the field is plainly required, which the entry's own required
	// array already says. Sharing the maps would leak it in.
	fields := make(map[string]any, len(props))
	for key, value := range props {
		if key == spec.listKey || key == "stop_on_error" || key == "include_results" {
			continue
		}
		entry, ok := value.(map[string]any)
		if !ok {
			fields[key] = value
			continue
		}
		clone := make(map[string]any, len(entry))
		for k, v := range entry {
			clone[k] = v
		}
		fields[key] = clone
	}
	required := make([]any, 0, len(spec.relaxes))
	for _, field := range spec.relaxes {
		if _, ok := fields[field]; ok {
			required = append(required, field)
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           fields,
		"required":             required,
		"additionalProperties": false,
	}
}

// batchArgs turns one element of the batch into a full set of tool arguments.
func batchArgs(base map[string]any, spec batchSpec, element any) (map[string]any, error) {
	args := make(map[string]any, len(base)+2)
	for k, v := range base {
		if k == spec.listKey || k == "stop_on_error" || k == "include_results" {
			continue // batch controls are not arguments of the single call
		}
		args[k] = v
	}
	if spec.idKey != "" {
		id, ok := element.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of id strings", spec.listKey)
		}
		args[spec.idKey] = id
		return args, nil
	}
	item, ok := element.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of objects", spec.listKey)
	}
	for k, v := range item {
		args[k] = v
	}
	return args, nil
}

// runRestBatch applies a single-item handler across a list and reports totals.
//
// Per-item detail is omitted unless asked for: a hundred results would crowd
// out everything else in the caller's context, while the counts and the failures
// are what actually decide the next step.
func (s *mcpServer) runRestBatch(name string, spec batchSpec, args map[string]any, elements []any) (map[string]any, error) {
	stopOnError := optionalBool(args, "stop_on_error", false)
	includeResults := optionalBool(args, "include_results", false)

	succeeded, failed := 0, 0
	results := make([]map[string]any, 0, len(elements))
	failures := make([]map[string]any, 0)

	for i, element := range elements {
		itemArgs, err := batchArgs(args, spec, element)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", spec.listKey, i, err)
		}
		res, err, handled := s.dispatchRestSingle(name, itemArgs)
		if !handled {
			return nil, fmt.Errorf("unknown tool %q", name)
		}
		entry := map[string]any{"index": i}
		if spec.idKey != "" {
			entry["id"] = itemArgs[spec.idKey]
		}
		if err != nil {
			failed++
			entry["error"] = err.Error()
			failures = append(failures, entry)
			if stopOnError {
				results = append(results, entry)
				break
			}
		} else {
			succeeded++
			if includeResults {
				entry["result"] = res
			}
		}
		results = append(results, entry)
	}

	out := map[string]any{
		"tool": name, "requested": len(elements),
		"succeeded": succeeded, "failed": failed,
	}
	if includeResults {
		out["results"] = results
	}
	if failed > 0 {
		out["failures"] = failures
		if stopOnError {
			out["note"] = "stopped at the first failure; the items after it were not attempted"
		}
	}
	return out, nil
}

// guardNewTypeProperties refuses an update-type whose properties list names a
// property the space does not have, unless the caller asked for that.
//
// Only update-type gets the guard, and the asymmetry with create-type is the
// point. On a new type, listing properties that do not exist yet is the useful
// shape — Recipe with cooking_time and calories in one call, instead of two
// create-property round trips first. On an existing type it is almost always a
// misspelling, and it lands twice: "due_dat" both fails to link due_date AND
// leaves a new space-wide property called Due Dat behind. The replace semantics
// of the list make the first half silent, because the property the caller meant
// simply disappears from the type.
//
// Off by default rather than on, because the damage is not undone by noticing
// it later: the stray property stays in the space and every type picker shows
// it. Set allow_new_properties=true for the case where a new property really is
// meant to be created here.
//
// The check costs one gRPC search of the space's property definitions, and only
// on calls that carry a properties list without the flag. A batch pays it per
// entry, because the batch path runs the single-item handler verbatim — setting
// allow_new_properties once at the top level skips it for the whole batch.
func (s *mcpServer) guardNewTypeProperties(spaceID string, args map[string]any) error {
	raw, ok := args["properties"]
	if !ok || raw == nil {
		return nil
	}
	if optionalBool(args, "allow_new_properties", false) {
		return nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("properties must be a list of {key, name?, format?} objects")
	}
	keys := make([]string, 0, len(entries))
	for i, element := range entries {
		entry, ok := element.(map[string]any)
		if !ok {
			return fmt.Errorf("properties[%d]: must be an object with a key", i)
		}
		if key := stringValue(entry["key"]); key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}

	// Fail closed, but say how to get past it. The check needs gRPC, which this
	// REST tool did not before; if it is unreachable the guard cannot tell a
	// typo from a real key, and silently letting the call through would defeat
	// the point of having it off by default.
	client, err := s.grpcClient()
	if err != nil {
		return fmt.Errorf("cannot verify the property keys against the space: %w. "+
			"Pass allow_new_properties=true to skip the check, accepting that unknown keys create new properties", err)
	}
	defer client.Close()

	unknown, err := client.UnknownPropertyKeys(context.Background(), spaceID, keys)
	if err != nil {
		return fmt.Errorf("cannot verify the property keys against the space: %w. "+
			"Pass allow_new_properties=true to skip the check, accepting that unknown keys create new properties", err)
	}
	if len(unknown) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(unknown))
	for _, key := range unknown {
		quoted = append(quoted, fmt.Sprintf("%q%s", key, client.SuggestPropertyKey(context.Background(), spaceID, key)))
	}
	return fmt.Errorf(
		"properties names %d key(s) this space has no property for: %s. "+
			"update-type would not refuse them — Anytype would CREATE a property for each one, "+
			"so a typo both fails to link the property you meant and adds a new one to the space. "+
			"Fix the spelling (list-properties shows the real ones), or pass allow_new_properties=true "+
			"if a new property really should be created here",
		len(unknown), strings.Join(quoted, ", "))
}
