package anytypefiles

// The layout switches of an object type.
//
// An object cannot be full width on its own — the setting belongs to its type
// and applies to every object of it. anytype-heart stores the three switches as
// plain hidden details on the type object (layoutWidth, layoutAlign,
// headerRelationsLayout, all format number, all ReadOnly:false) and never
// evaluates them itself: the only place the whole codebase mentions
// layoutWidth is the publish whitelist, and there it sits in the list for
// object types, not the one for documents. The clients read them when they
// draw the page.
//
// That makes the numbers a convention of the apps rather than of the protocol,
// which is why the tool takes names and translates them here, in one place.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gogo/protobuf/types"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Detail keys of the three switches.
const (
	detailLayoutWidth          = "layoutWidth"
	detailLayoutAlign          = "layoutAlign"
	detailHeaderRelationsLayou = "headerRelationsLayout"
)

// layoutWidthFull is what the "Full width" toggle writes when it is on; 0 is
// the ordinary, centred column.
const layoutWidthFull = 1

// headerPositions follows model.BlockAlign, which layoutAlign shares.
var headerPositions = map[string]float64{
	"left":   float64(model.Block_AlignLeft),
	"center": float64(model.Block_AlignCenter),
	"right":  float64(model.Block_AlignRight),
}

// propertiesViews maps the names the app shows.
//
// heart's own description of the relation says "Line or column", but the app
// labels the second one "List" — verified against the type editor after writing
// a 1. The app's wording wins, because that is what the user is looking at;
// "column" stays accepted so heart's spelling does not silently miss.
var propertiesViews = map[string]float64{
	"line":   0,
	"list":   1,
	"column": 1,
}

// propertiesViewNumbers is the way back, spelled out rather than derived: two
// names map to 1, and picking whichever one a map iteration happens to hit
// first would make the readback flap between calls.
var propertiesViewNumbers = map[float64]string{0: "line", 1: "list"}

// HeaderPositionNames lists the accepted header positions.
func HeaderPositionNames() []string { return []string{"left", "center", "right"} }

// PropertiesViewNames lists the accepted property layouts, in the app's own
// wording. "column" is accepted too but not advertised.
func PropertiesViewNames() []string { return []string{"line", "list"} }

// TypeLayoutSpec is the set of switches a caller wants to change. Anything left
// empty keeps its stored value.
type TypeLayoutSpec struct {
	FullWidth      *bool
	HeaderPosition string
	PropertiesView string
}

// TypeLayout is the state of the three switches.
type TypeLayout struct {
	FullWidth      bool   `json:"full_width"`
	HeaderPosition string `json:"header_position"`
	PropertiesView string `json:"properties_view"`
}

// SetTypeLayout writes the layout switches of a type and reports both states.
//
// Reporting what was there before matters here: the switches are hidden
// relations that no listing shows, so without a readback a caller has no way to
// find out what it just replaced.
func (c *Client) SetTypeLayout(ctx context.Context, spaceID, typeID string, spec TypeLayoutSpec) (TypeLayout, TypeLayout, error) {
	if strings.TrimSpace(typeID) == "" {
		return TypeLayout{}, TypeLayout{}, errors.New("type_id is required")
	}
	var details []*model.Detail

	if spec.FullWidth != nil {
		width := float64(0)
		if *spec.FullWidth {
			width = layoutWidthFull
		}
		details = append(details, numberDetail(detailLayoutWidth, width))
	}
	if spec.HeaderPosition != "" {
		value, ok := headerPositions[strings.ToLower(strings.TrimSpace(spec.HeaderPosition))]
		if !ok {
			return TypeLayout{}, TypeLayout{}, fmt.Errorf("header_position %q is not one of %s",
				spec.HeaderPosition, strings.Join(HeaderPositionNames(), ", "))
		}
		details = append(details, numberDetail(detailLayoutAlign, value))
	}
	if spec.PropertiesView != "" {
		value, ok := propertiesViews[strings.ToLower(strings.TrimSpace(spec.PropertiesView))]
		if !ok {
			return TypeLayout{}, TypeLayout{}, fmt.Errorf("properties_view %q is not one of %s",
				spec.PropertiesView, strings.Join(PropertiesViewNames(), ", "))
		}
		details = append(details, numberDetail(detailHeaderRelationsLayou, value))
	}
	if len(details) == 0 {
		return TypeLayout{}, TypeLayout{}, errors.New("nothing to change: pass at least one of full_width, header_position or properties_view")
	}

	before, err := c.ReadTypeLayout(ctx, spaceID, typeID)
	if err != nil {
		return TypeLayout{}, TypeLayout{}, err
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	resp, err := c.rpc.ObjectSetDetails(callCtx, &pb.RpcObjectSetDetailsRequest{
		ContextId: typeID, Details: details,
	})
	cancel()
	if err != nil {
		return TypeLayout{}, TypeLayout{}, fmt.Errorf("gRPC ObjectSetDetails failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectSetDetailsResponseError_NULL {
		return TypeLayout{}, TypeLayout{}, fmt.Errorf("ObjectSetDetails error (%s): %s",
			resp.Error.Code, resp.Error.Description)
	}

	after, err := c.ReadTypeLayout(ctx, spaceID, typeID)
	if err != nil {
		return before, TypeLayout{}, err
	}
	return before, after, nil
}

// ReadTypeLayout reports the layout switches of a type.
func (c *Client) ReadTypeLayout(ctx context.Context, spaceID, typeID string) (TypeLayout, error) {
	fields, err := c.objectDetails(ctx, spaceID, typeID)
	if err != nil {
		return TypeLayout{}, err
	}
	return TypeLayout{
		FullWidth:      fields[detailLayoutWidth].GetNumberValue() != 0,
		HeaderPosition: nameOfNumber(headerPositions, fields[detailLayoutAlign].GetNumberValue(), "left"),
		PropertiesView: propertiesViewNumbers[fields[detailHeaderRelationsLayou].GetNumberValue()],
	}, nil
}

// objectDetails reads the details of one object.
func (c *Client) objectDetails(ctx context.Context, spaceID, objectID string) (map[string]*types.Value, error) {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC ObjectShow failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return nil, fmt.Errorf("ObjectShow error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	for _, entry := range resp.GetObjectView().GetDetails() {
		if entry.GetId() == objectID {
			return entry.GetDetails().GetFields(), nil
		}
	}
	return map[string]*types.Value{}, nil
}

func numberDetail(key string, value float64) *model.Detail {
	return &model.Detail{Key: key, Value: &types.Value{
		Kind: &types.Value_NumberValue{NumberValue: value}}}
}

// nameOfNumber turns a stored number back into the name the tools use.
func nameOfNumber(names map[string]float64, value float64, fallback string) string {
	for name, candidate := range names {
		if candidate == value {
			return name
		}
	}
	return fallback
}
