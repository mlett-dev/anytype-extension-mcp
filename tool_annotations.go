package main

// MCP tool annotations (spec 2025-11-25, ToolAnnotations). Clients that see no
// annotations assume a tool writes, destroys and reaches the open world, so
// every tool states all three hints explicitly.
//
// readOnly:    changes nothing in Anytype or on disk.
// additive:    writes, but only adds (create, insert, duplicate, pin, upload).
// destructive: changes, replaces, reorders, archives or removes existing
//              state — also when that only happens behind an optional flag
//              (apply, confirm, overwrite, update_existing, ...).
//
// openWorld marks tools that fetch from outside Anytype and this server: web
// URLs, Unsplash, Notion, or a client-supplied download_url.

type toolKind int

const (
	readOnly toolKind = iota
	additive
	destructive
)

type toolHints struct {
	kind      toolKind
	openWorld bool
}

func (h toolHints) annotations() map[string]any {
	return map[string]any{
		"readOnlyHint":    h.kind == readOnly,
		"destructiveHint": h.kind == destructive,
		"openWorldHint":   h.openWorld,
	}
}

var (
	hintReadOnly    = toolHints{kind: readOnly}
	hintAdditive    = toolHints{kind: additive}
	hintDestructive = toolHints{kind: destructive}
)

func openWorldTool(h toolHints) toolHints { h.openWorld = true; return h }

var toolAnnotations = map[string]toolHints{
	// Local files and transfer.
	"file-info":                        hintReadOnly,
	"file-list-input":                  hintReadOnly,
	"file-list-output":                 hintReadOnly,
	"file-upload":                      hintAdditive,
	"file-upload-many":                 hintAdditive,
	"file-download":                    hintDestructive, // target_name renames over an existing file
	"file-download-many":               hintDestructive,
	"file-export":                      hintReadOnly, // only writes its own expiring cache
	"file-export-many":                 hintReadOnly,
	"pdf-inspect":                      hintReadOnly,
	"file-upload-attachment":           openWorldTool(hintAdditive),
	"file-stage-attachment":            openWorldTool(hintDestructive), // overwrite
	"object-set-cover-from-attachment": openWorldTool(hintDestructive),
	"debug-receive-file":               openWorldTool(hintReadOnly), // fetch_reference
	"debug-receive-openai-file":        openWorldTool(hintReadOnly), // fetches download_url
	"server-info":                      hintReadOnly,

	// Compact REST wrappers.
	"get-list-objects-compact":    hintReadOnly,
	"get-object-compact":          hintReadOnly,
	"get-objects-compact-many":    hintReadOnly,
	"create-objects-compact-many": hintAdditive,
	"update-object-compact":       hintDestructive,
	"update-objects-compact-many": hintDestructive,
	"search-global-compact":       hintReadOnly,
	"search-space-compact":        hintReadOnly,
	"list-objects-compact":        hintReadOnly,
	"list-types-compact":          hintReadOnly,
	"get-type-compact":            hintReadOnly,
	"get-list-views-compact":      hintReadOnly,

	// REST: spaces, members, schema, templates, collections.
	"list-spaces":        hintReadOnly,
	"get-space":          hintReadOnly,
	"create-space":       hintAdditive,
	"update-space":       hintDestructive,
	"delete-object":      hintDestructive,
	"add-list-objects":   hintAdditive,
	"remove-list-object": hintDestructive,
	"list-members":       hintReadOnly,
	"get-member":         hintReadOnly,
	"list-properties":    hintReadOnly,
	"get-property":       hintReadOnly,
	"create-property":    hintAdditive,
	"update-property":    hintDestructive,
	"delete-property":    hintDestructive,
	"list-tags":          hintReadOnly,
	"get-tag":            hintReadOnly,
	"create-tag":         hintAdditive,
	"update-tag":         hintDestructive,
	"delete-tag":         hintDestructive,
	"create-type":        hintAdditive,
	"update-type":        hintDestructive,
	"delete-type":        hintDestructive,
	"list-templates":     hintReadOnly,
	"get-template":       hintReadOnly,

	// Queries and views.
	"query-create":         hintAdditive,
	"query-inspect":        hintReadOnly,
	"query-set-source":     hintDestructive,
	"query-view-create":    hintAdditive,
	"query-view-update":    hintDestructive,
	"query-view-delete":    hintDestructive,
	"query-view-arrange":   hintDestructive,
	"query-filter-add":     hintAdditive,
	"query-filter-remove":  hintDestructive,
	"query-sort-add":       hintAdditive,
	"query-sort-remove":    hintDestructive,
	"query-order-set":      hintDestructive,
	"object-to-query":      hintDestructive,
	"object-to-collection": hintDestructive,

	// Blocks.
	"block-list":              hintReadOnly,
	"block-create":            hintAdditive,
	"block-set-text":          hintDestructive,
	"block-turn-into":         hintDestructive,
	"block-set-checked":       hintDestructive,
	"block-mark":              hintDestructive,
	"block-move":              hintDestructive,
	"block-duplicate":         hintAdditive,
	"block-delete":            hintDestructive,
	"block-paste":             hintDestructive, // replace_block_ids
	"block-split":             hintDestructive,
	"block-merge":             hintDestructive,
	"block-style":             hintDestructive,
	"block-column-width":      hintDestructive,
	"block-file-create":       openWorldTool(hintAdditive), // url
	"block-relation-add":      hintAdditive,
	"block-extract-to-object": hintDestructive,
	"block-link-appearance":   hintDestructive,
	"block-embed-create":      hintAdditive,
	"block-embed-set-text":    hintDestructive,
	"block-embed-query":       hintAdditive,
	"block-export":            hintReadOnly,

	// Tables.
	"table-inspect":    hintReadOnly,
	"table-set-cells":  hintDestructive,
	"table-insert":     hintAdditive,
	"table-delete":     hintDestructive,
	"table-duplicate":  hintAdditive,
	"table-move":       hintDestructive,
	"table-row-header": hintDestructive,
	"table-row-clear":  hintDestructive,
	"table-sort":       hintDestructive,

	// Objects: state, history, export/import, templates.
	"object-set-archived":          hintDestructive,
	"object-set-favorite":          hintDestructive,
	"object-undo":                  hintDestructive,
	"object-export":                hintReadOnly,
	"object-export-files":          hintDestructive,                // writes into the output root
	"object-import":                openWorldTool(hintDestructive), // update_existing, Notion
	"object-versions":              hintReadOnly,
	"object-version-show":          hintReadOnly,
	"object-version-restore":       hintDestructive,
	"object-version-diff":          hintReadOnly,
	"object-duplicate":             hintAdditive,
	"template-create":              hintAdditive,
	"type-set-featured-properties": hintDestructive,
	"objects-modify-property":      hintDestructive,
	"object-apply-template":        hintDestructive,
	"object-create-from-url":       openWorldTool(hintAdditive),
	"object-graph":                 hintReadOnly,
	"object-date":                  hintReadOnly,
	"object-flags":                 hintReadOnly,
	"object-delete-permanently":    hintDestructive,

	// Covers and images.
	"unsplash-search":            openWorldTool(hintReadOnly),
	"unsplash-download":          openWorldTool(hintAdditive),
	"object-set-cover":           hintDestructive,
	"object-set-cover-from-file": hintDestructive,
	"object-get-cover":           hintReadOnly,

	// Space settings and schema layout.
	"space-file-usage":     hintReadOnly,
	"space-set-homepage":   hintDestructive,
	"space-get-homepage":   hintReadOnly,
	"get-type-layout":      hintReadOnly,
	"type-set-layout":      hintDestructive,
	"schema-set-order":     hintDestructive,
	"space-system-objects": hintReadOnly,

	// Sidebar.
	"sidebar-inspect":       hintReadOnly,
	"sidebar-widget-add":    hintAdditive,
	"sidebar-widget-update": hintDestructive,
	"sidebar-widget-remove": hintDestructive,
	"sidebar-arrange":       hintDestructive,
	"sidebar-snapshot":      hintReadOnly,
	"sidebar-apply":         hintDestructive, // apply
	"sidebar-prune":         hintDestructive, // apply

	// Usage analysis.
	"analyze-property-usage": hintReadOnly,
	"list-archived":          hintReadOnly,
	"analyze-schema-usage":   hintReadOnly,
	"clean-unused-tags":      hintDestructive, // confirm
}

// annotateTools sets each definition's annotations from toolAnnotations.
func annotateTools(defs []any) []any {
	for _, def := range defs {
		m, ok := def.(map[string]any)
		if !ok {
			continue
		}
		if hints, ok := toolAnnotations[m["name"].(string)]; ok {
			m["annotations"] = hints.annotations()
		}
	}
	return defs
}
