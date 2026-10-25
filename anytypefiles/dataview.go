package anytypefiles

// Dataview access for Anytype queries (sets) and collections.
//
// This package is named after its first use (file upload/download) but is in
// practice the shared anytype-heart gRPC client, so the query work lives here
// rather than duplicating the connection and session-token plumbing.
//
// None of this is reachable through the public REST API: it can create a query
// object but not give it a source, views, filters or sorts. Those live only in
// the dataview block of the object, which is gRPC-only territory.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gogo/protobuf/types"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// DataviewBlockID is the block id Anytype gives the dataview block of a set or
// collection. InspectDataview still discovers it rather than assuming it.
const DataviewBlockID = "dataview"

// FilterSpec is the JSON-friendly form of a dataview filter.
type FilterSpec struct {
	ID          string `json:"id,omitempty"`
	RelationKey string `json:"property_key"`
	// APIRelationKey is the spelling list-properties reports, filled in on read
	// when it differs from the internal one. See relationkeys.go for why the
	// two exist and why property_key keeps carrying the internal spelling:
	// both are accepted as input, so adding the public one is enough to make a
	// read-modify-write round trip, while changing property_key's meaning would
	// silently break every caller that feeds it back somewhere else.
	APIRelationKey string `json:"api_property_key,omitempty"`
	Condition      string `json:"condition,omitempty"`
	// Operator is "no" on an ordinary filter. "and" or "or" make the entry a
	// group: it then has no property, condition or value of its own and
	// combines NestedFilters instead — the app's "advanced filter".
	Operator      string       `json:"operator,omitempty"`
	Format        string       `json:"format,omitempty"`
	Value         any          `json:"value,omitempty"`
	QuickOption   string       `json:"quick_option,omitempty"`
	NestedFilters []FilterSpec `json:"nested_filters,omitempty"`
	// There is deliberately no include_time. The app forces it to false on
	// every filter before querying (lib/dataview.ts getData), so a stored true
	// only made the REST result differ from what the app shows.
}

// SortSpec is the JSON-friendly form of a dataview sort.
type SortSpec struct {
	ID             string `json:"id,omitempty"`
	RelationKey    string `json:"property_key"`
	APIRelationKey string `json:"api_property_key,omitempty"`
	Type           string `json:"direction,omitempty"`
	Format         string `json:"format,omitempty"`
	IncludeTime    bool   `json:"include_time,omitempty"`
	EmptyPlacement string `json:"empty_placement,omitempty"`
}

// RelationSpec is a column of a view.
type RelationSpec struct {
	Key       string `json:"property_key"`
	APIKey    string `json:"api_property_key,omitempty"`
	IsVisible bool   `json:"visible"`
	Width     int32  `json:"width,omitempty"`
}

// Scalar view fields, spelled the way the tool schema and ViewSpec's JSON tags
// spell them. The presence map and patchModel share these names, so the two
// cannot drift apart.
const (
	ViewFieldName            = "name"
	ViewFieldLayout          = "layout"
	ViewFieldGroupKey        = "group_property_key"
	ViewFieldCoverKey        = "cover_property_key"
	ViewFieldPageLimit       = "page_limit"
	ViewFieldDefaultTypeID   = "default_object_type_id"
	ViewFieldDefaultTemplate = "default_template_id"
	ViewFieldHideIcon        = "hide_icon"
	ViewFieldCardSize        = "card_size"
)

// ViewScalarFields lists every non-list field of a view a caller can set.
func ViewScalarFields() []string {
	return []string{
		ViewFieldName, ViewFieldLayout, ViewFieldGroupKey, ViewFieldCoverKey,
		ViewFieldPageLimit, ViewFieldDefaultTypeID, ViewFieldDefaultTemplate,
		ViewFieldHideIcon, ViewFieldCardSize,
	}
}

// ViewSpec is the JSON-friendly form of a dataview view.
type ViewSpec struct {
	ID                  string         `json:"id,omitempty"`
	Name                string         `json:"name,omitempty"`
	Type                string         `json:"layout,omitempty"`
	Filters             []FilterSpec   `json:"filters,omitempty"`
	Sorts               []SortSpec     `json:"sorts,omitempty"`
	Relations           []RelationSpec `json:"relations,omitempty"`
	GroupRelationKey    string         `json:"group_property_key,omitempty"`
	APIGroupKey         string         `json:"api_group_property_key,omitempty"`
	CoverRelationKey    string         `json:"cover_property_key,omitempty"`
	APICoverKey         string         `json:"api_cover_property_key,omitempty"`
	PageLimit           int32          `json:"page_limit,omitempty"`
	DefaultObjectTypeID string         `json:"default_object_type_id,omitempty"`
	DefaultTemplateID   string         `json:"default_template_id,omitempty"`
	HideIcon            bool           `json:"hide_icon,omitempty"`
	CardSize            string         `json:"card_size,omitempty"`

	// FiltersSet, SortsSet and RelationsSet record whether the caller supplied
	// the list at all, so UpdateView can tell "leave as is" from "clear it".
	FiltersSet   bool `json:"-"`
	SortsSet     bool `json:"-"`
	RelationsSet bool `json:"-"`

	// Set does the same for the scalar fields above, and for the same reason:
	// heart's SetViewFields copies every simple field of the view it is handed,
	// so an omitted argument arriving as a zero value erases the stored one — an
	// update without name left the view called "" (shown as Untitled) and one
	// without layout turned a kanban into a table, taking its grouping with it.
	// CreateView ignores the map: there a zero value is the intended default.
	Set map[string]bool `json:"-"`
}

// MarkSet records that the caller supplied field.
func (v *ViewSpec) MarkSet(field string) {
	if v.Set == nil {
		v.Set = make(map[string]bool, len(ViewScalarFields()))
	}
	v.Set[field] = true
}

// Supplied reports whether the caller passed field.
func (v ViewSpec) Supplied(field string) bool {
	return v.Set[field]
}

// touchesScalars reports whether any scalar field was supplied, i.e. whether
// the view's metadata has to be written at all.
func (v ViewSpec) touchesScalars() bool {
	for _, field := range ViewScalarFields() {
		if v.Set[field] {
			return true
		}
	}
	return false
}

// patchModel writes the supplied scalar fields onto a stored view and leaves
// every other one exactly as it was.
func (v ViewSpec) patchModel(view *model.BlockContentDataviewView) error {
	if v.Supplied(ViewFieldLayout) {
		// allowEmpty is false here on purpose: layout 0 is Table, so an empty
		// string that got this far would silently flatten the view.
		viewType, err := lookupEnum(viewTypes, v.Type, "view layout", false)
		if err != nil {
			return err
		}
		view.Type = viewType
	}
	if v.Supplied(ViewFieldCardSize) {
		cardSize, err := lookupEnum(cardSizes, v.CardSize, "card size", false)
		if err != nil {
			return err
		}
		view.CardSize = cardSize
	}
	if v.Supplied(ViewFieldName) {
		view.Name = v.Name
	}
	if v.Supplied(ViewFieldGroupKey) {
		view.GroupRelationKey = v.GroupRelationKey
	}
	if v.Supplied(ViewFieldCoverKey) {
		view.CoverRelationKey = v.CoverRelationKey
	}
	if v.Supplied(ViewFieldPageLimit) {
		view.PageLimit = v.PageLimit
	}
	if v.Supplied(ViewFieldDefaultTypeID) {
		view.DefaultObjectTypeId = v.DefaultObjectTypeID
	}
	if v.Supplied(ViewFieldDefaultTemplate) {
		view.DefaultTemplateId = v.DefaultTemplateID
	}
	if v.Supplied(ViewFieldHideIcon) {
		view.HideIcon = v.HideIcon
	}
	return nil
}

// DataviewInfo describes the dataview block of a set or collection.
type DataviewInfo struct {
	ObjectID string `json:"object_id"`
	BlockID  string `json:"block_id"`
	// TargetObjectID is set when the block is an inline view of another query
	// or collection, i.e. what block-embed-query creates. It is empty on the
	// dataview block of a query object itself.
	TargetObjectID string `json:"target_object_id,omitempty"`
	// Source is the effective source: for an embed it is read from the object
	// the block points at, because an embed's own source is always empty.
	Source       []string      `json:"source"`
	SourceFrom   string        `json:"source_from,omitempty"`
	IsCollection bool          `json:"is_collection"`
	ActiveView   string        `json:"active_view"`
	Views        []ViewSpec    `json:"views"`
	ObjectOrders []ObjectOrder `json:"object_orders,omitempty"`
	// Blocks lists every dataview block of the object, so a page holding more
	// than one embedded query does not silently look like it holds one.
	Blocks []DataviewBlockRef `json:"dataview_blocks,omitempty"`
	// Warnings names configuration that is stored but cannot work, so a caller
	// finds out from a read instead of from a view that quietly misbehaves.
	Warnings []string `json:"warnings,omitempty"`
}

// inspectDataviewSource reads the source of the object an embed points at.
//
// It stops there instead of recursing: an embed of an embed would otherwise be
// a loop waiting to happen, and one hop is all a source ever needs.
func (c *Client) inspectDataviewSource(ctx context.Context, spaceID, objectID string) (*DataviewInfo, error) {
	_, dv, details, _, err := c.loadDataviewBlock(ctx, spaceID, objectID, "")
	if err != nil {
		return nil, err
	}
	info := &DataviewInfo{ObjectID: objectID, Source: dv.Source, IsCollection: dv.IsCollection}
	if len(info.Source) == 0 {
		info.Source = setOfFromDetails(details, objectID)
	}
	return info, nil
}

// DataviewBlockRef identifies one dataview block of an object.
type DataviewBlockRef struct {
	BlockID        string `json:"block_id"`
	TargetObjectID string `json:"target_object_id,omitempty"`
}

// ObjectOrder is the manual, drag-and-drop order of one view.
//
// It is stored per view (and per kanban group) on the dataview block, exactly
// where the GUI writes it when a row or card is dragged. It is presentation
// state: anytype-heart never consults it when running a query (verified — no
// reference to ObjectOrders exists in pkg/lib/database or core/subscription),
// so the Anytype clients apply it at render time and server-side reads such as
// the REST list endpoints keep returning the view's sort order.
type ObjectOrder struct {
	ViewID    string   `json:"view_id"`
	GroupID   string   `json:"group_id,omitempty"`
	ObjectIDs []string `json:"object_ids"`
}

var viewTypes = map[string]model.BlockContentDataviewViewType{
	"table":    model.BlockContentDataviewView_Table,
	"grid":     model.BlockContentDataviewView_Table, // REST calls the table layout "grid"
	"list":     model.BlockContentDataviewView_List,
	"gallery":  model.BlockContentDataviewView_Gallery,
	"kanban":   model.BlockContentDataviewView_Kanban,
	"calendar": model.BlockContentDataviewView_Calendar,
	"graph":    model.BlockContentDataviewView_Graph,
}

var filterConditions = map[string]model.BlockContentDataviewFilterCondition{
	"none":             model.BlockContentDataviewFilter_None,
	"equal":            model.BlockContentDataviewFilter_Equal,
	"not_equal":        model.BlockContentDataviewFilter_NotEqual,
	"greater":          model.BlockContentDataviewFilter_Greater,
	"less":             model.BlockContentDataviewFilter_Less,
	"greater_or_equal": model.BlockContentDataviewFilter_GreaterOrEqual,
	"less_or_equal":    model.BlockContentDataviewFilter_LessOrEqual,
	"like":             model.BlockContentDataviewFilter_Like,
	"not_like":         model.BlockContentDataviewFilter_NotLike,
	"in":               model.BlockContentDataviewFilter_In,
	"not_in":           model.BlockContentDataviewFilter_NotIn,
	"empty":            model.BlockContentDataviewFilter_Empty,
	"not_empty":        model.BlockContentDataviewFilter_NotEmpty,
	"all_in":           model.BlockContentDataviewFilter_AllIn,
	"not_all_in":       model.BlockContentDataviewFilter_NotAllIn,
	"exact_in":         model.BlockContentDataviewFilter_ExactIn,
	"not_exact_in":     model.BlockContentDataviewFilter_NotExactIn,
	"exists":           model.BlockContentDataviewFilter_Exists,
}

// filterOperators deliberately holds only "no".
//
// In anytype-heart Operator does not say how a condition combines with its
// neighbours — it marks the filter as a GROUP. MakeFilter (pkg/lib/database/
// filter.go) returns early for Operator_No and otherwise ignores RelationKey,
// Condition and Value entirely, building the filter from NestedFilters instead.
// A leaf filter carrying "and" therefore loses its predicate and becomes an
// empty group: empty AND matches everything (both FiltersAnd.FilterObject and
// any-store query.And{}), empty OR is worse still — heart's in-memory FiltersOr
// answers true for the empty case while any-store's query.Or{} answers false.
// Either way the filter stops constraining anything while the API reports
// success, so the value is rejected rather than stored.
var filterOperators = map[string]model.BlockContentDataviewFilterOperator{
	"no": model.BlockContentDataviewFilter_No,
}

// filterOperatorRefusal explains the rejection where the caller will read it.
const filterOperatorRefusal = "filter operator %q on a single filter is not supported: anytype reads an operator as a filter GROUP, which drops the property, condition and value of this filter and leaves it matching everything. Omit the operator (the filters of a view combine with AND), or build a group: operator and/or with the rules in nested_filters"

// brokenFilterOperator reports a stored filter that was written with an
// operator and has no nested filters, i.e. one that cannot match as intended.
func brokenFilterOperator(f *model.BlockContentDataviewFilter) bool {
	return f.Operator != model.BlockContentDataviewFilter_No && len(f.NestedFilters) == 0
}

var sortTypes = map[string]model.BlockContentDataviewSortType{
	"asc":    model.BlockContentDataviewSort_Asc,
	"desc":   model.BlockContentDataviewSort_Desc,
	"custom": model.BlockContentDataviewSort_Custom,
}

var sortEmptyTypes = map[string]model.BlockContentDataviewSortEmptyType{
	"not_specified": model.BlockContentDataviewSort_NotSpecified,
	"start":         model.BlockContentDataviewSort_Start,
	"end":           model.BlockContentDataviewSort_End,
}

var cardSizes = map[string]model.BlockContentDataviewViewSize{
	"small":  model.BlockContentDataviewView_Small,
	"medium": model.BlockContentDataviewView_Medium,
	"large":  model.BlockContentDataviewView_Large,
}

var relationFormats = map[string]model.RelationFormat{
	"longtext":     model.RelationFormat_longtext,
	"text":         model.RelationFormat_shorttext,
	"shorttext":    model.RelationFormat_shorttext,
	"number":       model.RelationFormat_number,
	"status":       model.RelationFormat_status,
	"select":       model.RelationFormat_status,
	"tag":          model.RelationFormat_tag,
	"multi_select": model.RelationFormat_tag,
	"date":         model.RelationFormat_date,
	"file":         model.RelationFormat_file,
	"files":        model.RelationFormat_file,
	"checkbox":     model.RelationFormat_checkbox,
	"url":          model.RelationFormat_url,
	"email":        model.RelationFormat_email,
	"phone":        model.RelationFormat_phone,
	"emoji":        model.RelationFormat_emoji,
	"object":       model.RelationFormat_object,
	"objects":      model.RelationFormat_object,
	"relations":    model.RelationFormat_relations,
}

// ViewLayoutNames lists the accepted view layouts, for tool schemas.
func ViewLayoutNames() []string {
	return []string{"table", "list", "gallery", "kanban", "calendar", "graph"}
}

// FilterFormatNames lists every property format a dataview filter or sort
// accepts, for tool schemas.
//
// Deliberately NOT the same list as the REST tools' propertyFormats. The two
// layers speak different vocabularies for the same formats — REST says objects,
// select, text where a dataview says object, status, longtext — and the read
// side reports the dataview spelling, because that is what is stored. Offering
// only the REST names meant a schema-validating client could not send back the
// filter it had just read: of the fourteen names a read can produce, eight were
// missing from the enum, emoji and relations among them.
//
// Derived from relationFormats rather than written out, so a format cannot be
// added to the lookup and forgotten here. Sorted for a stable tools/list.
//
// This must not be used for create-property or the type property links: those
// go to the REST API, which only knows its own spelling and rejects the rest.
func FilterFormatNames() []string {
	out := make([]string, 0, len(relationFormats))
	for name := range relationFormats {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FilterConditionNames lists the accepted filter conditions, for tool schemas.
func FilterConditionNames() []string {
	// exists is left out: the app does not apply it, so it filters nothing
	// there while REST applies it. It is still read and written back.
	return []string{"equal", "not_equal", "greater", "less", "greater_or_equal",
		"less_or_equal", "like", "not_like", "in", "not_in", "empty", "not_empty",
		"all_in", "not_all_in", "exact_in", "not_exact_in"}
}

// canonicalNames inverts a lookup table so a stored enum can be reported in the
// spelling the same table accepts back.
//
// The read side used to hand out strings.ToLower(enum.String()), which is the
// protobuf name and not the vocabulary the write side takes: NotEqual came back
// as "notequal" while filterConditions only knows "not_equal", so a filter list
// read with query-inspect could not be written back with query-view-update.
// Only these two tables were affected — every other enum's protobuf name
// happens to be a key of its table already, and relation formats deliberately
// keep theirs, because "longtext" and "text" are different formats and folding
// one onto the other would lose that.
//
// Both tables are one key per value today, so the inverse is unambiguous. The
// pick is still made deterministically rather than trusting that: map iteration
// order is randomised, so should a second name for one value ever be added the
// way viewTypes carries "grid", an arbitrary pick would make the reported
// spelling differ between runs of the same build. A stable, wrong answer can be
// caught by a test; one that flips cannot.
func canonicalNames[T comparable](table map[string]T) map[T]string {
	names := make([]string, 0, len(table))
	for name := range table {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[T]string, len(table))
	for _, name := range names {
		if _, taken := out[table[name]]; !taken {
			out[table[name]] = name
		}
	}
	return out
}

// protobufAliases lets a table also accept the protobuf spelling of its own
// values, so a filter list captured from an older query-inspect response still
// writes back instead of being refused as an unknown condition.
func protobufAliases[T fmt.Stringer](table map[string]T) {
	for _, value := range table {
		alias := strings.ToLower(value.String())
		if _, taken := table[alias]; !taken {
			table[alias] = value
		}
	}
}

var (
	filterConditionNames = canonicalNames(filterConditions)
	sortEmptyTypeNames   = canonicalNames(sortEmptyTypes)
)

func init() {
	// Order matters: the canonical maps above are built from the tables before
	// the aliases go in, so an alias can never win as the reported spelling.
	protobufAliases(filterConditions)
	protobufAliases(sortEmptyTypes)
}

// canonicalName reports the write-side spelling of a stored enum, falling back
// to the protobuf name so an enum value this build does not know still comes
// through as something rather than as an empty string.
func canonicalName[T comparable](names map[T]string, value T, fallback string) string {
	if name, ok := names[value]; ok {
		return name
	}
	return strings.ToLower(fallback)
}

func lookupEnum[T any](table map[string]T, raw string, what string, allowEmpty bool) (T, error) {
	var zero T
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		if allowEmpty {
			return zero, nil
		}
		return zero, fmt.Errorf("%s is required", what)
	}
	value, ok := table[key]
	if !ok {
		return zero, fmt.Errorf("unknown %s %q", what, raw)
	}
	return value, nil
}

// enumName is the inverse of lookupEnum: it reports a stored enum under the
// same name the write side accepts. It goes through the very same table rather
// than the generated String(), which does not always match (an icon size is
// SizeNone there, but "none" here).
//
// Only for one-to-one tables. Where two names share a value — viewTypes maps
// both "table" and "grid" to Table — map iteration would pick either of them.
func enumName[T comparable](table map[string]T, value T) string {
	for name, candidate := range table {
		if candidate == value {
			return name
		}
	}
	return ""
}

// toProtoValue converts decoded JSON into a protobuf value.
func toProtoValue(v any) *types.Value {
	switch value := v.(type) {
	case nil:
		return &types.Value{Kind: &types.Value_NullValue{}}
	case bool:
		return &types.Value{Kind: &types.Value_BoolValue{BoolValue: value}}
	case float64:
		return &types.Value{Kind: &types.Value_NumberValue{NumberValue: value}}
	case int:
		return &types.Value{Kind: &types.Value_NumberValue{NumberValue: float64(value)}}
	case string:
		return &types.Value{Kind: &types.Value_StringValue{StringValue: value}}
	case []any:
		items := make([]*types.Value, 0, len(value))
		for _, item := range value {
			items = append(items, toProtoValue(item))
		}
		return &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: items}}}
	case map[string]any:
		fields := map[string]*types.Value{}
		for k, item := range value {
			fields[k] = toProtoValue(item)
		}
		return &types.Value{Kind: &types.Value_StructValue{StructValue: &types.Struct{Fields: fields}}}
	default:
		return &types.Value{Kind: &types.Value_StringValue{StringValue: fmt.Sprintf("%v", value)}}
	}
}

func fromProtoValue(v *types.Value) any {
	if v == nil {
		return nil
	}
	switch kind := v.Kind.(type) {
	case *types.Value_NullValue:
		return nil
	case *types.Value_BoolValue:
		return kind.BoolValue
	case *types.Value_NumberValue:
		return kind.NumberValue
	case *types.Value_StringValue:
		return kind.StringValue
	case *types.Value_ListValue:
		out := make([]any, 0, len(kind.ListValue.GetValues()))
		for _, item := range kind.ListValue.GetValues() {
			out = append(out, fromProtoValue(item))
		}
		return out
	case *types.Value_StructValue:
		out := map[string]any{}
		for k, item := range kind.StructValue.GetFields() {
			out[k] = fromProtoValue(item)
		}
		return out
	}
	return nil
}

// toModel converts a prepared filter (see filterprep.go) into the stored
// struct. base, when given, is the filter of the same id already stored: the
// result is written onto a copy of it, so a field of the struct this code does
// not model is kept rather than reset — heart replaces the whole filter.
func (f FilterSpec) toModel(base *model.BlockContentDataviewFilter) (*model.BlockContentDataviewFilter, error) {
	op := strings.ToLower(strings.TrimSpace(f.Operator))
	if group, ok := filterGroupOperators[op]; ok {
		if len(f.NestedFilters) == 0 {
			return nil, fmt.Errorf("a filter group with operator %s needs nested_filters", op)
		}
		out := cloneFilter(base)
		if out == nil {
			out = &model.BlockContentDataviewFilter{}
		}
		out.Id, out.Operator, out.RelationKey = f.ID, group, ""
		out.Condition, out.Value, out.Format = model.BlockContentDataviewFilter_None, nil, 0
		out.QuickOption, out.IncludeTime, out.NestedFilters = model.BlockContentDataviewFilter_ExactDate, false, nil
		for i, n := range f.NestedFilters {
			var nb *model.BlockContentDataviewFilter
			if base != nil && i < len(base.NestedFilters) {
				nb = base.NestedFilters[i]
			}
			child, err := n.toModel(nb)
			if err != nil {
				return nil, fmt.Errorf("nested_filters[%d]: %w", i, err)
			}
			out.NestedFilters = append(out.NestedFilters, child)
		}
		return out, nil
	}
	if len(f.NestedFilters) > 0 {
		return nil, errors.New("nested_filters needs operator and or or")
	}
	if strings.TrimSpace(f.RelationKey) == "" {
		return nil, errors.New("filter needs property_key")
	}
	condition, err := lookupEnum(filterConditions, f.Condition, "filter condition", false)
	if err != nil {
		return nil, err
	}
	if op != "" && op != "no" {
		return nil, fmt.Errorf(filterOperatorRefusal, f.Operator)
	}
	format, err := lookupEnum(relationFormats, f.Format, "property format", true)
	if err != nil {
		return nil, err
	}
	quick, err := lookupEnum(filterQuickOptions, f.QuickOption, "quick option", true)
	if err != nil {
		return nil, err
	}
	out := cloneFilter(base)
	if out == nil {
		out = &model.BlockContentDataviewFilter{}
	}
	out.Id = f.ID
	out.Operator = model.BlockContentDataviewFilter_No
	out.RelationKey = f.RelationKey
	out.Condition = condition
	out.Format = format
	out.QuickOption = quick
	out.IncludeTime = false
	out.NestedFilters = nil
	out.Value = nil
	// empty/not_empty/exists take no operand.
	if !noValueCondition(condition) {
		out.Value = toProtoValue(f.Value)
	}
	return out, nil
}

func filterFromModel(f *model.BlockContentDataviewFilter) FilterSpec {
	if f.RelationKey == "" && f.Operator != model.BlockContentDataviewFilter_No {
		out := FilterSpec{ID: f.Id, Operator: strings.ToLower(f.Operator.String())}
		for _, n := range f.NestedFilters {
			out.NestedFilters = append(out.NestedFilters, filterFromModel(n))
		}
		return out
	}
	out := FilterSpec{
		ID:          f.Id,
		RelationKey: f.RelationKey,
		Condition:   canonicalName(filterConditionNames, f.Condition, f.Condition.String()),
		Operator:    strings.ToLower(f.Operator.String()),
		Format:      f.Format.String(),
		Value:       fromProtoValue(f.Value),
	}
	if f.QuickOption != model.BlockContentDataviewFilter_ExactDate {
		out.QuickOption = canonicalName(filterQuickOptionNames, f.QuickOption, f.QuickOption.String())
	}
	// A leaf that wrongly carries nested filters still shows them, so the
	// read hides nothing that is stored.
	for _, n := range f.NestedFilters {
		out.NestedFilters = append(out.NestedFilters, filterFromModel(n))
	}
	return out
}

// toModel converts a sort. base, when given, is the stored sort of the same
// id; CustomOrder and NoCollate are not modelled and survive through it.
func (s SortSpec) toModel(base *model.BlockContentDataviewSort) (*model.BlockContentDataviewSort, error) {
	if strings.TrimSpace(s.RelationKey) == "" {
		return nil, errors.New("sort needs property_key")
	}
	sortType, err := lookupEnum(sortTypes, s.Type, "sort direction", true)
	if err != nil {
		return nil, err
	}
	format, err := lookupEnum(relationFormats, s.Format, "property format", true)
	if err != nil {
		return nil, err
	}
	empty, err := lookupEnum(sortEmptyTypes, s.EmptyPlacement, "empty placement", true)
	if err != nil {
		return nil, err
	}
	out := &model.BlockContentDataviewSort{}
	if base != nil {
		c := *base
		out = &c
	}
	out.Id = s.ID
	out.RelationKey = s.RelationKey
	out.Type = sortType
	out.Format = format
	out.IncludeTime = s.IncludeTime
	out.EmptyPlacement = empty
	return out, nil
}

func sortFromModel(s *model.BlockContentDataviewSort) SortSpec {
	return SortSpec{
		ID:             s.Id,
		RelationKey:    s.RelationKey,
		Type:           strings.ToLower(s.Type.String()),
		Format:         s.Format.String(),
		IncludeTime:    s.IncludeTime,
		EmptyPlacement: canonicalName(sortEmptyTypeNames, s.EmptyPlacement, s.EmptyPlacement.String()),
	}
}

func (v ViewSpec) toModel() (*model.BlockContentDataviewView, error) {
	viewType, err := lookupEnum(viewTypes, v.Type, "view layout", true)
	if err != nil {
		return nil, err
	}
	cardSize, err := lookupEnum(cardSizes, v.CardSize, "card size", true)
	if err != nil {
		return nil, err
	}
	out := &model.BlockContentDataviewView{
		Id:                  v.ID,
		Name:                v.Name,
		Type:                viewType,
		CoverRelationKey:    v.CoverRelationKey,
		GroupRelationKey:    v.GroupRelationKey,
		PageLimit:           v.PageLimit,
		DefaultObjectTypeId: v.DefaultObjectTypeID,
		DefaultTemplateId:   v.DefaultTemplateID,
		HideIcon:            v.HideIcon,
		CardSize:            cardSize,
	}
	// Filters and sorts are prepared by CreateView (filterprep.go), which
	// needs the space to check them.
	for _, r := range v.Relations {
		if strings.TrimSpace(r.Key) == "" {
			return nil, errors.New("view relation needs property_key")
		}
		out.Relations = append(out.Relations, &model.BlockContentDataviewRelation{
			Key:       r.Key,
			IsVisible: r.IsVisible,
			Width:     r.Width,
		})
	}
	return out, nil
}

func viewFromModel(v *model.BlockContentDataviewView) ViewSpec {
	out := ViewSpec{
		ID:                  v.Id,
		Name:                v.Name,
		Type:                strings.ToLower(v.Type.String()),
		GroupRelationKey:    v.GroupRelationKey,
		CoverRelationKey:    v.CoverRelationKey,
		PageLimit:           v.PageLimit,
		DefaultObjectTypeID: v.DefaultObjectTypeId,
		DefaultTemplateID:   v.DefaultTemplateId,
		HideIcon:            v.HideIcon,
		CardSize:            strings.ToLower(v.CardSize.String()),
	}
	for _, f := range v.Filters {
		out.Filters = append(out.Filters, filterFromModel(f))
	}
	for _, s := range v.Sorts {
		out.Sorts = append(out.Sorts, sortFromModel(s))
	}
	for _, r := range v.Relations {
		out.Relations = append(out.Relations, RelationSpec{
			Key: r.Key, IsVisible: r.IsVisible, Width: r.Width,
		})
	}
	return out
}

// AnnotateAPIKeys fills in the api_property_key of every key a DataviewInfo
// reports, so a caller can look the property up without a second round of
// guessing.
//
// This is a separate step rather than part of InspectDataview on purpose. It
// costs one search of the space's properties, and InspectDataview is called in
// a loop over every query of a space by analyze-schema-usage and
// clean-unused-tags — folding the load in there would multiply that cost by the
// number of queries for two tools that never look at the public spelling. The
// tools that show their result to a caller call this; the internal read-back
// paths (SetQuerySource's restore, findViewModel) deliberately do not.
//
// Failure to load the map is not failure to inspect: the annotation is extra
// information, so the error is returned for the caller to report as a warning
// while the DataviewInfo it already has stays usable.
func (c *Client) AnnotateAPIKeys(ctx context.Context, spaceID string, info *DataviewInfo) error {
	if info == nil {
		return nil
	}
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return err
	}
	for i := range info.Views {
		view := &info.Views[i]
		view.APIGroupKey = keys.apiKey(view.GroupRelationKey)
		view.APICoverKey = keys.apiKey(view.CoverRelationKey)
		for j := range view.Filters {
			annotateFilter(&view.Filters[j], keys)
		}
		for j := range view.Sorts {
			view.Sorts[j].APIRelationKey = keys.apiKey(view.Sorts[j].RelationKey)
		}
		// A filter or sort on a property that was deleted or archived is
		// skipped by the app (lib/dataview.ts getFilteredFilters), while the
		// REST list endpoint still applies it.
		WalkFilters(view.Filters, func(f FilterSpec) {
			if f.RelationKey == "" {
				return
			}
			if _, err := keys.resolve(f.RelationKey); err != nil {
				info.Warnings = append(info.Warnings, fmt.Sprintf(
					"view %q, filter %q is on %q, a property that is deleted or archived: the app ignores it, get-list-objects-compact still applies it. Remove it",
					view.ID, f.ID, f.RelationKey))
			}
		})
		for _, so := range view.Sorts {
			if _, err := keys.resolve(so.RelationKey); err != nil {
				info.Warnings = append(info.Warnings, fmt.Sprintf(
					"view %q, sort %q is on %q, a property that is deleted or archived: the app ignores it. Remove it",
					view.ID, so.ID, so.RelationKey))
			}
		}
		for j := range view.Relations {
			view.Relations[j].APIKey = keys.apiKey(view.Relations[j].Key)
		}
	}
	return nil
}

// CreateQuery creates a query (set) whose source is one or more type ids.
func (c *Client) CreateQuery(ctx context.Context, spaceID, name string, source []string, iconEmoji string) (string, error) {
	if strings.TrimSpace(spaceID) == "" {
		return "", errors.New("space_id is required")
	}
	if len(source) == 0 {
		return "", errors.New("source is required: a query without a source returns nothing")
	}
	fields := map[string]*types.Value{}
	if name != "" {
		fields["name"] = &types.Value{Kind: &types.Value_StringValue{StringValue: name}}
	}
	if iconEmoji != "" {
		fields["iconEmoji"] = &types.Value{Kind: &types.Value_StringValue{StringValue: iconEmoji}}
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.ObjectCreateSet(callCtx, &pb.RpcObjectCreateSetRequest{
		SpaceId: spaceID,
		Source:  source,
		Details: &types.Struct{Fields: fields},
	})
	if err != nil {
		return "", fmt.Errorf("gRPC ObjectCreateSet failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectCreateSetResponseError_NULL {
		return "", fmt.Errorf("ObjectCreateSet error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.ObjectId, nil
}

// InspectDataview reads the dataview block of a set or collection.
func (c *Client) InspectDataview(ctx context.Context, spaceID, objectID, blockID string) (*DataviewInfo, error) {
	found, dv, details, blocks, err := c.loadDataviewBlock(ctx, spaceID, objectID, blockID)
	if err != nil {
		return nil, err
	}
	info := &DataviewInfo{
		ObjectID:       objectID,
		BlockID:        found,
		TargetObjectID: dv.TargetObjectId,
		Source:         dv.Source,
		IsCollection:   dv.IsCollection,
		ActiveView:     dv.ActiveView,
		Blocks:         blocks,
	}
	// Where the effective source lives depends on what this block is.
	//
	// On a query object it is the setOf detail, not the dataview block, which
	// stays empty. On an embedded view it is the object the block points at:
	// heart leaves Source empty there on purpose, because TargetObjectId IS the
	// source. Reading setOf off the page holding the embed answers null and
	// makes a perfectly healthy embed look broken, so follow the target.
	if len(info.Source) == 0 {
		if dv.TargetObjectId != "" && dv.TargetObjectId != objectID {
			if target, err := c.inspectDataviewSource(ctx, spaceID, dv.TargetObjectId); err == nil {
				info.Source = target.Source
				info.SourceFrom = dv.TargetObjectId
				info.IsCollection = target.IsCollection
			}
		} else {
			info.Source = setOfFromDetails(details, objectID)
		}
	}
	if len(blocks) > 1 {
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"this object has %d dataview blocks; reported here is %q. Pass block_id to address another one",
			len(blocks), found))
	}
	for _, v := range dv.Views {
		for _, f := range v.Filters {
			if brokenFilterOperator(f) {
				info.Warnings = append(info.Warnings, fmt.Sprintf(
					"view %q, filter %q on %q has operator %q and no nested filters: anytype reads it as an empty filter group, so it constrains nothing. Rewrite the filter without an operator",
					v.Id, f.Id, f.RelationKey, strings.ToLower(f.Operator.String())))
			} else if reason := inactiveReason(f); reason != "" {
				// The app leaves such a filter out of the query, the REST list
				// endpoint applies it: the two then show different rows.
				info.Warnings = append(info.Warnings, fmt.Sprintf(
					"view %q, filter %q on %q filters nothing in the app: %s. get-list-objects-compact still applies it, so its rows differ from the app's",
					v.Id, f.Id, f.RelationKey, reason))
			}
		}
		info.Views = append(info.Views, viewFromModel(v))
	}
	for _, o := range dv.ObjectOrders {
		// heart empties an order instead of removing it when a filter or sort
		// changes (resetObjectOrderForView); an empty one is no order.
		if len(o.ObjectIds) == 0 {
			continue
		}
		info.ObjectOrders = append(info.ObjectOrders, ObjectOrder{
			ViewID: o.ViewId, GroupID: o.GroupId, ObjectIDs: o.ObjectIds,
		})
	}
	return info, nil
}

// loadDataviewBlock returns one dataview block of an object together with the
// object details, which is where a query keeps its effective source, and a list
// of every dataview block the object has.
//
// blockID picks the block. It has to be honoured rather than ignored: a page
// can hold several embedded queries, and answering with whichever comes first
// meant a caller read one block while writing to another — the reads and the
// writes of the same tool call disagreed about what they were working on.
func (c *Client) loadDataviewBlock(ctx context.Context, spaceID, objectID, blockID string) (string, *model.BlockContentDataview, []*model.ObjectViewDetailsSet, []DataviewBlockRef, error) {
	if strings.TrimSpace(objectID) == "" {
		return "", nil, nil, nil, errors.New("object_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()

	resp, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("gRPC ObjectShow failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return "", nil, nil, nil, fmt.Errorf("ObjectShow error (%s): %s", resp.Error.Code, resp.Error.Description)
	}

	blockID = strings.TrimSpace(blockID)
	var (
		blocks   []DataviewBlockRef
		chosen   *model.BlockContentDataview
		chosenID string
	)
	for _, block := range resp.GetObjectView().GetBlocks() {
		dv := block.GetDataview()
		if dv == nil {
			continue
		}
		blocks = append(blocks, DataviewBlockRef{BlockID: block.Id, TargetObjectID: dv.TargetObjectId})
		if chosen != nil {
			continue
		}
		if blockID == "" || block.Id == blockID {
			chosen, chosenID = dv, block.Id
		}
	}
	if chosen != nil {
		return chosenID, chosen, resp.GetObjectView().GetDetails(), blocks, nil
	}
	if blockID != "" && len(blocks) > 0 {
		ids := make([]string, 0, len(blocks))
		for _, b := range blocks {
			ids = append(ids, b.BlockID)
		}
		return "", nil, nil, nil, fmt.Errorf("object %s has no dataview block %q; it has %s",
			objectID, blockID, strings.Join(ids, ", "))
	}
	return "", nil, nil, nil, fmt.Errorf("object %s has no dataview block; it is not a query or collection", objectID)
}

// findViewModel returns one view exactly as anytype-heart stores it.
//
// UpdateView patches this struct instead of building a fresh one from a
// ViewSpec, because SetViewFields copies every simple field of the view it is
// handed and ViewSpec does not model all of them: cover_fit,
// group_background_colors, end_relation_key (the end date of a calendar view),
// wrap_content, list_size and alternate_rows have no representation here and
// would be reset on every single update, whatever the caller passed.
func (c *Client) findViewModel(ctx context.Context, spaceID, objectID, blockID, viewID string) (*model.BlockContentDataviewView, error) {
	_, dv, _, _, err := c.loadDataviewBlock(ctx, spaceID, objectID, blockID)
	if err != nil {
		return nil, err
	}
	for _, v := range dv.Views {
		if v.Id == viewID {
			return v, nil
		}
	}
	return nil, fmt.Errorf("view %s not found on object %s", viewID, objectID)
}

// registerRelations makes the dataview block aware of the properties a view
// refers to.
//
// Without this a column is stored but has nothing behind it. heart's
// syncViewRelationsAndRelationLinks only runs one way — it copies the block's
// RelationLinks into the view, never a view relation back into the block — and
// BlockDataviewViewRelationReplace does not touch RelationLinks at all. A view
// then lists a property as visible while the block does not know it exists, and
// the client has no schema to render the column from. Verified on a live space:
// two dataviews whose views showed six visible columns each had RelationLinks
// holding nothing but name and the five system properties.
//
// Adding is idempotent — heart's simple/dataview.AddRelation checks
// RelationLinks.Has(key) first — so this can run before every write.
func (c *Client) registerRelations(ctx context.Context, objectID, blockID string, keys []string) error {
	wanted := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		wanted = append(wanted, key)
	}
	if len(wanted) == 0 {
		return nil
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewRelationAdd(callCtx, &pb.RpcBlockDataviewRelationAddRequest{
		ContextId: objectID, BlockId: blockID, RelationKeys: wanted,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewRelationAdd failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewRelationAddResponseError_NULL {
		return fmt.Errorf("BlockDataviewRelationAdd error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// relationKeysOfView lists every property a view configuration refers to.
func relationKeysOfView(spec ViewSpec) []string {
	keys := make([]string, 0, len(spec.Relations)+2)
	for _, r := range spec.Relations {
		keys = append(keys, r.Key)
	}
	if spec.GroupRelationKey != "" {
		keys = append(keys, spec.GroupRelationKey)
	}
	if spec.CoverRelationKey != "" {
		keys = append(keys, spec.CoverRelationKey)
	}
	return keys
}

// SetQuerySource replaces the source types of a query.
//
// heart rebuilds the whole dataview block from the new source
// (SetSourceInSet -> MakeDataviewContent): it keeps the views but re-derives
// every view's relations from the type, which resets column widths and
// visibility, and it clears DefaultObjectTypeId and DefaultTemplateId outright.
// The two defaults are read before the call and written back afterwards,
// because losing them silently is the difference between "new object here uses
// the Task template" and "new object here is a blank page".
func (c *Client) SetQuerySource(ctx context.Context, spaceID, objectID, blockID string, source []string) error {
	before, beforeErr := c.InspectDataview(ctx, spaceID, objectID, blockID)

	callCtx, cancel := c.contextWithAuth(ctx)
	resp, err := c.rpc.BlockDataviewSetSource(callCtx, &pb.RpcBlockDataviewSetSourceRequest{
		ContextId: objectID, BlockId: blockID, Source: source,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewSetSource failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewSetSourceResponseError_NULL {
		return fmt.Errorf("BlockDataviewSetSource error (%s): %s", resp.Error.Code, resp.Error.Description)
	}

	if beforeErr != nil {
		return nil
	}
	for _, v := range before.Views {
		if v.DefaultObjectTypeID == "" && v.DefaultTemplateID == "" {
			continue
		}
		restore := ViewSpec{
			DefaultObjectTypeID: v.DefaultObjectTypeID,
			DefaultTemplateID:   v.DefaultTemplateID,
		}
		restore.MarkSet(ViewFieldDefaultTypeID)
		restore.MarkSet(ViewFieldDefaultTemplate)
		if err := c.UpdateView(ctx, spaceID, objectID, blockID, v.ID, restore); err != nil {
			return fmt.Errorf("source set, but restoring the default type and template of view %s failed: %w", v.ID, err)
		}
	}
	return nil
}

// CreateView adds a view and returns its id.
func (c *Client) CreateView(ctx context.Context, spaceID, objectID, blockID string, spec ViewSpec) (string, error) {
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return "", err
	}
	if err := keys.applyToView(&spec); err != nil {
		return "", err
	}
	view, err := spec.toModel()
	if err != nil {
		return "", err
	}
	if err := keys.validateGrouping(view); err != nil {
		return "", err
	}
	prep := &filterPrep{c: c, ctx: ctx, spaceID: spaceID, keys: keys, options: map[string][]tagOption{}}
	if view.Filters, err = prep.prepareFilters(spec.Filters, nil); err != nil {
		return "", err
	}
	if view.Sorts, err = prep.prepareSorts(spec.Sorts, nil); err != nil {
		return "", err
	}
	if err := c.registerRelations(ctx, objectID, blockID, relationKeysOfView(spec)); err != nil {
		return "", err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewViewCreate(callCtx, &pb.RpcBlockDataviewViewCreateRequest{
		ContextId: objectID, BlockId: blockID, View: view,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockDataviewViewCreate failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewViewCreateResponseError_NULL {
		return "", fmt.Errorf("BlockDataviewViewCreate error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.ViewId, nil
}

// UpdateView applies a view configuration.
//
// BlockDataviewViewUpdate only writes view metadata (name, layout, grouping,
// …) — verified against anytype-heart v0.50.8, it silently ignores the Filters
// and Sorts it is handed. To give callers the declarative behaviour they
// expect, the supplied filters and sorts are reconciled afterwards through the
// dedicated RPCs: existing entries are removed and the requested ones re-added.
// Lists the caller did not supply are left untouched.
//
// The metadata write is a patch, and it has to be built that way by hand: the
// RPC replaces the whole view, so the stored view is read first and only the
// fields the caller supplied are overwritten. It is skipped entirely when the
// call touches nothing but filters, sorts or columns.
func (c *Client) UpdateView(ctx context.Context, spaceID, objectID, blockID, viewID string, spec ViewSpec) error {
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return err
	}
	if err := keys.applyToView(&spec); err != nil {
		return err
	}

	// Everything is checked and converted before the first write. The lists
	// are replaced entry by entry (remove all, add each), so an entry refused
	// halfway used to leave the view with its old filters gone and only part
	// of the new ones written, while the call reported an error.
	var (
		stored     *model.BlockContentDataviewView
		newFilters []*model.BlockContentDataviewFilter
		newSorts   []*model.BlockContentDataviewSort
	)
	if spec.FiltersSet || spec.SortsSet {
		if stored, err = c.findViewModel(ctx, spaceID, objectID, blockID, viewID); err != nil {
			return err
		}
		prep := &filterPrep{c: c, ctx: ctx, spaceID: spaceID, keys: keys, options: map[string][]tagOption{}}
		if spec.FiltersSet {
			if newFilters, err = prep.prepareFilters(spec.Filters, stored.Filters); err != nil {
				return err
			}
		}
		if spec.SortsSet {
			if newSorts, err = prep.prepareSorts(spec.Sorts, stored.Sorts); err != nil {
				return err
			}
		}
	}

	if err := c.registerRelations(ctx, objectID, blockID, relationKeysOfView(spec)); err != nil {
		return err
	}

	if spec.touchesScalars() {
		view, err := c.findViewModel(ctx, spaceID, objectID, blockID, viewID)
		if err != nil {
			return err
		}
		if err := spec.patchModel(view); err != nil {
			return err
		}
		view.Id = viewID
		// Validate the patched view rather than the arguments, so that
		// switching a view to kanban is checked against the grouping it
		// already has — but only when the caller actually touched layout or
		// grouping. An update that renames a view must not fail over a kanban
		// somebody else configured badly, least of all inside the restore step
		// of SetQuerySource, which would report failure on a source that was
		// already written.
		if spec.Supplied(ViewFieldLayout) || spec.Supplied(ViewFieldGroupKey) {
			if err := keys.validateGrouping(view); err != nil {
				return err
			}
		}

		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockDataviewViewUpdate(callCtx, &pb.RpcBlockDataviewViewUpdateRequest{
			ContextId: objectID, BlockId: blockID, ViewId: viewID, View: view,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("gRPC BlockDataviewViewUpdate failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewViewUpdateResponseError_NULL {
			return fmt.Errorf("BlockDataviewViewUpdate error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}

	if spec.FiltersSet {
		if err := c.replaceFilters(ctx, objectID, blockID, viewID, stored.Filters, newFilters); err != nil {
			return err
		}
	}
	if spec.SortsSet {
		if err := c.replaceSorts(ctx, objectID, blockID, viewID, stored.Sorts, newSorts); err != nil {
			return err
		}
	}

	if spec.RelationsSet {
		current, err := c.findView(ctx, spaceID, objectID, blockID, viewID)
		if err != nil {
			return err
		}
		if err := c.replaceViewRelations(ctx, objectID, blockID, viewID, current.Relations, spec.Relations); err != nil {
			return fmt.Errorf("replacing columns: %w", err)
		}
	}
	return nil
}

// replaceFilters swaps a view's filters for prepared ones. heart has no call
// that replaces the list (BlockDataviewViewUpdate ignores Filters), so it is
// remove all, then add each. Should an RPC fail after the removal, the old
// filters are put back as they were stored.
func (c *Client) replaceFilters(ctx context.Context, objectID, blockID, viewID string, old, wanted []*model.BlockContentDataviewFilter) error {
	if err := c.removeFilterIDs(ctx, objectID, blockID, viewID, filterIDs(old)); err != nil {
		return fmt.Errorf("replacing filters: %w", err)
	}
	var added []string
	for _, f := range wanted {
		id, err := c.addFilterModel(ctx, objectID, blockID, viewID, f)
		if err != nil {
			restoreErr := c.removeFilterIDs(ctx, objectID, blockID, viewID, added)
			for _, o := range old {
				if restoreErr != nil {
					break
				}
				_, restoreErr = c.addFilterModel(ctx, objectID, blockID, viewID, o)
			}
			if restoreErr != nil {
				return fmt.Errorf("replacing filters: %w; putting the old filters back failed too (%v), so check the view with query-inspect", err, restoreErr)
			}
			return fmt.Errorf("replacing filters: %w; the old filters were put back", err)
		}
		added = append(added, id)
	}
	return nil
}

// replaceSorts is replaceFilters for sorts.
func (c *Client) replaceSorts(ctx context.Context, objectID, blockID, viewID string, old, wanted []*model.BlockContentDataviewSort) error {
	if err := c.removeSortIDs(ctx, objectID, blockID, viewID, sortIDs(old)); err != nil {
		return fmt.Errorf("replacing sorts: %w", err)
	}
	var added []string
	for _, s := range wanted {
		if err := c.addSortModel(ctx, objectID, blockID, viewID, s); err != nil {
			restoreErr := c.removeSortIDs(ctx, objectID, blockID, viewID, added)
			for _, o := range old {
				if restoreErr != nil {
					break
				}
				restoreErr = c.addSortModel(ctx, objectID, blockID, viewID, o)
			}
			if restoreErr != nil {
				return fmt.Errorf("replacing sorts: %w; putting the old sorts back failed too (%v), so check the view with query-inspect", err, restoreErr)
			}
			return fmt.Errorf("replacing sorts: %w; the old sorts were put back", err)
		}
		added = append(added, s.Id)
	}
	return nil
}

func filterIDs(fs []*model.BlockContentDataviewFilter) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		if f.Id != "" {
			out = append(out, f.Id)
		}
	}
	return out
}

func sortIDs(ss []*model.BlockContentDataviewSort) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s.Id != "" {
			out = append(out, s.Id)
		}
	}
	return out
}

// replaceViewRelations makes a view's visible columns match the requested list.
//
// Columns are NOT added and removed — they are shown and hidden. A view keeps a
// relation entry for every property the dataview knows about, and IsVisible
// decides whether it appears as a column. Removing one does not stick: heart's
// syncViewRelationsAndRelationLinks runs right after every change and puts any
// missing relation straight back with IsVisible=false (verified live — a view
// with seven relations still had all seven after removing five).
//
// So the requested columns are made visible, everything else is hidden, and the
// requested order is applied. ReorderViewRelations appends the keys that were
// not listed, so the hidden ones simply trail behind.
//
// A column whose width the caller did not state keeps the width it has. The RPC
// replaces the whole relation entry and heart stores a zero width verbatim —
// syncViewRelationsAndRelationLinks only fills in DefaultViewRelationWidth for
// relations that are missing entirely, so it does not repair one — which would
// discard every column width set in the GUI.
func (c *Client) replaceViewRelations(ctx context.Context, objectID, blockID, viewID string, current, wanted []RelationSpec) error {
	wantedKeys := make(map[string]bool, len(wanted))
	for _, r := range wanted {
		wantedKeys[r.Key] = true
	}
	currentWidth := make(map[string]int32, len(current))
	for _, r := range current {
		currentWidth[r.Key] = r.Width
	}

	apply := func(spec RelationSpec, visible bool) error {
		rel := &model.BlockContentDataviewRelation{
			Key: spec.Key, IsVisible: visible, Width: spec.Width,
		}
		callCtx, cancel := c.contextWithAuth(ctx)
		defer cancel()
		// Replace inserts when the key is not there yet, so it covers both the
		// "already a column" and the "new property" case.
		resp, err := c.rpc.BlockDataviewViewRelationReplace(callCtx, &pb.RpcBlockDataviewViewRelationReplaceRequest{
			ContextId: objectID, BlockId: blockID, ViewId: viewID,
			RelationKey: spec.Key, Relation: rel,
		})
		if err != nil {
			return fmt.Errorf("gRPC BlockDataviewViewRelationReplace failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewViewRelationReplaceResponseError_NULL {
			return fmt.Errorf("BlockDataviewViewRelationReplace error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
		return nil
	}

	for _, r := range current {
		if wantedKeys[r.Key] {
			continue
		}
		if !r.IsVisible {
			continue
		}
		if err := apply(r, false); err != nil {
			return err
		}
	}

	order := make([]string, 0, len(wanted))
	for _, r := range wanted {
		order = append(order, r.Key)
		if r.Width == 0 {
			r.Width = currentWidth[r.Key]
		}
		if err := apply(r, true); err != nil {
			return err
		}
	}

	if len(order) == 0 {
		return nil
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewViewRelationSort(callCtx, &pb.RpcBlockDataviewViewRelationSortRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID, RelationKeys: order,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewViewRelationSort failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewViewRelationSortResponseError_NULL {
		return fmt.Errorf("BlockDataviewViewRelationSort error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// findView returns the current state of one view.
func (c *Client) findView(ctx context.Context, spaceID, objectID, blockID, viewID string) (ViewSpec, error) {
	info, err := c.InspectDataview(ctx, spaceID, objectID, blockID)
	if err != nil {
		return ViewSpec{}, err
	}
	for _, v := range info.Views {
		if v.ID == viewID {
			return v, nil
		}
	}
	return ViewSpec{}, fmt.Errorf("view %s not found on object %s", viewID, objectID)
}

// DeleteView removes a view.
func (c *Client) DeleteView(ctx context.Context, objectID, blockID, viewID string) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewViewDelete(callCtx, &pb.RpcBlockDataviewViewDeleteRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewViewDelete failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewViewDeleteResponseError_NULL {
		return fmt.Errorf("BlockDataviewViewDelete error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// SetViewPosition moves a view to a zero-based position.
func (c *Client) SetViewPosition(ctx context.Context, objectID, blockID, viewID string, position uint32) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewViewSetPosition(callCtx, &pb.RpcBlockDataviewViewSetPositionRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID, Position: position,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewViewSetPosition failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewViewSetPositionResponseError_NULL {
		return fmt.Errorf("BlockDataviewViewSetPosition error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// SetActiveView marks a view as the active one.
func (c *Client) SetActiveView(ctx context.Context, objectID, blockID, viewID string) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewViewSetActive(callCtx, &pb.RpcBlockDataviewViewSetActiveRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewViewSetActive failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewViewSetActiveResponseError_NULL {
		return fmt.Errorf("BlockDataviewViewSetActive error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// AddFilter appends a filter to a view and returns its id together with the
// filter as stored, values converted and the format taken from the property.
func (c *Client) AddFilter(ctx context.Context, spaceID, objectID, blockID, viewID string, spec FilterSpec) (string, FilterSpec, error) {
	view, err := c.findViewModel(ctx, spaceID, objectID, blockID, viewID)
	if err != nil {
		return "", FilterSpec{}, err
	}
	spec.ID = ""
	op := strings.ToLower(strings.TrimSpace(spec.Operator))
	if _, group := filterGroupOperators[op]; group {
		// The app offers one group per view and hides "Advanced filter" once
		// there is one; its rules are extended there, not beside it.
		for _, f := range view.Filters {
			if f.RelationKey == "" && f.Operator != model.BlockContentDataviewFilter_No {
				return "", FilterSpec{}, fmt.Errorf("this view already has a filter group (%s); change its rules with query-view-update instead of adding a second group", f.Id)
			}
		}
	}
	prep, err := c.newFilterPrep(ctx, spaceID)
	if err != nil {
		return "", FilterSpec{}, err
	}
	filter, err := prep.prepareFilter(spec, nil, 0)
	if err != nil {
		return "", FilterSpec{}, err
	}
	id, err := c.addFilterModel(ctx, objectID, blockID, viewID, filter)
	if err != nil {
		return "", FilterSpec{}, err
	}
	filter.Id = id
	stored := filterFromModel(filter)
	annotateFilter(&stored, prep.keys)
	return id, stored, nil
}

// annotateFilter adds the public spelling of every key in a filter.
func annotateFilter(f *FilterSpec, keys *relationKeyMap) {
	f.APIRelationKey = keys.apiKey(f.RelationKey)
	for i := range f.NestedFilters {
		annotateFilter(&f.NestedFilters[i], keys)
	}
}

func (c *Client) addFilterModel(ctx context.Context, objectID, blockID, viewID string, filter *model.BlockContentDataviewFilter) (string, error) {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewFilterAdd(callCtx, &pb.RpcBlockDataviewFilterAddRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID, Filter: filter,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockDataviewFilterAdd failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewFilterAddResponseError_NULL {
		return "", fmt.Errorf("BlockDataviewFilterAdd error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	if resp.FilterId != "" {
		return resp.FilterId, nil
	}
	return filter.Id, nil
}

// RemoveFilters deletes filters by id. Every id has to be on the view: heart
// removes unknown ids without complaint, so a mistyped id used to report
// success while the filter it meant stayed.
func (c *Client) RemoveFilters(ctx context.Context, spaceID, objectID, blockID, viewID string, ids []string) error {
	if len(ids) == 0 {
		return errors.New("filter_ids is required")
	}
	view, err := c.findViewModel(ctx, spaceID, objectID, blockID, viewID)
	if err != nil {
		return err
	}
	if err := checkIDs("filter", ids, filterIDs(view.Filters)); err != nil {
		return err
	}
	return c.removeFilterIDs(ctx, objectID, blockID, viewID, ids)
}

// checkIDs refuses ids a view does not have, naming the ones it has.
func checkIDs(what string, ids, present []string) error {
	known := make(map[string]bool, len(present))
	for _, id := range present {
		known[id] = true
	}
	var unknown []string
	for _, id := range ids {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	have := "none"
	if len(present) > 0 {
		have = strings.Join(present, ", ")
	}
	return fmt.Errorf("this view has no %s with the id %s, so nothing was removed. Its %s ids: %s",
		what, strings.Join(unknown, ", "), what, have)
}

func (c *Client) removeFilterIDs(ctx context.Context, objectID, blockID, viewID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewFilterRemove(callCtx, &pb.RpcBlockDataviewFilterRemoveRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID, Ids: ids,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewFilterRemove failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewFilterRemoveResponseError_NULL {
		return fmt.Errorf("BlockDataviewFilterRemove error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// AddSort appends a sort to a view and returns its id.
func (c *Client) AddSort(ctx context.Context, spaceID, objectID, blockID, viewID string, spec SortSpec) (string, error) {
	prep, err := c.newFilterPrep(ctx, spaceID)
	if err != nil {
		return "", err
	}
	spec.ID = ""
	sort, err := prep.prepareSort(spec, nil)
	if err != nil {
		return "", err
	}
	if err := c.addSortModel(ctx, objectID, blockID, viewID, sort); err != nil {
		return "", err
	}
	return sort.Id, nil
}

func (c *Client) addSortModel(ctx context.Context, objectID, blockID, viewID string, sort *model.BlockContentDataviewSort) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewSortAdd(callCtx, &pb.RpcBlockDataviewSortAddRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID, Sort: sort,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewSortAdd failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewSortAddResponseError_NULL {
		return fmt.Errorf("BlockDataviewSortAdd error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// RemoveSorts deletes sorts by id, refusing ids the view does not have.
func (c *Client) RemoveSorts(ctx context.Context, spaceID, objectID, blockID, viewID string, ids []string) error {
	if len(ids) == 0 {
		return errors.New("sort_ids is required")
	}
	view, err := c.findViewModel(ctx, spaceID, objectID, blockID, viewID)
	if err != nil {
		return err
	}
	if err := checkIDs("sort", ids, sortIDs(view.Sorts)); err != nil {
		return err
	}
	return c.removeSortIDs(ctx, objectID, blockID, viewID, ids)
}

func (c *Client) removeSortIDs(ctx context.Context, objectID, blockID, viewID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewSortRemove(callCtx, &pb.RpcBlockDataviewSortRemoveRequest{
		ContextId: objectID, BlockId: blockID, ViewId: viewID, Ids: ids,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewSortRemove failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewSortRemoveResponseError_NULL {
		return fmt.Errorf("BlockDataviewSortRemove error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// ConvertToQuery turns an existing object into a query with the given source.
func (c *Client) ConvertToQuery(ctx context.Context, objectID string, source []string) error {
	if len(source) == 0 {
		return errors.New("source is required when converting to a query")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectToSet(callCtx, &pb.RpcObjectToSetRequest{ContextId: objectID, Source: source})
	if err != nil {
		return fmt.Errorf("gRPC ObjectToSet failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectToSetResponseError_NULL {
		return fmt.Errorf("ObjectToSet error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// ConvertToCollection turns an existing object into a collection.
func (c *Client) ConvertToCollection(ctx context.Context, objectID string) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectToCollection(callCtx, &pb.RpcObjectToCollectionRequest{ContextId: objectID})
	if err != nil {
		return fmt.Errorf("gRPC ObjectToCollection failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectToCollectionResponseError_NULL {
		return fmt.Errorf("ObjectToCollection error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// setOfFromDetails reads the setOf relation of an object, which is where a
// query stores the types it draws from.
func setOfFromDetails(details []*model.ObjectViewDetailsSet, objectID string) []string {
	for _, entry := range details {
		if entry.GetId() != objectID {
			continue
		}
		value, ok := entry.GetDetails().GetFields()["setOf"]
		if !ok {
			continue
		}
		switch kind := value.Kind.(type) {
		case *types.Value_StringValue:
			if kind.StringValue != "" {
				return []string{kind.StringValue}
			}
		case *types.Value_ListValue:
			out := make([]string, 0, len(kind.ListValue.GetValues()))
			for _, item := range kind.ListValue.GetValues() {
				if s, ok := item.Kind.(*types.Value_StringValue); ok && s.StringValue != "" {
					out = append(out, s.StringValue)
				}
			}
			return out
		}
	}
	return nil
}

// SetObjectOrder writes the manual order of a view.
//
// This is the declarative form: the supplied list replaces the view's order
// outright. The relative BlockDataviewObjectOrderMove RPC is deliberately not
// used — it fails with "object order is not found" until an order already
// exists, so it cannot establish one, and a model can state a full order far
// more reliably than a relative placement anyway.
func (c *Client) SetObjectOrder(ctx context.Context, objectID, blockID, viewID, groupID string, objectIDs []string) error {
	if strings.TrimSpace(viewID) == "" {
		return errors.New("view_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewObjectOrderUpdate(callCtx, &pb.RpcBlockDataviewObjectOrderUpdateRequest{
		ContextId: objectID, BlockId: blockID,
		ObjectOrders: []*model.BlockContentDataviewObjectOrder{{
			ViewId: viewID, GroupId: groupID, ObjectIds: objectIDs,
		}},
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockDataviewObjectOrderUpdate failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewObjectOrderUpdateResponseError_NULL {
		return fmt.Errorf("BlockDataviewObjectOrderUpdate error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}
