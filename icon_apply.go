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

	// rendersFileIcons is false for types. anytype-heart's type reader passes
	// an empty image to the icon resolver (core/api/service/type.go), so a file
	// icon on a type is stored but can never be read back, no matter which
	// other relations are cleared. Repairing that is not possible; saying so is.
	rendersFileIcons bool

	// reread fetches the current state of the target from the REST API.
	reread func() (map[string]any, error)
}

func (s *mcpServer) objectIconTarget(spaceID, objectID string) iconTarget {
	return iconTarget{
		objectID:         objectID,
		rendersFileIcons: true,
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
		objectID:         typeID,
		rendersFileIcons: false,
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

	if want == anytypefiles.IconFormatFile && !target.rendersFileIcons {
		return obj, false, fmt.Sprintf(
			"the file icon was stored but anytype-heart never reads a file icon back on a type, so the API keeps reporting %s; use an emoji or a built-in icon instead",
			describeReportedIcon(reportedIconFormat(obj)))
	}
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
// Types are the one target where the repair can fail for good: heart stores a
// file icon on them but never reads one back, so the warning is the result.
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

	fresh, applied, note := s.applyIcon(s.typeIconTarget(spaceID, typeID), want, typeObj)
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
