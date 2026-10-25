package anytypefiles

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

// maxReported caps the ids listed per kind of failure; the counts stay exact.
const maxReported = 50

// DetailCheck is a batch property change as read back from the space index.
type DetailCheck struct {
	Verified        int              `json:"verified"`
	Mismatches      []DetailMismatch `json:"mismatches,omitempty"`
	MismatchCount   int              `json:"mismatch_count,omitempty"`
	Unverified      []string         `json:"unverified,omitempty"`
	UnverifiedCount int              `json:"unverified_count,omitempty"`
}

// DetailMismatch is one object whose property does not hold the change.
type DetailMismatch struct {
	ObjectID    string `json:"object_id"`
	PropertyKey string `json:"property_key"`
	Expected    string `json:"expected"`
	Found       any    `json:"found"`
}

// Complete is true when every object was read back and holds the change.
func (c DetailCheck) Complete() bool {
	return c.MismatchCount == 0 && c.UnverifiedCount == 0
}

// detailExpectation is what one property must look like after the call.
type detailExpectation struct {
	label    string // the key as the caller spelled it
	key      string // the internal key heart stores
	removed  bool
	hasValue bool
	value    any
	contains []any
	lacks    []any
}

// detailExpectations folds the operations per key in the order heart applies
// them: a set replaces what came before, add and remove adjust it.
func detailExpectations(ops []DetailOperation, resolved []string) []*detailExpectation {
	byKey := map[string]*detailExpectation{}
	var order []*detailExpectation
	for i, op := range ops {
		e := byKey[resolved[i]]
		if e == nil {
			e = &detailExpectation{label: op.PropertyKey, key: resolved[i]}
			byKey[resolved[i]] = e
			order = append(order, e)
		}
		if op.HasSet {
			*e = detailExpectation{label: op.PropertyKey, key: resolved[i], removed: op.Set == nil, hasValue: op.Set != nil, value: op.Set}
			continue
		}
		// heart applies add before remove within one operation. After a set,
		// add and remove are checked on their own: the exact list is no longer
		// worth predicting.
		e.hasValue = false
		if op.HasAdd {
			for _, v := range asList(op.Add) {
				e.contains = append(e.contains, v)
				e.lacks = dropValue(e.lacks, v)
			}
		}
		if op.HasRemove {
			for _, v := range asList(op.Remove) {
				e.lacks = append(e.lacks, v)
				e.contains = dropValue(e.contains, v)
			}
		}
	}
	return order
}

// checkDetails reads the objects back and compares. heart indexes a change
// before the write RPC returns (smartBlock.Apply runs the indexer), so no
// waiting is needed. Archived objects drop out of a default search and get a
// second one; what neither finds (deleted, or a layout the index leaves out)
// is reported as unverified rather than failed.
func (c *Client) checkDetails(ctx context.Context, spaceID string, objectIDs []string, expect []*detailExpectation) (DetailCheck, error) {
	keys := []string{"id"}
	for _, e := range expect {
		keys = append(keys, e.key)
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
	found := map[string]*types.Struct{}
	for _, filters := range [][]*model.BlockContentDataviewFilter{{idFilter}, {idFilter, archived}} {
		records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{SpaceId: spaceID, Filters: filters, Keys: keys}, "the changed objects")
		if err != nil {
			return DetailCheck{}, fmt.Errorf("the change was sent, but reading it back failed: %w", err)
		}
		for _, r := range records {
			if id, _ := fromProtoValue(structField(r, "id")).(string); id != "" {
				found[id] = r
			}
		}
	}

	var out DetailCheck
	for _, id := range objectIDs {
		record, ok := found[id]
		if !ok {
			out.UnverifiedCount++
			if len(out.Unverified) < maxReported {
				out.Unverified = append(out.Unverified, id)
			}
			continue
		}
		clean := true
		for _, e := range expect {
			value, present := record.Fields[e.key]
			if want, got, ok := e.holds(value, present); !ok {
				clean = false
				out.MismatchCount++
				if len(out.Mismatches) < maxReported {
					out.Mismatches = append(out.Mismatches, DetailMismatch{ObjectID: id, PropertyKey: e.label, Expected: want, Found: got})
				}
			}
		}
		if clean {
			out.Verified++
		}
	}
	return out, nil
}

// holds says whether the stored value meets the expectation, and if not, what
// was expected and what was found, in words a caller can act on.
func (e *detailExpectation) holds(value *types.Value, present bool) (string, any, bool) {
	var got any = "absent"
	if present {
		got = fromProtoValue(value)
	}
	if e.removed {
		if present {
			if got == nil {
				got = "null"
			}
			return "removed", got, false
		}
		return "", nil, true
	}
	if e.hasValue {
		// heart skips setting an empty string on an object that never had
		// the key (basic.isEmptyValueSet), and an absent key reads as empty
		// in the app as well.
		if !present && isEmptySet(e.value) {
			return "", nil, true
		}
		if !present || !sameValue(e.value, got) {
			return fmt.Sprintf("= %v", e.value), got, false
		}
		return "", nil, true
	}
	have := asList(got)
	if !present {
		have = nil
	}
	for _, v := range e.contains {
		if !containsValue(have, v) {
			return fmt.Sprintf("contains %v", v), got, false
		}
	}
	for _, v := range e.lacks {
		if containsValue(have, v) {
			return fmt.Sprintf("lacks %v", v), got, false
		}
	}
	return "", nil, true
}

func asList(v any) []any {
	switch list := v.(type) {
	case nil:
		return nil
	case []any:
		return list
	case []string:
		out := make([]any, len(list))
		for i, s := range list {
			out[i] = s
		}
		return out
	default:
		return []any{v}
	}
}

func dropValue(list []any, v any) []any {
	out := list[:0]
	for _, item := range list {
		if !sameValue(item, v) {
			out = append(out, item)
		}
	}
	return out
}

func containsValue(list []any, v any) bool {
	for _, item := range list {
		if sameValue(item, v) {
			return true
		}
	}
	return false
}

func isEmptySet(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case bool:
		return !x
	}
	return false
}

// sameValue compares what was sent with what heart stored: numbers as
// numbers, a single value against a one-element list (heart keeps object and
// tag properties as lists), and a date string against the Unix seconds heart
// keeps for date properties.
func sameValue(want, got any) bool {
	wantList, wantIsList := want.([]any)
	gotList, gotIsList := got.([]any)
	switch {
	case wantIsList && gotIsList:
		if len(wantList) != len(gotList) {
			return false
		}
		for i := range wantList {
			if !sameValue(wantList[i], gotList[i]) {
				return false
			}
		}
		return true
	case wantIsList && len(wantList) == 1:
		return sameValue(wantList[0], got)
	case gotIsList && len(gotList) == 1:
		return sameValue(want, gotList[0])
	case wantIsList || gotIsList:
		return false
	}
	if w, ok := toFloat(want); ok {
		if g, ok := toFloat(got); ok {
			return math.Abs(w-g) < 1e-9
		}
	}
	if ws, ok := want.(string); ok {
		if g, ok := toFloat(got); ok {
			if t, ok := parseDate(ws); ok {
				return t.Unix() == int64(g)
			}
		}
	}
	return fmt.Sprintf("%v", want) == fmt.Sprintf("%v", got)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
