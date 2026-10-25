package anytypefiles

import (
	"context"
	"sort"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

// EmptyKey is a property an object holds with no value.
type EmptyKey struct {
	Key      string // the spelling list-properties reports
	Internal string // the key heart stores
	Name     string
}

// EmptyKeys finds, per object, the properties it holds with an empty value:
// null, "" or an empty list. REST leaves most of them out (null always, "" and
// [] depending on the format), while the app's properties panel lists every
// key an object holds, under Local when its type does not. Hidden system
// properties are skipped, as the app skips them.
func (c *Client) EmptyKeys(ctx context.Context, spaceID string, objectIDs []string) (map[string][]EmptyKey, error) {
	out := map[string][]EmptyKey{}
	if len(objectIDs) == 0 {
		return out, nil
	}
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	ids := make([]*types.Value, 0, len(objectIDs))
	for _, id := range objectIDs {
		ids = append(ids, &types.Value{Kind: &types.Value_StringValue{StringValue: id}})
	}
	idFilter := &model.BlockContentDataviewFilter{
		RelationKey: "id", Condition: model.BlockContentDataviewFilter_In,
		Value: &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: ids}}},
	}
	archived := &model.BlockContentDataviewFilter{
		RelationKey: "isArchived", Condition: model.BlockContentDataviewFilter_Equal,
		Value: &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
	}
	for _, filters := range [][]*model.BlockContentDataviewFilter{{idFilter}, {idFilter, archived}} {
		records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{SpaceId: spaceID, Filters: filters}, "the objects' properties")
		if err != nil {
			return nil, err
		}
		for _, r := range records {
			id, _ := fromProtoValue(structField(r, "id")).(string)
			if id == "" {
				continue
			}
			var empty []EmptyKey
			for key, value := range r.GetFields() {
				if !keys.internal[key] || keys.hidden[key] || !isEmptyValue(value) {
					continue
				}
				api := keys.apiKey(key)
				if api == "" {
					api = key
				}
				empty = append(empty, EmptyKey{Key: api, Internal: key, Name: keys.names[key]})
			}
			sort.Slice(empty, func(i, j int) bool { return empty[i].Key < empty[j].Key })
			out[id] = empty
		}
	}
	return out, nil
}
