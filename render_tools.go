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
			"other views, filter and sort counts, columns, cut-off cells and rows shown of the total; text cut off at the side, images that failed to load; " +
			"complete, and why not). The report holds what the image shows in numbers; read it first. " +
			"An embedded view has its own view settings and shows its first rows (10 by default) above a Load more button. " +
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
	views, err := renderViews(args)
	if err != nil {
		return nativeToolResult{}, err
	}

	fingerprint, toggles, err := s.renderInputs(spaceID, objectID)
	if err != nil {
		return nativeToolResult{}, err
	}
	viewsKey, _ := json.Marshal(views)
	key := fmt.Sprintf("%s|%s|%d|%s|%s|%t|%t|%t|%t|%s|%s", spaceID, objectID, width, theme, format, expand, fullHeight, allRows, wantImage, viewsKey, fingerprint)
	now := time.Now()
	if memo, ok := s.recentRender(key, now); ok {
		s.note("reused", "render")
		return memo, nil
	}

	var expandIDs []string
	if expand {
		expandIDs = toggles
	}

	rendered, err := s.callRenderer(map[string]any{
		"space_id": spaceID, "object_id": objectID, "width": width, "theme": theme,
		"format": format, "full_height": fullHeight, "expand_ids": expandIDs,
		"all_rows": allRows, "image": wantImage, "views": views,
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
	if rendered.Complete != nil {
		out["complete"] = *rendered.Complete
		if !*rendered.Complete {
			out["incomplete_reason"] = incompleteReason(rendered.Truncated, fullHeight)
		}
	}
	var warnings []string
	if len(rendered.RPC.Refused) > 0 {
		warnings = append(warnings, "the renderer refused calls the page made, so parts may be missing: "+strings.Join(rendered.RPC.Refused, ", "))
	}
	if len(rendered.Unsettled) > 0 {
		warnings = append(warnings, "captured while still loading ("+strings.Join(rendered.Unsettled, ", ")+"), so parts may be missing or not yet in place")
	}
	if n := partlyShownViews(rendered.Layout); !allRows && fullHeight && n > 0 {
		warnings = append(warnings, fmt.Sprintf("%d embedded views show only their first rows; all_rows loads the rest", n))
	}
	if closed, _ := rendered.Layout["toggles_not_open"].([]any); expand && len(closed) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d of %d toggles did not open, so their content is not in the image", len(closed), len(toggles)))
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

// incompleteReason says why the image stops above the end of the object.
func incompleteReason(truncated, fullHeight bool) string {
	switch {
	case truncated:
		return "the object is taller than the renderer's height limit; the image stops there"
	case !fullHeight:
		return "full_height is false: the image shows the first screen only"
	default:
		return "the content continues below the image"
	}
}

// partlyShownViews counts embedded views that hold more rows than they show.
// The object's own view ("dataview") loads up to 500 rows without a button.
func partlyShownViews(layout map[string]any) int {
	views, _ := layout["dataviews"].([]any)
	n := 0
	for _, v := range views {
		dv, _ := v.(map[string]any)
		shown, _ := dv["rows_shown"].(float64)
		total, ok := dv["rows_total"].(float64)
		if ok && dv["block_id"] != "dataview" && shown < total {
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
// repeat check, and the blocks the GUI can fold, for expand_toggles. Reading
// it also turns a wrong id into an error before the browser starts.
func (s *mcpServer) renderInputs(spaceID, objectID string) (string, []string, error) {
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
	var toggles []string
	for _, b := range blocks {
		if b.Kind == "text" && strings.HasPrefix(b.Style, "toggle") {
			toggles = append(toggles, b.ID)
		}
	}
	return sha256Hex(encoded), toggles, nil
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
