package main

// Making an icon write take effect, and reporting honestly when it does not.
//
// anytype-heart accepts an icon write, stores it, and then may still report the
// old icon: the icon lives in several detail relations at once and the reader
// resolves them by a fixed precedence, so an emoji left over from before hides
// a newly written file icon. See anytypefiles/icons.go for the mechanism.
//
// Every tool that writes an icon therefore checks what the API reports back
// afterwards. The check itself is free: anytype-heart's create and update
// handlers both end in a real read of the object, so the response already
// carries the truth. What it cannot see is the icon that is stored but losing,
// which is why the relations are normalised on every icon write rather than
// only on the writes that visibly failed.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// iconTarget is the thing an icon was written to, and how to read it back.
type iconTarget struct {
	objectID string

	// reread fetches the current state of the target from the REST API.
	reread func() (map[string]any, error)
}

func (s *mcpServer) objectIconTarget(spaceID, objectID string) iconTarget {
	return iconTarget{
		objectID: objectID,
		reread: func() (map[string]any, error) {
			payload, err := s.anytypeAPIRequest(http.MethodGet,
				"/v1/spaces/"+url.PathEscape(spaceID)+"/objects/"+url.PathEscape(objectID), nil, nil)
			if err != nil {
				return nil, err
			}
			return objectFromPayload(payload, "object")
		},
	}
}

func (s *mcpServer) typeIconTarget(spaceID, typeID string) iconTarget {
	return iconTarget{
		objectID: typeID,
		reread: func() (map[string]any, error) {
			payload, err := s.anytypeAPIRequest(http.MethodGet,
				"/v1/spaces/"+url.PathEscape(spaceID)+"/types/"+url.PathEscape(typeID), nil, nil)
			if err != nil {
				return nil, err
			}
			return objectFromPayload(payload, "type")
		},
	}
}

// iconValueKeys names the field that carries the actual icon, per format.
var iconValueKeys = map[string]string{
	anytypefiles.IconFormatEmoji: "emoji",
	anytypefiles.IconFormatFile:  "file",
	anytypefiles.IconFormatNamed: "name",
}

// requestedIconFormat returns the icon format a create or update call asked
// for, or "" when the call carried no icon and there is nothing to verify.
//
// An icon with an empty value is a request to remove that icon rather than to
// set one — an empty emoji is how an emoji is cleared. There is no format to
// verify then: what the target shows afterwards is whatever icon it still has.
func requestedIconFormat(args map[string]any) string {
	icon, ok := args["icon"].(map[string]any)
	if !ok {
		return ""
	}
	format := stringValue(icon["format"])
	if valueKey, known := iconValueKeys[format]; known && stringValue(icon[valueKey]) == "" {
		return ""
	}
	return format
}

// reportedIconFormat returns the icon format the API reports on an object.
func reportedIconFormat(obj map[string]any) string {
	icon, ok := obj["icon"].(map[string]any)
	if !ok {
		return ""
	}
	return stringValue(icon["format"])
}

// applyIcon makes the icon a call asked for the only icon the target has, and
// reports honestly when it cannot. It returns the target as it stands
// afterwards, whether the requested icon is in effect, and a note whenever
// something is worth saying about it.
//
// The icon that was asked for may already be the one showing while another
// icon is still stored underneath, so this runs for every icon write rather
// than only for the ones that visibly failed. It clears the competing
// relations once and never loops.
func (s *mcpServer) applyIcon(target iconTarget, want string, obj map[string]any) (map[string]any, bool, string) {
	if want == "" {
		return obj, true, ""
	}
	// Whether the icon is already the one being reported decides how much a
	// failure below matters: a stale relation that stays behind is untidy, a
	// requested icon that never shows is a failed write.
	showing := reportedIconFormat(obj) == want

	if !anytypefiles.IconFormatKnown(want) {
		if showing {
			return obj, true, ""
		}
		return obj, false, fmt.Sprintf("the API reports %s instead of the requested %q icon",
			describeReportedIcon(reportedIconFormat(obj)), want)
	}

	client, err := s.grpcClient()
	if err == nil {
		defer client.Close()
		err = client.ClearIconRelationsExcept(context.Background(), target.objectID, want)
	}
	if err != nil {
		if showing {
			// What was asked for is in effect; only the leftovers of the
			// previous icon could not be removed.
			return obj, true, fmt.Sprintf(
				"the %s icon is in effect, but the icon it replaced is still stored and would reappear if this one were removed: %v",
				want, err)
		}
		return obj, false, fmt.Sprintf(
			"the %s icon was stored but is hidden by the icon it was meant to replace, which could not be cleared: %v",
			want, err)
	}

	if showing {
		// Clearing a relation that was already losing changes nothing visible,
		// so there is nothing to read back.
		return obj, true, ""
	}
	fresh, err := target.reread()
	if err != nil {
		return obj, false, fmt.Sprintf(
			"the previous icon was cleared so the %s icon should now show, but reading it back failed: %v", want, err)
	}
	if reportedIconFormat(fresh) == want {
		return fresh, true, ""
	}
	return fresh, false, fmt.Sprintf("the API still reports %s after the requested %s icon was written",
		describeReportedIcon(reportedIconFormat(fresh)), want)
}

// verifyTypeIcon checks the icon of a type that was just written and annotates
// the payload when the icon is not the one that was asked for. The payload
// comes back untouched when the call carried no icon.
//
// A file icon on a type is checked over gRPC: heart's REST type reader passes
// an empty image to the icon resolver (core/api/service/type.go) and never
// reports one, while the app shows it, ahead of a built-in icon or an emoji.
func (s *mcpServer) verifyTypeIcon(spaceID string, args map[string]any, payload map[string]any) map[string]any {
	want := requestedIconFormat(args)
	if want == "" {
		return payload
	}
	typeObj, err := objectFromPayload(payload, "type")
	if err != nil {
		return payload
	}
	typeID := stringValue(typeObj["id"])
	if typeID == "" {
		return payload
	}

	var (
		fresh   map[string]any
		applied bool
		note    string
	)
	if want == anytypefiles.IconFormatFile {
		fresh, applied, note = s.applyTypeFileIcon(spaceID, typeID, iconFileID(args), typeObj)
	} else {
		fresh, applied, note = s.applyIcon(s.typeIconTarget(spaceID, typeID), want, typeObj)
	}
	payload["type"] = fresh
	for key, value := range iconFields(note, applied) {
		payload[key] = value
	}
	return payload
}

func describeReportedIcon(format string) string {
	if format == "" {
		return "no icon"
	}
	return fmt.Sprintf("a %q icon", format)
}

// iconFields turns the outcome of an icon apply into the fields a tool result
// carries: nothing when there is nothing to say, a warning on its own when the
// icon is in effect but something around it went wrong, and icon_applied:false
// alongside it when the requested icon is not showing at all.
func iconFields(note string, applied bool) map[string]any {
	if note == "" {
		return nil
	}
	if applied {
		return map[string]any{"icon_warning": note}
	}
	return map[string]any{
		"icon_applied": false,
		"icon_warning": note,
	}
}

// iconFileID is the image object a file icon names.
func iconFileID(args map[string]any) string {
	icon, _ := args["icon"].(map[string]any)
	return stringValue(icon["file"])
}

// applyTypeFileIcon clears the built-in icon and the emoji a type may still
// hold, then confirms over gRPC that the image is the icon it has, and reports
// it the way REST reports an object's file icon.
func (s *mcpServer) applyTypeFileIcon(spaceID, typeID, fileID string, typeObj map[string]any) (map[string]any, bool, string) {
	client, err := s.grpcClient()
	if err != nil {
		return typeObj, false, fmt.Sprintf("the file icon was sent, but it cannot be checked: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.ClearIconRelationsExcept(ctx, typeID, anytypefiles.IconFormatFile); err != nil {
		return typeObj, false, fmt.Sprintf("the file icon was stored, but the previous icon could not be cleared: %v", err)
	}
	states, err := client.IconStates(ctx, spaceID, []string{typeID})
	if err != nil {
		return typeObj, false, fmt.Sprintf("the file icon was stored, but reading it back failed: %v", err)
	}
	st := states[typeID]
	if st.Image == "" || (fileID != "" && st.Image != fileID) || st.Name != "" || st.Emoji != "" {
		return typeObj, false, "the type does not hold the requested file icon alone after the write"
	}
	typeObj["icon"] = map[string]any{"format": anytypefiles.IconFormatFile, "file": st.Image}
	return typeObj, true, ""
}

// iconRemoval is true when a call asks for no icon: icon null, or a known
// format with an empty value ({"format":"emoji","emoji":""}).
func iconRemoval(args map[string]any) bool {
	raw, present := args["icon"]
	if !present {
		return false
	}
	if raw == nil {
		return true
	}
	icon, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	valueKey, known := iconValueKeys[stringValue(icon["format"])]
	return known && stringValue(icon[valueKey]) == ""
}

// removeIcon empties every icon relation over gRPC and confirms it. REST can
// clear an emoji but refuses an empty built-in icon, and clears only the one
// relation it is given.
func (s *mcpServer) removeIcon(spaceID, objectID string) (bool, string) {
	client, err := s.grpcClient()
	if err != nil {
		return false, fmt.Sprintf("the icon could not be removed: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.ClearIcon(ctx, objectID); err != nil {
		return false, fmt.Sprintf("the icon could not be removed: %v", err)
	}
	states, err := client.IconStates(ctx, spaceID, []string{objectID})
	if err != nil {
		return false, fmt.Sprintf("the icon was removed, but reading it back failed: %v", err)
	}
	if !states[objectID].Empty() {
		return false, "an icon is still stored after the removal"
	}
	return true, ""
}

// fillTypeImageIcons adds the image icon REST leaves out of types, read over
// gRPC, to every type that came back without an icon.
func (s *mcpServer) fillTypeImageIcons(spaceID string, typeObjs []map[string]any) {
	var ids []string
	for _, t := range typeObjs {
		if t["icon"] == nil && stringValue(t["id"]) != "" {
			ids = append(ids, stringValue(t["id"]))
		}
	}
	if len(ids) == 0 {
		return
	}
	client, err := s.grpcClient()
	if err != nil {
		return
	}
	defer client.Close()
	states, err := client.IconStates(context.Background(), spaceID, ids)
	if err != nil {
		return
	}
	for _, t := range typeObjs {
		if st, ok := states[stringValue(t["id"])]; ok && t["icon"] == nil && st.Image != "" {
			t["icon"] = map[string]any{"format": anytypefiles.IconFormatFile, "file": st.Image}
		}
	}
}
