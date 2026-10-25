package anytypefiles

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/types"

	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Every field of a stored filter either goes through query-inspect and back,
// or is deliberately not modelled and then survives through the stored base.
// A field added upstream fails here instead of being dropped by every
// read-modify-write — the way quickOption and nestedFilters were.
var (
	filterFieldsModelled = []string{"Id", "Operator", "RelationKey", "Condition", "Value", "QuickOption", "Format", "NestedFilters"}
	filterFieldsFromBase = []string{"RelationProperty"}
	filterFieldsForced   = []string{"IncludeTime"} // always false, as the app queries
	sortFieldsModelled   = []string{"Id", "RelationKey", "Type", "Format", "IncludeTime", "EmptyPlacement"}
	sortFieldsFromBase   = []string{"CustomOrder", "NoCollate"}
)

func exportedFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Name
		if !strings.HasPrefix(name, "XXX_") {
			out = append(out, name)
		}
	}
	return out
}

func TestEveryFilterFieldIsAccountedFor(t *testing.T) {
	known := map[string]bool{}
	for _, list := range [][]string{filterFieldsModelled, filterFieldsFromBase, filterFieldsForced} {
		for _, f := range list {
			known[f] = true
		}
	}
	for _, f := range exportedFields(reflect.TypeOf(model.BlockContentDataviewFilter{})) {
		if !known[f] {
			t.Errorf("BlockContentDataviewFilter has a field %s this code does not know: carry it in FilterSpec, or keep it from the stored base", f)
		}
	}
	known = map[string]bool{}
	for _, list := range [][]string{sortFieldsModelled, sortFieldsFromBase} {
		for _, f := range list {
			known[f] = true
		}
	}
	for _, f := range exportedFields(reflect.TypeOf(model.BlockContentDataviewSort{})) {
		if !known[f] {
			t.Errorf("BlockContentDataviewSort has a field %s this code does not know: carry it in SortSpec, or keep it from the stored base", f)
		}
	}
}

func str(s string) *types.Value { return &types.Value{Kind: &types.Value_StringValue{StringValue: s}} }
func num(n float64) *types.Value {
	return &types.Value{Kind: &types.Value_NumberValue{NumberValue: n}}
}

func TestFilterRoundTripIsLossless(t *testing.T) {
	leaf := &model.BlockContentDataviewFilter{
		Id: "f1", Operator: model.BlockContentDataviewFilter_No, RelationKey: "dueDate",
		RelationProperty: "legacy", Condition: model.BlockContentDataviewFilter_Greater,
		Value: num(3), QuickOption: model.BlockContentDataviewFilter_NumberOfDaysAgo,
		Format: model.RelationFormat_date,
	}
	group := &model.BlockContentDataviewFilter{
		Id: "g1", Operator: model.BlockContentDataviewFilter_Or,
		NestedFilters: []*model.BlockContentDataviewFilter{
			{Operator: model.BlockContentDataviewFilter_No, RelationKey: "status", Condition: model.BlockContentDataviewFilter_In,
				Value:  &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: []*types.Value{str("opt")}}}},
				Format: model.RelationFormat_status},
			{Operator: model.BlockContentDataviewFilter_And, NestedFilters: []*model.BlockContentDataviewFilter{
				{Operator: model.BlockContentDataviewFilter_No, RelationKey: "done", Condition: model.BlockContentDataviewFilter_Equal,
					Value: &types.Value{Kind: &types.Value_BoolValue{BoolValue: false}}, Format: model.RelationFormat_checkbox},
			}},
		},
	}
	for _, stored := range []*model.BlockContentDataviewFilter{leaf, group} {
		back, err := filterFromModel(stored).toModel(stored)
		if err != nil {
			t.Fatalf("%s does not write back: %v", stored.Id, err)
		}
		if !proto.Equal(back, stored) {
			t.Errorf("%s changed on the way through query-inspect:\n stored %v\n   back %v", stored.Id, stored, back)
		}
	}
	// Without the stored base only RelationProperty is lost.
	back, err := filterFromModel(leaf).toModel(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := *leaf
	want.RelationProperty = ""
	if !proto.Equal(back, &want) {
		t.Errorf("leaf without base:\n want %v\n  got %v", &want, back)
	}
}

func TestIncludeTimeIsAlwaysWrittenFalse(t *testing.T) {
	stored := &model.BlockContentDataviewFilter{Id: "f", RelationKey: "dueDate", Condition: model.BlockContentDataviewFilter_Equal,
		Value: num(1), Format: model.RelationFormat_date, IncludeTime: true}
	back, err := filterFromModel(stored).toModel(stored)
	if err != nil {
		t.Fatal(err)
	}
	if back.IncludeTime {
		t.Error("include_time survived; the app always queries with false, so REST and app would differ")
	}
}

func TestSortRoundTripKeepsUnmodelledFields(t *testing.T) {
	stored := &model.BlockContentDataviewSort{
		Id: "s1", RelationKey: "name", Type: model.BlockContentDataviewSort_Desc, Format: model.RelationFormat_shorttext,
		IncludeTime: true, EmptyPlacement: model.BlockContentDataviewSort_End, NoCollate: true,
		CustomOrder: []*types.Value{str("a"), str("b")},
	}
	spec := sortFromModel(stored)
	back, err := spec.toModel(stored)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(back, stored) {
		t.Errorf("sort changed:\n stored %v\n   back %v", stored, back)
	}
}

func TestGroupShapeIsChecked(t *testing.T) {
	cases := []struct {
		name string
		spec FilterSpec
		ok   bool
	}{
		{"group without rules", FilterSpec{Operator: "or"}, false},
		{"rules without operator", FilterSpec{NestedFilters: []FilterSpec{{RelationKey: "done", Condition: "equal", Value: true}}}, false},
		{"leaf with operator no", FilterSpec{RelationKey: "done", Condition: "equal", Operator: "no", Value: true}, true},
		{"group", FilterSpec{Operator: "or", NestedFilters: []FilterSpec{{RelationKey: "done", Condition: "equal", Value: true}}}, true},
	}
	for _, c := range cases {
		_, err := c.spec.toModel(nil)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestParseDateValue(t *testing.T) {
	cases := map[string]float64{
		"2026-10-25":                1792886400,
		"2026-10-25T00:00:00Z":      1792886400,
		"2026-10-25T01:00:00+01:00": 1792886400,
		"2026-10-25T12:00:00":       1792886400 + 12*3600,
		"1792886400":                1792886400,
	}
	for in, want := range cases {
		got, err := parseDateValue(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	if got, err := parseDateValue(float64(5)); err != nil || got != 5 {
		t.Errorf("a number stays: %v %v", got, err)
	}
	for _, bad := range []any{"25.10.2026", "morgen", true} {
		if _, err := parseDateValue(bad); err == nil {
			t.Errorf("%v accepted as a date", bad)
		}
	}
}

func TestNormalizeFilterValueMatchesTheApp(t *testing.T) {
	eq, in := model.BlockContentDataviewFilter_Equal, model.BlockContentDataviewFilter_In
	exact, today, daysAgo := model.BlockContentDataviewFilter_ExactDate, model.BlockContentDataviewFilter_Today, model.BlockContentDataviewFilter_NumberOfDaysAgo
	cases := []struct {
		name   string
		format model.RelationFormat
		cond   model.BlockContentDataviewFilterCondition
		quick  model.BlockContentDataviewFilterQuickOption
		in     any
		want   any
		fails  bool
	}{
		{"number from string", model.RelationFormat_number, eq, exact, "5", float64(5), false},
		{"number from text", model.RelationFormat_number, eq, exact, "fuenf", nil, true},
		{"empty number stays empty", model.RelationFormat_number, eq, exact, "", nil, false},
		{"checkbox string refused", model.RelationFormat_checkbox, eq, exact, "false", nil, true},
		{"checkbox bool", model.RelationFormat_checkbox, eq, exact, false, false, false},
		{"single tag id becomes a list", model.RelationFormat_tag, in, exact, "id1", []any{"id1"}, false},
		{"select id becomes a list", model.RelationFormat_status, eq, exact, "id1", []any{"id1"}, false},
		{"empty list stays empty", model.RelationFormat_tag, in, exact, []any{}, []any{}, false},
		{"object list of non-strings refused", model.RelationFormat_object, in, exact, []any{float64(1)}, nil, true},
		{"iso date", model.RelationFormat_date, eq, exact, "2026-10-25", float64(1792886400), false},
		{"period ignores value", model.RelationFormat_date, eq, today, nil, float64(0), false},
		{"days ago takes a number", model.RelationFormat_date, model.BlockContentDataviewFilter_Greater, daysAgo, "3", float64(3), false},
		{"text from number", model.RelationFormat_longtext, model.BlockContentDataviewFilter_Like, exact, float64(3), "3", false},
		{"no operand for empty", model.RelationFormat_tag, model.BlockContentDataviewFilter_Empty, exact, []any{"x"}, nil, false},
	}
	for _, c := range cases {
		got, err := normalizeFilterValue(c.format, c.cond, c.quick, c.in)
		if c.fails {
			if err == nil {
				t.Errorf("%s: accepted, got %v", c.name, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, %v; want %#v", c.name, got, err, c.want)
		}
	}
}

func TestFilterActiveMirrorsTheApp(t *testing.T) {
	list := func(ids ...string) *types.Value {
		vals := []*types.Value{}
		for _, id := range ids {
			vals = append(vals, str(id))
		}
		return &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: vals}}}
	}
	cases := []struct {
		name   string
		f      *model.BlockContentDataviewFilter
		active bool
	}{
		{"in []", &model.BlockContentDataviewFilter{RelationKey: "tag", Condition: model.BlockContentDataviewFilter_In, Value: list(), Format: model.RelationFormat_tag}, false},
		{"in [x]", &model.BlockContentDataviewFilter{RelationKey: "tag", Condition: model.BlockContentDataviewFilter_In, Value: list("x"), Format: model.RelationFormat_tag}, true},
		{"exists", &model.BlockContentDataviewFilter{RelationKey: "n", Condition: model.BlockContentDataviewFilter_Exists, Format: model.RelationFormat_number}, false},
		{"empty", &model.BlockContentDataviewFilter{RelationKey: "n", Condition: model.BlockContentDataviewFilter_Empty}, true},
		{"text equal empty string", &model.BlockContentDataviewFilter{RelationKey: "d", Condition: model.BlockContentDataviewFilter_Equal, Value: str("")}, false},
		{"date without value", &model.BlockContentDataviewFilter{RelationKey: "d", Condition: model.BlockContentDataviewFilter_Equal, Format: model.RelationFormat_date}, false},
		{"date today", &model.BlockContentDataviewFilter{RelationKey: "d", Condition: model.BlockContentDataviewFilter_Equal, Format: model.RelationFormat_date, QuickOption: model.BlockContentDataviewFilter_Today}, true},
		{"group with one live rule", &model.BlockContentDataviewFilter{Operator: model.BlockContentDataviewFilter_Or, NestedFilters: []*model.BlockContentDataviewFilter{
			{RelationKey: "tag", Condition: model.BlockContentDataviewFilter_In, Value: list(), Format: model.RelationFormat_tag},
			{RelationKey: "n", Condition: model.BlockContentDataviewFilter_NotEmpty},
		}}, true},
		{"group of empty rules", &model.BlockContentDataviewFilter{Operator: model.BlockContentDataviewFilter_And, NestedFilters: []*model.BlockContentDataviewFilter{
			{RelationKey: "name", Condition: model.BlockContentDataviewFilter_Like, Value: str("")},
		}}, false},
	}
	for _, c := range cases {
		if got := filterActive(c.f); got != c.active {
			t.Errorf("%s: active=%v, want %v", c.name, got, c.active)
		}
	}
}

func TestQuickOptionNamesRoundTrip(t *testing.T) {
	for _, name := range FilterQuickOptionNames() {
		value, err := lookupEnum(filterQuickOptions, name, "quick option", false)
		if err != nil {
			t.Fatalf("advertised %q not accepted: %v", name, err)
		}
		if back := canonicalName(filterQuickOptionNames, value, value.String()); back != name {
			t.Errorf("%q reads back as %q", name, back)
		}
	}
	if len(FilterQuickOptionNames()) != len(filterQuickOptions) {
		t.Errorf("advertised %d quick options, the table has %d", len(FilterQuickOptionNames()), len(filterQuickOptions))
	}
}

func TestCheckIDs(t *testing.T) {
	if err := checkIDs("filter", []string{"a"}, []string{"a", "b"}); err != nil {
		t.Errorf("known id refused: %v", err)
	}
	err := checkIDs("filter", []string{"a", "zz"}, []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "zz") || !strings.Contains(err.Error(), "nothing was removed") {
		t.Errorf("unknown id not reported: %v", err)
	}
}
