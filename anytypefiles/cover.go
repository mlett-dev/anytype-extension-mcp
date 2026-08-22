package anytypefiles

// Object covers.
//
// A cover is not part of the public REST API at all: its UpdateObjectRequest
// carries name, icon, type and properties, and its Object model has no cover
// field, so a cover can neither be written nor read there. Anytype keeps it in
// hidden detail relations on the object — coverId together with coverType,
// where type 1 means "the id is a file" (anytype-heart treats coverId as a file
// relation exactly when coverType is 1).
//
// Setting these details touches nothing else: no blocks, no properties, no
// other metadata.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

// coverTypeImage marks coverId as referring to an uploaded image object.
const coverTypeImage = 1

// Cover is the cover state of an object.
type Cover struct {
	ImageObjectID string  `json:"image_object_id,omitempty"`
	Type          int64   `json:"type"`
	Scale         float64 `json:"scale,omitempty"`
	X             float64 `json:"x,omitempty"`
	Y             float64 `json:"y,omitempty"`
}

// IsSet reports whether the object actually has a cover.
func (c Cover) IsSet() bool { return c.ImageObjectID != "" || c.Type != 0 }

// SetCover puts an existing image object on an object as its cover, replacing
// whatever was there. Passing an empty id removes the cover instead.
//
// Any previous cover image object is left alone — it may well be in use
// elsewhere, and deleting other people's data as a side effect of an update
// is not this function's business.
func (c *Client) SetCover(ctx context.Context, objectID, imageObjectID string) error {
	if strings.TrimSpace(objectID) == "" {
		return errors.New("object_id is required")
	}
	imageObjectID = strings.TrimSpace(imageObjectID)

	coverType := int64(coverTypeImage)
	if imageObjectID == "" {
		coverType = 0 // clears the cover
	}
	details := []*model.Detail{
		{Key: "coverId", Value: &types.Value{
			Kind: &types.Value_StringValue{StringValue: imageObjectID}}},
		{Key: "coverType", Value: &types.Value{
			Kind: &types.Value_NumberValue{NumberValue: float64(coverType)}}},
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

// GetCover reads an object's cover.
//
// It goes through ObjectShow because the REST API models no cover at all, so
// the compact object tools can never show one however they are asked.
func (c *Client) GetCover(ctx context.Context, spaceID, objectID string) (Cover, error) {
	if strings.TrimSpace(objectID) == "" {
		return Cover{}, errors.New("object_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	if err != nil {
		return Cover{}, fmt.Errorf("gRPC ObjectShow failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return Cover{}, fmt.Errorf("ObjectShow error (%s): %s", resp.Error.Code, resp.Error.Description)
	}

	var cover Cover
	for _, record := range resp.GetObjectView().GetDetails() {
		if record.Id != objectID {
			continue
		}
		fields := record.GetDetails().GetFields()
		if v, ok := fields["coverId"]; ok {
			cover.ImageObjectID, _ = fromProtoValue(v).(string)
		}
		if v, ok := fields["coverType"]; ok {
			if n, ok := fromProtoValue(v).(float64); ok {
				cover.Type = int64(n)
			}
		}
		if v, ok := fields["coverScale"]; ok {
			cover.Scale, _ = fromProtoValue(v).(float64)
		}
		if v, ok := fields["coverX"]; ok {
			cover.X, _ = fromProtoValue(v).(float64)
		}
		if v, ok := fields["coverY"]; ok {
			cover.Y, _ = fromProtoValue(v).(float64)
		}
	}
	return cover, nil
}
