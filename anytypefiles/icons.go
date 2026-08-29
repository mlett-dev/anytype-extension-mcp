package anytypefiles

// Object and type icons.
//
// An icon looks like one field but is stored in three independent detail
// relations: iconEmoji, iconImage, and the iconName/iconOption pair for the
// built-in symbols. They are not exclusive. anytype-heart's REST layer writes
// only the relation the requested format needs (processIconFields in
// core/api/service/object.go) and never clears the others, while reading
// resolves them in a fixed order — iconName, then iconEmoji, then iconImage
// (getIcon in core/api/service/icon.go).
//
// So a file icon written over an existing emoji is stored and still invisible:
// the emoji keeps winning, and the API reports success while showing the old
// icon. The reverse looks fine and is not — the emoji shows, but the file icon
// stays stored underneath and resurfaces when the emoji is removed.
//
// Emptying the other relations is what makes an icon exclusive, and it has to
// happen over gRPC because the REST API refuses the empty values that would
// express "no emoji" or "no named icon" — it answers 400 "invalid icon name"
// and "icon file is not valid".
//
// This is deliberately a second step after the REST write rather than a
// replacement for it: the REST layer validates a file id against the space
// (isValidFileReference), and a raw ObjectSetDetails would skip that check.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gogo/protobuf/types"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Icon formats as the REST API names them.
const (
	IconFormatEmoji = "emoji"
	IconFormatFile  = "file"
	IconFormatNamed = "icon"
)

// iconRelationsToClear lists, per icon format, the icon relations that do not
// belong to that format.
//
// Every one of them is cleared, not just the ones that outrank the format. A
// relation that merely loses the resolution is invisible rather than harmless:
// it is still stored, and it comes back the moment the icon above it is
// removed — set a file icon, switch to an emoji, drop the emoji, and the old
// picture reappears out of nowhere. Clearing all of them is what turns the
// three relations into the single exclusive icon the API pretends they are.
var iconRelationsToClear = map[string][]string{
	IconFormatNamed: {"iconEmoji", "iconImage"},
	IconFormatEmoji: {"iconName", "iconOption", "iconImage"},
	IconFormatFile:  {"iconName", "iconOption", "iconEmoji"},
}

// IconFormatKnown reports whether format is one this package can reason about.
// An unknown format is not an error anywhere: it only means the relations that
// would conflict with it are unknown, so none are cleared.
func IconFormatKnown(format string) bool {
	_, ok := iconRelationsToClear[format]
	return ok
}

// ClearIconRelationsExcept empties every icon relation that does not belong to
// format, leaving the icon of that format as the only one stored.
//
// The icon itself was already written through the REST API; this only removes
// what would compete with it. Other details are untouched, and an image object
// that stops being an icon is left alone — it may be in use elsewhere.
func (c *Client) ClearIconRelationsExcept(ctx context.Context, objectID, format string) error {
	if strings.TrimSpace(objectID) == "" {
		return errors.New("object_id is required")
	}
	keys := iconRelationsToClear[format]
	if len(keys) == 0 {
		return nil
	}

	details := make([]*model.Detail, 0, len(keys))
	for _, key := range keys {
		// iconOption is a number and the others are strings; heart stores the
		// zero value of each as "unset".
		value := &types.Value{Kind: &types.Value_StringValue{StringValue: ""}}
		if key == "iconOption" {
			value = &types.Value{Kind: &types.Value_NumberValue{NumberValue: 0}}
		}
		details = append(details, &model.Detail{Key: key, Value: value})
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectSetDetails(callCtx, &pb.RpcObjectSetDetailsRequest{
		ContextId: objectID, Details: details,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectSetDetails failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectSetDetailsResponseError_NULL {
		return fmt.Errorf("ObjectSetDetails error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}
