package anytypefiles

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gogo/protobuf/types"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

// Filters and sorts are prepared here before anything is written: keys
// resolved, the format taken from the property, values brought into the shape
// the Anytype app itself stores and applies.
//
// The app is the measure, not anytype-heart. heart stores and applies whatever
// it is given, but the app (anytype-ts v0.56, lib/dataview.ts getData and
// lib/relation.ts) first drops every filter it considers inactive
// (isFilterActive), then rewrites the rest through filterMapper: the format
// comes from the property, the value through formatValue (a number from a
// string, a list from a single id, a date through parseFloat) and includeTime
// is forced to false. A filter written in any other shape therefore gives one
// result in the app and another through the REST list endpoint, which applies
// the stored filter literally — in a live test "in []" showed 6 rows in the
// app and none through REST, "5" as a number 2 and none, an ISO date nothing
// in either. Writing exactly what the app would have written makes the two
// agree.

// filterQuickOptions are the app's relative date options ("Today", "This
// week", "Number of days ago"). heart turns them into a date range when the
// query runs (pkg/lib/database/quickoptions.go), so the filter follows the
// calendar instead of pinning one day.
var filterQuickOptions = map[string]model.BlockContentDataviewFilterQuickOption{
	"exact_date":         model.BlockContentDataviewFilter_ExactDate,
	"yesterday":          model.BlockContentDataviewFilter_Yesterday,
	"today":              model.BlockContentDataviewFilter_Today,
	"tomorrow":           model.BlockContentDataviewFilter_Tomorrow,
	"last_week":          model.BlockContentDataviewFilter_LastWeek,
	"current_week":       model.BlockContentDataviewFilter_CurrentWeek,
	"next_week":          model.BlockContentDataviewFilter_NextWeek,
	"last_month":         model.BlockContentDataviewFilter_LastMonth,
	"current_month":      model.BlockContentDataviewFilter_CurrentMonth,
	"next_month":         model.BlockContentDataviewFilter_NextMonth,
	"number_of_days_ago": model.BlockContentDataviewFilter_NumberOfDaysAgo,
	"number_of_days_now": model.BlockContentDataviewFilter_NumberOfDaysNow,
	"last_year":          model.BlockContentDataviewFilter_LastYear,
	"current_year":       model.BlockContentDataviewFilter_CurrentYear,
	"next_year":          model.BlockContentDataviewFilter_NextYear,
}

var filterQuickOptionNames = canonicalNames(filterQuickOptions)

// FilterQuickOptionNames lists the accepted quick options, for tool schemas.
func FilterQuickOptionNames() []string {
	return []string{"today", "yesterday", "tomorrow", "last_week", "current_week", "next_week",
		"last_month", "current_month", "next_month", "last_year", "current_year", "next_year",
		"number_of_days_ago", "number_of_days_now", "exact_date"}
}

// dayOptions take a number of days as their value, exactDate a date. Every
// other quick option is a period that ignores the value.
func dayOption(q model.BlockContentDataviewFilterQuickOption) bool {
	return q == model.BlockContentDataviewFilter_NumberOfDaysAgo || q == model.BlockContentDataviewFilter_NumberOfDaysNow
}

func periodOption(q model.BlockContentDataviewFilterQuickOption) bool {
	return q != model.BlockContentDataviewFilter_ExactDate && !dayOption(q)
}

// filterOperators name the two kinds of filter group. A group carries no
// property, condition or value of its own: it combines its nested_filters.
var filterGroupOperators = map[string]model.BlockContentDataviewFilterOperator{
	"and": model.BlockContentDataviewFilter_And,
	"or":  model.BlockContentDataviewFilter_Or,
}

// maxFilterDepth bounds group nesting. The app nests groups (a rule can be
// turned into a group of its own), but a depth beyond a few levels is a
// mistake rather than a query.
const maxFilterDepth = 4

// noValueConditions take no operand.
func noValueCondition(c model.BlockContentDataviewFilterCondition) bool {
	return c == model.BlockContentDataviewFilter_Empty ||
		c == model.BlockContentDataviewFilter_NotEmpty ||
		c == model.BlockContentDataviewFilter_Exists
}

// arrayFormat reports the formats whose filter value the app always stores as
// a list (formatValue: getArrayValue).
func arrayFormat(f model.RelationFormat) bool {
	switch f {
	case model.RelationFormat_status, model.RelationFormat_tag, model.RelationFormat_object,
		model.RelationFormat_file, model.RelationFormat_relations:
		return true
	}
	return false
}

func textFormat(f model.RelationFormat) bool {
	switch f {
	case model.RelationFormat_longtext, model.RelationFormat_shorttext, model.RelationFormat_url,
		model.RelationFormat_email, model.RelationFormat_phone, model.RelationFormat_emoji:
		return true
	}
	return false
}

// formatsCompatible decides whether a format the caller named fits the
// property. The text formats count as one family: list-properties says text
// where a dataview says longtext, and the app filters them all alike.
func formatsCompatible(a, b model.RelationFormat) bool {
	if a == b {
		return true
	}
	if textFormat(a) && textFormat(b) {
		return true
	}
	objectish := func(f model.RelationFormat) bool {
		return f == model.RelationFormat_object || f == model.RelationFormat_file
	}
	return objectish(a) && objectish(b)
}

// filterActive mirrors the app's isFilterActive (lib/relation.ts, v0.56.1):
// what it answers false for, the app leaves out of the query.
func filterActive(f *model.BlockContentDataviewFilter) bool {
	if f == nil {
		return false
	}
	if f.RelationKey == "" && f.Operator != model.BlockContentDataviewFilter_No {
		for _, n := range f.NestedFilters {
			if filterActive(n) {
				return true
			}
		}
		return false
	}
	switch f.Condition {
	case model.BlockContentDataviewFilter_None:
		return false
	case model.BlockContentDataviewFilter_Empty, model.BlockContentDataviewFilter_NotEmpty:
		return true
	}
	if f.Format == model.RelationFormat_date &&
		(f.Condition == model.BlockContentDataviewFilter_In || periodOption(f.QuickOption)) {
		return true
	}
	return valueSet(fromProtoValue(f.Value))
}

// valueSet mirrors the app's isFilterValueSet.
func valueSet(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case float64, bool:
		return true
	}
	return true
}

// inactiveReason explains, for a stored filter, why the app ignores it. ""
// when it is applied.
func inactiveReason(f *model.BlockContentDataviewFilter) string {
	if filterActive(f) {
		return ""
	}
	if f.Condition == model.BlockContentDataviewFilter_Exists {
		return "the app does not apply the exists condition"
	}
	return "it has no value"
}

// parseDateValue turns what a caller may pass for a date into Unix seconds,
// which is what heart compares and the app's formatValue produces.
//
// A date without a time is read as midnight UTC: heart computes the day range
// of a date filter in its own time zone, which is UTC in the anytype-cli
// container, so UTC midnight names the same day there.
func parseDateValue(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case string:
		s := strings.TrimSpace(x)
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n, nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
				return float64(t.Unix()), nil
			}
		}
		return 0, fmt.Errorf("date value %q is neither Unix seconds nor an ISO 8601 date such as 2026-10-25 or 2026-10-25T14:30:00+01:00", x)
	}
	return 0, fmt.Errorf("date value must be Unix seconds or an ISO 8601 string, not %T", v)
}

func parseNumberValue(v any, what string) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s must be a number, not %v", what, v)
}

// isEmptyValue reports a value that carries nothing: the app treats a filter
// holding one as inactive.
func isEmptyFilterValue(v any) bool {
	return !valueSet(v)
}

// normalizeFilterValue brings a value into the shape the app stores for the
// property's format. Tag ids are resolved separately, because that needs the
// space. An empty value stays empty; whether that is acceptable is decided by
// the caller, which knows whether the filter is new.
func normalizeFilterValue(format model.RelationFormat, condition model.BlockContentDataviewFilterCondition,
	quick model.BlockContentDataviewFilterQuickOption, value any) (any, error) {
	if noValueCondition(condition) {
		return nil, nil
	}
	switch {
	case format == model.RelationFormat_date:
		if periodOption(quick) {
			// The period decides; the app stores a placeholder.
			if n, ok := value.(float64); ok {
				return n, nil
			}
			return float64(0), nil
		}
		if isEmptyFilterValue(value) {
			return nil, nil
		}
		if dayOption(quick) {
			return parseNumberValue(value, "the number of days")
		}
		return parseDateValue(value)
	case format == model.RelationFormat_number:
		if isEmptyFilterValue(value) {
			return nil, nil
		}
		return parseNumberValue(value, "a number filter's value")
	case format == model.RelationFormat_checkbox:
		b, ok := value.(bool)
		if !ok {
			// The app's !!value would read the string "false" as true.
			return nil, fmt.Errorf("a checkbox filter needs true or false, not %v", value)
		}
		return b, nil
	case arrayFormat(format):
		switch x := value.(type) {
		case nil:
			return []any{}, nil
		case string:
			if x == "" {
				return []any{}, nil
			}
			return []any{x}, nil
		case []any:
			for _, item := range x {
				if _, ok := item.(string); !ok {
					return nil, fmt.Errorf("list values must be ids (strings), not %v", item)
				}
			}
			return x, nil
		}
		return nil, fmt.Errorf("value must be an id or a list of ids, not %v", value)
	case textFormat(format):
		switch x := value.(type) {
		case nil:
			return "", nil
		case string:
			return x, nil
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), nil
		}
		return nil, fmt.Errorf("a text filter needs a string, not %v", value)
	}
	return value, nil
}

// tagOption is one option of a select or multi-select property.
type tagOption struct {
	id, name, key string
}

// filterPrep prepares filters and sorts for one space. It loads what it needs
// once: the property keys and formats up front, the options of a select
// property the first time a filter on it names one.
type filterPrep struct {
	c       *Client
	ctx     context.Context
	spaceID string
	keys    *relationKeyMap
	options map[string][]tagOption
}

func (c *Client) newFilterPrep(ctx context.Context, spaceID string) (*filterPrep, error) {
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	return &filterPrep{c: c, ctx: ctx, spaceID: spaceID, keys: keys, options: map[string][]tagOption{}}, nil
}

// format answers the stored format of a property, and whether it is known.
func (p *filterPrep) format(key string) (model.RelationFormat, bool) {
	if f, ok := p.keys.formats[key]; ok {
		return f, true
	}
	if f, err := bundle.GetRelationFormat(domain.RelationKey(key)); err == nil {
		return f, true
	}
	return 0, false
}

// tagOptions loads the options of one select or multi-select property.
func (p *filterPrep) tagOptions(key string) ([]tagOption, error) {
	if opts, ok := p.options[key]; ok {
		return opts, nil
	}
	records, err := p.c.searchAll(p.ctx, &pb.RpcObjectSearchRequest{
		SpaceId: p.spaceID,
		Filters: []*model.BlockContentDataviewFilter{
			{
				RelationKey: "resolvedLayout",
				Condition:   model.BlockContentDataviewFilter_Equal,
				Value:       &types.Value{Kind: &types.Value_NumberValue{NumberValue: float64(model.ObjectType_relationOption)}},
			},
			{
				RelationKey: "relationKey",
				Condition:   model.BlockContentDataviewFilter_Equal,
				Value:       &types.Value{Kind: &types.Value_StringValue{StringValue: key}},
			},
		},
		Keys: []string{"id", "name", "apiObjectKey", "uniqueKey"},
	}, "the options of "+key)
	if err != nil {
		return nil, err
	}
	opts := make([]tagOption, 0, len(records))
	for _, r := range records {
		o := tagOption{}
		o.id, _ = fromProtoValue(structField(r, "id")).(string)
		o.name, _ = fromProtoValue(structField(r, "name")).(string)
		o.key, _ = fromProtoValue(structField(r, "apiObjectKey")).(string)
		if o.key == "" {
			// The REST key derived from the unique key (core/api/util ToTagApiKey).
			unique, _ := fromProtoValue(structField(r, "uniqueKey")).(string)
			o.key = strings.TrimPrefix(unique, "opt-")
		}
		if o.id != "" {
			opts = append(opts, o)
		}
	}
	p.options[key] = opts
	return opts, nil
}

// tagMatches finds the options a value names: its id, else its exact key, else
// its name in any case, else its key in another spelling (Done_Soon for
// done_soon). The exact key comes before the name so that every key REST takes
// keeps naming the tag it named there.
func tagMatches(opts []tagOption, v string) []tagOption {
	for _, o := range opts {
		if o.id == v {
			return []tagOption{o}
		}
	}
	for _, o := range opts {
		if o.key != "" && o.key == v {
			return []tagOption{o}
		}
	}
	var hits []tagOption
	for _, o := range opts {
		if strings.EqualFold(o.name, v) {
			hits = append(hits, o)
		}
	}
	if len(hits) > 0 {
		return hits
	}
	for _, o := range opts {
		if o.key != "" && normalizeKey(o.key) == normalizeKey(v) {
			hits = append(hits, o)
		}
	}
	return hits
}

// resolveTags turns tag names or keys into ids, among the options of that one
// property. heart stores whatever string it gets, and a name instead of an id
// silently matches nothing — the app even shows such a filter as "empty".
func (p *filterPrep) resolveTags(key string, values []any, keep map[string]bool) ([]any, error) {
	out := make([]any, 0, len(values))
	pending := false
	for _, raw := range values {
		if !keep[raw.(string)] {
			pending = true
		}
	}
	if !pending {
		return append(out, values...), nil
	}
	opts, err := p.tagOptions(key)
	if err != nil {
		return nil, err
	}
	for _, raw := range values {
		if keep[raw.(string)] {
			out = append(out, raw)
			continue
		}
		v := strings.TrimSpace(raw.(string))
		hits := tagMatches(opts, v)
		switch len(hits) {
		case 1:
			out = append(out, hits[0].id)
		case 0:
			names := make([]string, 0, len(opts))
			for _, o := range opts {
				names = append(names, o.name)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("%s has no tag %q; its tags are: %s", p.keys.describe(key), v, strings.Join(names, ", "))
		default:
			ids := make([]string, 0, len(hits))
			for _, h := range hits {
				ids = append(ids, h.id)
			}
			return nil, fmt.Errorf("%s has %d tags named %q; pass the id of the one you mean: %s", p.keys.describe(key), len(hits), v, strings.Join(ids, ", "))
		}
	}
	return out, nil
}

// prepareFilter checks and completes one filter and converts it.
//
// stored is the filter of the same id already on the view, if any. It serves
// twice: an entry that names a stored filter may stay inactive (the app's
// "+ Filter" leaves exactly such filters behind, and a view holding one must
// still be writable as a whole), and the stored struct is the base the new
// values are written onto, so a field this code does not model survives.
func (p *filterPrep) prepareFilter(spec FilterSpec, stored *model.BlockContentDataviewFilter, depth int) (*model.BlockContentDataviewFilter, error) {
	if depth > maxFilterDepth {
		return nil, fmt.Errorf("filter groups nest at most %d levels deep", maxFilterDepth)
	}
	op := strings.ToLower(strings.TrimSpace(spec.Operator))
	if _, group := filterGroupOperators[op]; group || len(spec.NestedFilters) > 0 {
		return p.prepareGroup(spec, stored, depth)
	}
	if op != "" && op != "no" {
		return nil, fmt.Errorf(filterOperatorRefusal, spec.Operator)
	}

	key, err := p.keys.resolve(spec.RelationKey)
	if err != nil {
		// A stored filter on a property that has since been deleted or
		// archived is written back as it is: the app keeps such filters (and
		// ignores them), and refusing them would make every view holding one
		// impossible to rewrite. query-inspect warns about them.
		if !keepsStoredKey(stored, spec.RelationKey) {
			return nil, err
		}
		key = stored.RelationKey
	}
	spec.RelationKey = key
	condition, err := lookupEnum(filterConditions, spec.Condition, "filter condition", false)
	if err != nil {
		return nil, err
	}
	if condition == model.BlockContentDataviewFilter_None {
		return nil, errors.New("filter condition none filters nothing; pick a condition")
	}

	format, known := p.format(key)
	if spec.Format != "" {
		given, err := lookupEnum(relationFormats, spec.Format, "property format", false)
		if err != nil {
			return nil, err
		}
		if !known {
			format, known = given, true
		} else if !formatsCompatible(given, format) {
			return nil, fmt.Errorf("%s has the format %s, not %s; omit format and it is taken from the property",
				p.keys.describe(key), strings.ToLower(format.String()), spec.Format)
		}
	}
	if known {
		spec.Format = strings.ToLower(format.String())
	}

	quick := model.BlockContentDataviewFilter_ExactDate
	if spec.QuickOption != "" {
		quick, err = lookupEnum(filterQuickOptions, spec.QuickOption, "quick option", false)
		if err != nil {
			return nil, err
		}
		if quick != model.BlockContentDataviewFilter_ExactDate && format != model.RelationFormat_date {
			return nil, fmt.Errorf("quick_option is for date properties; %s is not one", p.keys.describe(key))
		}
	}

	value := spec.Value
	if known {
		// A property whose format nobody can tell keeps its value as given.
		if value, err = normalizeFilterValue(format, condition, quick, spec.Value); err != nil {
			return nil, fmt.Errorf("%s: %w", p.keys.describe(key), err)
		}
	} else if noValueCondition(condition) {
		value = nil
	}
	if known && (format == model.RelationFormat_tag || format == model.RelationFormat_status) && !noValueCondition(condition) {
		if list, ok := value.([]any); ok && len(list) > 0 {
			// Ids already stored in this filter stay, even when the tag has
			// been deleted since: the app keeps them too.
			if value, err = p.resolveTags(key, list, storedStrings(stored)); err != nil {
				return nil, err
			}
		}
	}
	spec.Value = value

	out, err := spec.toModel(stored)
	if err != nil {
		return nil, err
	}
	if reason := inactiveReason(out); reason != "" && stored == nil {
		if condition == model.BlockContentDataviewFilter_Exists {
			return nil, fmt.Errorf("the exists condition is not applied by the Anytype app; use not_empty")
		}
		return nil, fmt.Errorf("this filter on %s would filter nothing: %s, and the app ignores such a filter. Give it a value, or use empty / not_empty",
			p.keys.describe(key), reason)
	}
	return out, nil
}

// prepareGroup prepares an AND/OR group, the app's "advanced filter".
func (p *filterPrep) prepareGroup(spec FilterSpec, stored *model.BlockContentDataviewFilter, depth int) (*model.BlockContentDataviewFilter, error) {
	op, ok := filterGroupOperators[strings.ToLower(strings.TrimSpace(spec.Operator))]
	if !ok {
		return nil, errors.New("nested_filters needs operator and or or")
	}
	if len(spec.NestedFilters) == 0 {
		return nil, fmt.Errorf("a filter group with operator %s needs nested_filters", spec.Operator)
	}
	if strings.TrimSpace(spec.RelationKey) != "" || (spec.Condition != "" && !strings.EqualFold(spec.Condition, "none")) {
		return nil, fmt.Errorf("operator %s makes this a filter group, which has no property_key or condition of its own: put the rules into nested_filters, or omit operator for a single filter", spec.Operator)
	}
	// Nested filters carry no id in the app, so a stored group lets its
	// rules stay inactive as a whole, as the app does with a half-filled rule.
	var base *model.BlockContentDataviewFilter
	if stored != nil {
		base = cloneFilter(stored)
	}
	out := base
	if out == nil {
		out = &model.BlockContentDataviewFilter{}
	}
	out.Id = spec.ID
	out.Operator = op
	out.RelationKey = ""
	out.Condition = model.BlockContentDataviewFilter_None
	out.Value = nil
	out.QuickOption = model.BlockContentDataviewFilter_ExactDate
	out.IncludeTime = false
	out.NestedFilters = nil
	for i, n := range spec.NestedFilters {
		var storedChild *model.BlockContentDataviewFilter
		if stored != nil {
			// Keep the stored rule as the base when it is the same position,
			// and let it stay inactive like the stored group.
			if i < len(stored.NestedFilters) {
				storedChild = stored.NestedFilters[i]
			} else {
				storedChild = &model.BlockContentDataviewFilter{}
			}
		}
		child, err := p.prepareFilter(n, storedChild, depth+1)
		if err != nil {
			return nil, fmt.Errorf("nested_filters[%d]: %w", i, err)
		}
		out.NestedFilters = append(out.NestedFilters, child)
	}
	if stored == nil && !filterActive(out) {
		return nil, errors.New("every rule of this filter group would filter nothing")
	}
	return out, nil
}

// prepareSort checks and completes one sort. stored is the sort of the same id
// already on the view, used as the base so CustomOrder and NoCollate survive.
func (p *filterPrep) prepareSort(spec SortSpec, stored *model.BlockContentDataviewSort) (*model.BlockContentDataviewSort, error) {
	key, err := p.keys.resolve(spec.RelationKey)
	if err != nil {
		// As for filters: a stored sort on a property that is gone stays.
		if stored == nil || stored.RelationKey == "" || strings.TrimSpace(spec.RelationKey) != stored.RelationKey {
			return nil, err
		}
		key = stored.RelationKey
	}
	spec.RelationKey = key
	if strings.EqualFold(strings.TrimSpace(spec.Type), "custom") && stored == nil {
		return nil, errors.New("sort direction custom needs an order that this tool cannot carry; use asc or desc, or query-order-set for a hand-made row order")
	}
	if format, known := p.format(key); known {
		spec.Format = strings.ToLower(format.String())
	}
	out, err := spec.toModel(stored)
	if err != nil {
		return nil, err
	}
	if out.Id == "" {
		out.Id = newObjectID()
	}
	return out, nil
}

// prepareFilters prepares a whole filter list against the filters stored on
// the view, matched by id. Nothing is written here, so a refused entry leaves
// the view untouched.
func (p *filterPrep) prepareFilters(specs []FilterSpec, stored []*model.BlockContentDataviewFilter) ([]*model.BlockContentDataviewFilter, error) {
	byID := make(map[string]*model.BlockContentDataviewFilter, len(stored))
	for _, f := range stored {
		if f.Id != "" {
			byID[f.Id] = f
		}
	}
	seen := make(map[string]bool, len(specs))
	out := make([]*model.BlockContentDataviewFilter, 0, len(specs))
	for i, spec := range specs {
		if spec.ID != "" {
			if seen[spec.ID] {
				return nil, fmt.Errorf("filters[%d]: the id %s appears twice", i, spec.ID)
			}
			seen[spec.ID] = true
		}
		f, err := p.prepareFilter(spec, byID[spec.ID], 0)
		if err != nil {
			return nil, fmt.Errorf("filters[%d]: %w", i, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// prepareSorts is prepareFilters for sorts.
func (p *filterPrep) prepareSorts(specs []SortSpec, stored []*model.BlockContentDataviewSort) ([]*model.BlockContentDataviewSort, error) {
	byID := make(map[string]*model.BlockContentDataviewSort, len(stored))
	for _, s := range stored {
		if s.Id != "" {
			byID[s.Id] = s
		}
	}
	seen := make(map[string]bool, len(specs))
	out := make([]*model.BlockContentDataviewSort, 0, len(specs))
	for i, spec := range specs {
		if spec.ID != "" {
			if seen[spec.ID] {
				return nil, fmt.Errorf("sorts[%d]: the id %s appears twice", i, spec.ID)
			}
			seen[spec.ID] = true
		}
		s, err := p.prepareSort(spec, byID[spec.ID])
		if err != nil {
			return nil, fmt.Errorf("sorts[%d]: %w", i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// keepsStoredKey reports whether an entry names the same property as the
// stored filter it rewrites, so its key may stay even if it no longer resolves.
func keepsStoredKey(stored *model.BlockContentDataviewFilter, key string) bool {
	return stored != nil && stored.RelationKey != "" && strings.TrimSpace(key) == stored.RelationKey
}

// storedStrings lists the string values a stored filter holds.
func storedStrings(stored *model.BlockContentDataviewFilter) map[string]bool {
	out := map[string]bool{}
	if stored == nil {
		return out
	}
	switch v := fromProtoValue(stored.Value).(type) {
	case string:
		out[v] = true
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

func cloneFilter(f *model.BlockContentDataviewFilter) *model.BlockContentDataviewFilter {
	if f == nil {
		return nil
	}
	c := *f
	c.NestedFilters = append([]*model.BlockContentDataviewFilter(nil), f.NestedFilters...)
	return &c
}

// newObjectID makes an id in the form heart uses for filters and sorts
// (a BSON ObjectId: four bytes of time, eight random), so the id of a new sort
// is known without reading the view back.
func newObjectID() string {
	var b [12]byte
	binary.BigEndian.PutUint32(b[:4], uint32(time.Now().Unix()))
	_, _ = rand.Read(b[4:])
	return hex.EncodeToString(b[:])
}

// WalkFilters calls fn for every filter, groups and their nested rules
// included, so a scan for referenced tags or properties sees inside groups.
func WalkFilters(filters []FilterSpec, fn func(FilterSpec)) {
	for _, f := range filters {
		fn(f)
		WalkFilters(f.NestedFilters, fn)
	}
}
