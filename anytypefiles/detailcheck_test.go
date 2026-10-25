package anytypefiles

import (
	"testing"

	"github.com/gogo/protobuf/types"
)

func valueList(vs ...*types.Value) *types.Value {
	return &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: vs}}}
}

func TestDetailExpectations(t *testing.T) {
	null := &types.Value{Kind: &types.Value_NullValue{}}
	cases := []struct {
		name    string
		ops     []DetailOperation
		value   *types.Value // nil: key absent
		holds   bool
		wantMsg string
	}{
		{"removed and gone", []DetailOperation{{PropertyKey: "done", HasSet: true}}, nil, true, ""},
		{"removed but kept as null", []DetailOperation{{PropertyKey: "done", HasSet: true}}, null, false, "removed"},
		{"removed but still set", []DetailOperation{{PropertyKey: "done", HasSet: true}}, &types.Value{Kind: &types.Value_BoolValue{}}, false, "removed"},
		{"set false", []DetailOperation{{PropertyKey: "done", Set: false, HasSet: true}}, &types.Value{Kind: &types.Value_BoolValue{}}, true, ""},
		{"set true, found false", []DetailOperation{{PropertyKey: "done", Set: true, HasSet: true}}, &types.Value{Kind: &types.Value_BoolValue{}}, false, "= true"},
		{"empty string on a key never set", []DetailOperation{{PropertyKey: "description", Set: "", HasSet: true}}, nil, true, ""},
		{"single id against a one-element list", []DetailOperation{{PropertyKey: "tag", Set: "a", HasSet: true}}, valueList(str("a")), true, ""},
		{"list in order", []DetailOperation{{PropertyKey: "tag", Set: []any{"a", "b"}, HasSet: true}}, valueList(str("a"), str("b")), true, ""},
		{"list differs", []DetailOperation{{PropertyKey: "tag", Set: []any{"a", "b"}, HasSet: true}}, valueList(str("a")), false, "= [a b]"},
		{"number", []DetailOperation{{PropertyKey: "n", Set: float64(5), HasSet: true}}, &types.Value{Kind: &types.Value_NumberValue{NumberValue: 5}}, true, ""},
		{"date string against unix seconds", []DetailOperation{{PropertyKey: "dueDate", Set: "2026-10-25", HasSet: true}}, &types.Value{Kind: &types.Value_NumberValue{NumberValue: 1792886400}}, true, ""},
		{"add present", []DetailOperation{{PropertyKey: "tag", Add: "a", HasAdd: true}}, valueList(str("x"), str("a")), true, ""},
		{"add missing", []DetailOperation{{PropertyKey: "tag", Add: []any{"a"}, HasAdd: true}}, valueList(str("x")), false, "contains a"},
		{"add on an absent key", []DetailOperation{{PropertyKey: "tag", Add: "a", HasAdd: true}}, nil, false, "contains a"},
		{"remove gone", []DetailOperation{{PropertyKey: "tag", Remove: "a", HasRemove: true}}, valueList(str("x")), true, ""},
		{"remove still there", []DetailOperation{{PropertyKey: "tag", Remove: "a", HasRemove: true}}, valueList(str("a")), false, "lacks a"},
		{"swap in one operation", []DetailOperation{{PropertyKey: "tag", Add: "b", Remove: "a", HasAdd: true, HasRemove: true}}, valueList(str("b")), true, ""},
		{"add then remove the same value", []DetailOperation{{PropertyKey: "tag", Add: "a", HasAdd: true}, {PropertyKey: "tag", Remove: "a", HasRemove: true}}, valueList(), true, ""},
		{"set then add checks the add only", []DetailOperation{{PropertyKey: "tag", Set: []any{"a"}, HasSet: true}, {PropertyKey: "tag", Add: "b", HasAdd: true}}, valueList(str("a"), str("b")), true, ""},
	}
	for _, c := range cases {
		resolved := make([]string, len(c.ops))
		for i, op := range c.ops {
			resolved[i] = op.PropertyKey
		}
		exp := detailExpectations(c.ops, resolved)
		if len(exp) != 1 {
			t.Fatalf("%s: %d expectations, want 1", c.name, len(exp))
		}
		want, _, ok := exp[0].holds(c.value, c.value != nil)
		if ok != c.holds || want != c.wantMsg {
			t.Errorf("%s: holds=%v %q, want %v %q", c.name, ok, want, c.holds, c.wantMsg)
		}
	}
}

func TestDetailCheckComplete(t *testing.T) {
	if !(DetailCheck{Verified: 3}).Complete() {
		t.Error("all verified must be complete")
	}
	if (DetailCheck{Verified: 2, UnverifiedCount: 1}).Complete() {
		t.Error("an object not read back must not count as done")
	}
	if (DetailCheck{Verified: 2, MismatchCount: 1}).Complete() {
		t.Error("a mismatch must not count as done")
	}
}
