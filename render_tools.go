package main

// object-render: an object as the Anytype GUI shows it.
//
// The rendering happens in a separate daemon (anytype-render) that drives the
// real anytype-ts frontend in a headless browser against the same heart. This
// server only forwards the request, hands the image over through the export
// cache, and passes on the daemon's layout report — measured widths, wrapped
// and clipped text — which is often worth more to a model than the picture.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

const (
	defaultRenderWidth = 1280
	minRenderWidth     = 480
	maxRenderWidth     = 3840
	renderCallTimeout  = 120 * time.Second

	// ChatGPT sends many calls twice, 10-50 s apart. A repeat within this
	// window gets the first answer instead of a second browser run, but only
	// while the object's blocks are unchanged: render, fix, render again is
	// the normal loop and must never return the old picture.
	renderReuseWindow = 90 * time.Second

	// The renderer's cap for all_rows (anytype-render ROW_CAP).
	renderRowCap = 500
)

type renderMemo struct {
	key    string
	at     time.Time
	result nativeToolResult
}

func (s *mcpServer) renderToolDefs() []any {
	return []any{map[string]any{
		"name": "object-render",
		"description": "Render an object as the Anytype GUI shows it, with the real Anytype frontend: an image link plus a layout report " +
			"(table column widths, wrapped and clipped cells, px cut off at the side; per query or collection view its source, view name, " +
			"other views, applied filters (and stored ones that filter nothing or that the GUI ignores) and sorts, both also in words as the GUI labels them (filter_list, sort_list), columns with cut-off cells and the width they need, rows shown of the total; " +
			"text cut off at the side, images that failed to load). complete is true only when nothing is hidden: below the image, " +
			"behind Load more, in closed toggles or past the side (cell text cut short with an ellipsis does not count); " +
			"incomplete_reasons says what is and how to show it. " +
			"The report holds what the image shows in numbers; read it first. " +
			"An embedded view has its own views, and only their filters apply; it shows its first rows (10 by default) above a Load more button. " +
			"Opening the object sets its last-opened date, nothing else is written.",
		"inputSchema": restSchema([]string{"space_id", "object_id"}, map[string]any{
			"space_id":  spaceIDProp(),
			"object_id": strProp("Object to render: a page, a query, a collection or any other layout."),
			"width": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Window width in px, %d-%d. Defaults to %d. Page text stays about 700 px wide unless the type uses full width.", minRenderWidth, maxRenderWidth, defaultRenderWidth),
			},
			"theme":          enumProp("Defaults to light.", []string{"light", "dark"}),
			"expand_toggles": map[string]any{"type": "boolean", "description": "Show every toggle and toggle heading open. Defaults to false, as a page first opens.", "default": false},
			"full_height":    map[string]any{"type": "boolean", "description": "Capture the whole object instead of the first screen. Defaults to true.", "default": true},
			"all_rows":       map[string]any{"type": "boolean", "description": fmt.Sprintf("Press Load more in embedded views until every row shows, up to %d per view. Defaults to false.", renderRowCap), "default": false},
			"image":          map[string]any{"type": "boolean", "description": "false returns the layout report only, without an image. Defaults to true.", "default": true},
			"show_filters":   map[string]any{"type": "boolean", "description": "Open the filter and sort bar of every query or collection view, as its filter button does, so the image shows each filter. Defaults to false.", "default": false},
			"views": map[string]any{
				"type": "array",
				"description": "Show other views than the active ones, as a click on the view tab does, without saving the switch. " +
					"block_id of the query or collection as the report names it (\"dataview\" for the object's own); it may be left out when the object shows only one.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"block_id": strProp("Block of the query or collection."),
						"view":     strProp("View name or id."),
					},
					"required": []string{"view"},
				},
			},
			"format": enumProp("Defaults to jpeg; png is sharper for small text and larger.", []string{"jpeg", "png"}),
		}),
	}}
}

type renderResponse struct {
	Image     string `json:"image"`
	MIME      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Truncated bool   `json:"truncated"`
	// Complete is false when the image stops above the end of the content;
	// nil from a daemon that does not measure it.
	Complete *bool `json:"complete"`
	// Unsettled names what was still loading when the picture was taken:
	// network, fonts, images.
	Unsettled []string `json:"unsettled"`
	Theme     string   `json:"theme"`
	Ms        int      `json:"ms"`
	Renderer  struct {
		Frontend   string `json:"frontend"`
		Middleware string `json:"middleware"`
	} `json:"renderer"`
	Layout map[string]any `json:"layout"`
	RPC    struct {
		Refused []string `json:"refused"`
	} `json:"rpc"`
	PageErrors []string `json:"page_errors"`
	Error      string   `json:"error"`
}

func (s *mcpServer) toolObjectRender(args map[string]any) (nativeToolResult, error) {
	if s.cfg.renderURL == "" {
		return nativeToolResult{}, errors.New("no renderer configured: object-render needs the anytype-render daemon and ANYTYPE_RENDER_URL pointing at its API")
	}
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nativeToolResult{}, err
	}
	objectID, err := requiredString(args, "object_id")
	if err != nil {
		return nativeToolResult{}, err
	}
	width := optionalInt(args, "width", defaultRenderWidth)
	if width < minRenderWidth || width > maxRenderWidth {
		return nativeToolResult{}, fmt.Errorf("width must be %d-%d px", minRenderWidth, maxRenderWidth)
	}
	theme := strings.ToLower(optionalString(args, "theme"))
	if theme == "" {
		theme = "light"
	}
	if theme != "light" && theme != "dark" {
		return nativeToolResult{}, errors.New("theme must be light or dark")
	}
	format := strings.ToLower(optionalString(args, "format"))
	if format == "" || format == "jpg" {
		format = "jpeg"
	}
	if format != "jpeg" && format != "png" {
		return nativeToolResult{}, errors.New("format must be jpeg or png")
	}
	expand := optionalBool(args, "expand_toggles", false)
	fullHeight := optionalBool(args, "full_height", true)
	allRows := optionalBool(args, "all_rows", false)
	wantImage := optionalBool(args, "image", true)
	showFilters := optionalBool(args, "show_filters", false)
	views, err := renderViews(args)
	if err != nil {
		return nativeToolResult{}, err
	}

	fingerprint, blocks, err := s.renderInputs(spaceID, objectID)
	if err != nil {
		return nativeToolResult{}, err
	}
	viewsKey, _ := json.Marshal(views)
	key := fmt.Sprintf("%s|%s|%d|%s|%s|%t|%t|%t|%t|%t|%s|%s", spaceID, objectID, width, theme, format, expand, fullHeight, allRows, wantImage, showFilters, viewsKey, fingerprint)
	now := time.Now()
	if memo, ok := s.recentRender(key, now); ok {
		s.note("reused", "render")
		return memo, nil
	}

	var expandIDs []string
	if expand {
		expandIDs = toggleIDs(blocks)
	}

	rendered, err := s.callRenderer(map[string]any{
		"space_id": spaceID, "object_id": objectID, "width": width, "theme": theme,
		"format": format, "full_height": fullHeight, "expand_ids": expandIDs,
		"all_rows": allRows, "image": wantImage, "views": views, "show_filters": showFilters,
	})
	if err != nil {
		return nativeToolResult{}, err
	}
	s.note("render_ms", rendered.Ms)

	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"width_px": rendered.Width, "height_px": rendered.Height, "theme": rendered.Theme,
		"layout":   compactLayout(rendered.Layout),
		"renderer": strings.TrimSpace("anytype-ts " + rendered.Renderer.Frontend),
	}
	var content []map[string]any
	if wantImage {
		image, err := base64.StdEncoding.DecodeString(rendered.Image)
		if err != nil || len(image) == 0 {
			return nativeToolResult{}, errors.New("the renderer returned no image")
		}
		if len(image) > maxExportBytes {
			return nativeToolResult{}, fmt.Errorf("the image is %d bytes, above the %d-byte export limit; use format jpeg, a smaller width or full_height false", len(image), maxExportBytes)
		}
		ext := "jpg"
		if format == "png" {
			ext = "png"
		}
		short := objectID
		if len(short) > 16 {
			short = short[len(short)-16:]
		}
		exported, err := s.exportBytes(objectID, fmt.Sprintf("render-%s-%dpx-%s.%s", short, width, theme, ext), image, now)
		if err != nil {
			return nativeToolResult{}, err
		}
		out["uri"], out["expires_at"] = exported.Metadata["uri"], exported.Metadata["expires_at"]
		content = exported.Content
	}
	if rendered.Truncated {
		out["truncated"] = true
	}
	// A daemon without the height measurement cannot say whether it is all.
	if rendered.Complete != nil {
		reasons := hiddenContent(rendered, blocks, fullHeight, allRows, expand)
		out["complete"] = len(reasons) == 0
		if len(reasons) > 0 {
			out["incomplete_reasons"] = reasons
		}
	}
	var warnings []string
	if len(rendered.RPC.Refused) > 0 {
		warnings = append(warnings, "the renderer refused calls the page made, so parts may be missing: "+strings.Join(rendered.RPC.Refused, ", "))
	}
	if len(rendered.Unsettled) > 0 {
		warnings = append(warnings, "captured while still loading ("+strings.Join(rendered.Unsettled, ", ")+"), so parts may be missing or not yet in place")
	}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	result := nativeToolResult{Metadata: out, Content: content}
	s.rememberRender(key, now, result)
	return result, nil
}

// renderViews reads the views argument: which view to show per query or
// collection block. The renderer resolves names and block ids, since it sees
// what the page shows.
func renderViews(args map[string]any) ([]map[string]string, error) {
	raw, present := args["views"]
	if !present || raw == nil {
		return []map[string]string{}, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("views must be a list of {block_id, view}")
	}
	if len(list) > 50 {
		return nil, errors.New("views takes at most 50 entries")
	}
	out := make([]map[string]string, 0, len(list))
	for i, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("views[%d] must be an object with view and, if the object shows several queries or collections, block_id", i)
		}
		view := strings.TrimSpace(optionalString(entry, "view"))
		if view == "" {
			return nil, fmt.Errorf("views[%d].view is required: a view name or id", i)
		}
		out = append(out, map[string]string{"block_id": strings.TrimSpace(optionalString(entry, "block_id")), "view": view})
	}
	return out, nil
}

// hiddenContent lists what the image does not show, each with the way to
// show it: content below the image, rows behind Load more, closed toggles
// with content, and content cut off at the side. Text a cell cuts short is
// not in it: the report counts that per column, and nearly every grid has it.
func hiddenContent(r renderResponse, blocks []anytypefiles.BlockInfo, fullHeight, allRows, expand bool) []string {
	reasons := []string{}
	if r.Complete != nil && !*r.Complete {
		switch {
		case r.Truncated:
			reasons = append(reasons, "taller than the renderer's height limit: the image stops there")
		case !fullHeight:
			reasons = append(reasons, "only the first screen is in the image (full_height)")
		default:
			reasons = append(reasons, "content continues below the image")
		}
	}

	// Rows a view has but did not load. A "Load more" button is the GUI's
	// sign that more can be shown; past 500 per view all_rows stops.
	var behind, capped, slow, other int
	views, _ := r.Layout["dataviews"].([]any)
	for _, v := range views {
		dv, _ := v.(map[string]any)
		total, okTotal := dv["rows_total"].(float64)
		loaded, okLoaded := dv["rows_loaded"].(float64)
		if !okTotal || !okLoaded || loaded >= total {
			continue
		}
		missing := int(total - loaded)
		button, _ := dv["load_more"].(bool)
		switch {
		case button && !allRows:
			behind += missing
		case int(loaded) >= renderRowCap:
			capped += missing
		case button:
			slow += missing
		default:
			other += missing
		}
	}
	if behind > 0 {
		reasons = append(reasons, fmt.Sprintf("rows behind Load more: %d (all_rows)", behind))
	}
	if capped > 0 {
		reasons = append(reasons, fmt.Sprintf("rows beyond the %d loaded per view: %d", renderRowCap, capped))
	}
	if slow > 0 {
		reasons = append(reasons, fmt.Sprintf("rows that did not load in time: %d", slow))
	}
	if other > 0 {
		reasons = append(reasons, fmt.Sprintf("rows not loaded: %d", other))
	}

	closed := map[string]bool{}
	if expand {
		notOpen, _ := r.Layout["toggles_not_open"].([]any)
		for _, id := range notOpen {
			if s, ok := id.(string); ok {
				closed[s] = true
			}
		}
	} else {
		for _, id := range toggleIDs(blocks) {
			closed[id] = true
		}
	}
	if n := foldedToggles(blocks, closed); n > 0 {
		if expand {
			reasons = append(reasons, fmt.Sprintf("toggles that did not open: %d", n))
		} else {
			reasons = append(reasons, fmt.Sprintf("closed toggles with content: %d (expand_toggles)", n))
		}
	}

	var cutOff, widest int
	for _, list := range []string{"tables", "dataviews"} {
		items, _ := r.Layout[list].([]any)
		for _, item := range items {
			m, _ := item.(map[string]any)
			if px, _ := m["cut_off_px"].(float64); px > 0 {
				cutOff++
				widest = max(widest, int(px))
			}
		}
	}
	if cutOff > 0 {
		reasons = append(reasons, fmt.Sprintf("tables and views cut off at the side: %d, by up to %d px (a wider window, or narrower columns)", cutOff, widest))
	}
	if clipped, _ := r.Layout["clipped_blocks"].([]any); len(clipped) > 0 {
		reasons = append(reasons, fmt.Sprintf("text blocks running past the side: %d", len(clipped)))
	}
	return reasons
}

// toggleIDs are the blocks the GUI can fold: toggles and toggle headings.
func toggleIDs(blocks []anytypefiles.BlockInfo) []string {
	var ids []string
	for _, b := range blocks {
		if b.Kind == "text" && strings.HasPrefix(b.Style, "toggle") {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

// foldedToggles counts the closed toggles that hide something: those with
// children, and not themselves inside a closed toggle.
func foldedToggles(blocks []anytypefiles.BlockInfo, closed map[string]bool) int {
	parent := map[string]string{}
	children := map[string]int{}
	for _, b := range blocks {
		children[b.ID] = len(b.ChildrenIDs)
		for _, c := range b.ChildrenIDs {
			parent[c] = b.ID
		}
	}
	n := 0
	for id := range closed {
		if children[id] == 0 {
			continue
		}
		inside := false
		for p := parent[id]; p != "" && !inside; p = parent[p] {
			inside = closed[p]
		}
		if !inside {
			n++
		}
	}
	return n
}

// compactLayout keeps what a model can act on and drops what is empty.
func compactLayout(layout map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range layout {
		switch v := value.(type) {
		case nil:
			continue
		case []any:
			if len(v) == 0 {
				continue
			}
		case float64:
			if v == 0 && key != "content_width_px" {
				continue
			}
		}
		if key == "blocks_in_dom" {
			continue
		}
		out[key] = value
	}
	return out
}

func (s *mcpServer) callRenderer(request map[string]any) (renderResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return renderResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), renderCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.renderURL, "/")+"/render", bytes.NewReader(body))
	if err != nil {
		return renderResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return renderResponse{}, fmt.Errorf("renderer unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return renderResponse{}, err
	}
	var out renderResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return renderResponse{}, fmt.Errorf("renderer answered %d with no readable body", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error == "" {
			out.Error = resp.Status
		}
		return renderResponse{}, fmt.Errorf("render failed: %s", out.Error)
	}
	return out, nil
}

// renderInputs reads the object once: a fingerprint of its blocks for the
// repeat check, and the blocks themselves, for the toggles. Reading it also
// turns a wrong id into an error before the browser starts.
func (s *mcpServer) renderInputs(spaceID, objectID string) (string, []anytypefiles.BlockInfo, error) {
	client, err := s.grpcClient()
	if err != nil {
		return "", nil, err
	}
	defer client.Close()
	blocks, _, err := client.ReadBlocks(context.Background(), spaceID, objectID)
	if err != nil {
		return "", nil, err
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return "", nil, err
	}
	return sha256Hex(encoded), blocks, nil
}

// exportBytes puts bytes the server made itself into the export cache, through
// a private staging directory as createFileExport expects.
func (s *mcpServer) exportBytes(objectID, name string, data []byte, now time.Time) (nativeToolResult, error) {
	outRoot, err := ensureDir(s.cfg.outRoot)
	if err != nil {
		return nativeToolResult{}, err
	}
	staging, err := os.MkdirTemp(outRoot, ".mcp-render-")
	if err != nil {
		return nativeToolResult{}, err
	}
	defer os.RemoveAll(staging)
	path := filepath.Join(staging, "image")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nativeToolResult{}, err
	}
	return s.createFileExport(objectID, name, staging, path, now, nil)
}

func (s *mcpServer) recentRender(key string, now time.Time) (nativeToolResult, bool) {
	for _, memo := range s.renders {
		if memo.key == key && now.Sub(memo.at) < renderReuseWindow {
			return memo.result, true
		}
	}
	return nativeToolResult{}, false
}

func (s *mcpServer) rememberRender(key string, now time.Time, result nativeToolResult) {
	kept := s.renders[:0]
	for _, memo := range s.renders {
		if now.Sub(memo.at) < renderReuseWindow && memo.key != key {
			kept = append(kept, memo)
		}
	}
	s.renders = append(kept, renderMemo{key: key, at: now, result: result})
}
