package anytypefiles

// Translating property keys for dataviews.
//
// Anytype has two names for the same property. The REST API reports the one it
// calls a key — due_date, linked_projects — while the object index and every
// dataview filter, sort and column use an internal relation key: dueDate,
// linkedProjects. For plenty of properties the two coincide (done, name, tag),
// which is what makes the difference so easy to miss.
//
// The two can be much further apart than a change of case. A property created
// through the API or in the app gets a generated key — 6a87593ad7f55319fc7b1d73
// — while REST reports the snake_cased name it stores as apiObjectKey, so a
// property named Person is "person" over REST and something entirely different
// underneath (anytype-heart, objectcreator/relation.go and util.go). The
// internal key is not exposed by any REST endpoint at all: apimodel.Property
// carries it as RelationKey with a json:"-" tag. A caller therefore cannot look
// it up; translating here is the only way it can be got right.
//
// A filter written with the REST spelling is accepted, stored and echoed back
// unchanged, and then matches nothing at all. Verified: filtering tasks on
// linked_projects returned no rows, while the identical filter on
// linkedProjects returned the expected task. A sort behaves the same way, just
// with no visible symptom whatsoever.
//
// Callers should not have to know which spelling a property happens to use, so
// both are accepted and translated here — and a key that matches neither is an
// error, because the alternative is a filter that quietly does nothing.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/anyproto/anytype-heart/core/domain"
	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/bundle"
	"github.com/anyproto/anytype-heart/pkg/lib/localstore/addr"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

// normalizeKey strips the one difference between the two spellings that is not
// a difference in meaning, so due_date and dueDate compare equal.
func normalizeKey(key string) string {
	return strings.ToLower(strings.ReplaceAll(key, "_", ""))
}

// bundledKeys maps the normalised spelling of every relation anytype ships to
// the key itself.
//
// A bundled relation exists in every space whether or not that space's index
// holds an object for it — heart's FetchRelationByKey asks the bundle before it
// asks the index, for exactly that reason. Without the same fallback here, url
// or starred would be refused in a space that has simply never used them, while
// anytype itself accepts them.
var bundledKeys = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	for _, url := range bundle.ListRelationsUrls() {
		key := strings.TrimPrefix(url, addr.BundledRelationURLPrefix)
		out[normalizeKey(key)] = key
	}
	return out
})

// relationKeyMap translates the keys of one space.
type relationKeyMap struct {
	// byAPIKey maps the REST spelling to the internal one.
	byAPIKey map[string]string
	// byInternal is the inverse, and only holds the entries where the two
	// spellings actually differ. It is what lets a read report the key the
	// caller can look up, next to the internal one a dataview stores.
	byInternal map[string]string
	// internal holds every key that is already internal.
	internal map[string]bool
	// ids holds each property's object id. Only the membership check uses it:
	// heart's REST cache is keyed by id as well as by both spellings
	// (core/api/service/cache_manager.go), so a bafyrei... id names an existing
	// property there. resolve deliberately ignores it — a dataview filter needs
	// the relation key, and an id in that position is a mistake worth catching.
	ids map[string]bool
	// names is used to make error messages helpful.
	names map[string]string
	// formats records each property's format, which decides what a view can do
	// with it — grouping a kanban board is the case that matters.
	formats map[string]model.RelationFormat
}

// kanbanGroupFormats are the property formats anytype can group a board by.
//
// core/kanban/service.go registers a Grouper for exactly these three; anything
// else makes Grouper() answer "unsupported relation format", the group list
// comes back empty and the client draws a board with no columns at all. The
// view itself saves without complaint, which is why this is checked here.
var kanbanGroupFormats = map[model.RelationFormat]bool{
	model.RelationFormat_status:   true,
	model.RelationFormat_tag:      true,
	model.RelationFormat_checkbox: true,
}

// loadRelationKeys reads every property of a space and records both spellings.
func (c *Client) loadRelationKeys(ctx context.Context, spaceID string) (*relationKeyMap, error) {
	records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
		SpaceId: spaceID,
		Filters: []*model.BlockContentDataviewFilter{
			{
				RelationKey: "resolvedLayout",
				Condition:   model.BlockContentDataviewFilter_Equal,
				Value: &types.Value{Kind: &types.Value_NumberValue{
					NumberValue: float64(model.ObjectType_relation)}},
			},
		},
		Keys: []string{"id", "relationKey", "apiObjectKey", "name", "relationFormat"},
	}, "the property definitions")
	if err != nil {
		return nil, err
	}

	out := &relationKeyMap{
		byAPIKey:   make(map[string]string, len(records)),
		byInternal: make(map[string]string, len(records)),
		internal:   make(map[string]bool, len(records)),
		ids:        make(map[string]bool, len(records)),
		names:      make(map[string]string, len(records)),
		formats:    make(map[string]model.RelationFormat, len(records)),
	}
	for _, record := range records {
		key, _ := fromProtoValue(structField(record, "relationKey")).(string)
		if key == "" {
			continue
		}
		out.internal[key] = true
		if id, _ := fromProtoValue(structField(record, "id")).(string); id != "" {
			out.ids[id] = true
		}
		if name, ok := fromProtoValue(structField(record, "name")).(string); ok {
			out.names[key] = name
		}
		if format, ok := fromProtoValue(structField(record, "relationFormat")).(float64); ok {
			out.formats[key] = model.RelationFormat(int32(format))
		}
		if apiKey, _ := fromProtoValue(structField(record, "apiObjectKey")).(string); apiKey != "" && apiKey != key {
			out.byAPIKey[apiKey] = key
			out.byInternal[key] = apiKey
		}
	}
	return out, nil
}

// apiKey answers the spelling list-properties reports for an internal relation
// key, or "" when there is nothing to add.
//
// "" covers both cases where naming a second key would be noise or a lie: a
// property whose two spellings coincide (done, name, tag), and one this map has
// never heard of. Plenty of properties genuinely carry a generated key like
// 6a83275740bea100015faae7 as their PUBLIC key — in this space 49 of 103 do —
// so a caller must never assume a hex-looking key is the internal one.
//
// Only the index answers here, deliberately. Deriving a REST spelling for a
// bundled relation that the index does not carry (dueDate -> due_date) would
// name a key that list-properties does not report either, since that endpoint
// reads the same index — a plausible-looking value the caller cannot look up is
// worse than no value at all.
func (m *relationKeyMap) apiKey(internal string) string {
	if m == nil {
		return ""
	}
	return m.byInternal[internal]
}

// resolve turns whichever spelling the caller used into the internal one.
func (m *relationKeyMap) resolve(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("a property key is required")
	}
	if m.internal[key] {
		return key, nil
	}
	if internal, ok := m.byAPIKey[key]; ok {
		return internal, nil
	}
	if bundle.HasRelation(domain.RelationKey(key)) {
		return key, nil
	}
	if bundled, ok := bundledKeys()[normalizeKey(key)]; ok {
		return bundled, nil
	}
	return "", fmt.Errorf(
		"no property with the key %q exists in this space%s. "+
			"Use a key from list-properties; a key that matches no property is refused here, "+
			"because anytype would store it and then quietly do nothing",
		key, m.suggest(key))
}

// resolvePropertyKey translates one property key for a call outside the
// dataview world. Every gRPC request that names a property — modifying details,
// a relation block, the order of a property's options — needs the internal
// spelling just as much as a filter does, and the REST tools hand the caller
// the other one.
func (c *Client) resolvePropertyKey(ctx context.Context, spaceID, key string) (string, error) {
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return "", err
	}
	return keys.resolve(key)
}

// resolvePropertyKeys translates a list of them, reading the space's properties
// once for the whole list.
func (c *Client) resolvePropertyKeys(ctx context.Context, spaceID, label string, in []string) ([]string, error) {
	if len(in) == 0 {
		return in, nil
	}
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(in))
	for i, key := range in {
		internal, err := keys.resolve(key)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", label, i, err)
		}
		out = append(out, internal)
	}
	return out, nil
}

// suggest offers near misses, since the usual mistake is the wrong spelling of
// a real property rather than an invented one.
func (m *relationKeyMap) suggest(key string) string {
	lowered := normalizeKey(key)
	var hits []string
	for candidate := range m.internal {
		if normalizeKey(candidate) == lowered {
			hits = append(hits, candidate)
		}
	}
	for apiKey, internal := range m.byAPIKey {
		if normalizeKey(apiKey) == lowered {
			hits = append(hits, internal)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Strings(hits)
	return fmt.Sprintf(" (did you mean %s?)", strings.Join(hits, " or "))
}

// describe names a property the way the user knows it, for error messages.
func (m *relationKeyMap) describe(key string) string {
	if name := m.names[key]; name != "" && name != key {
		return fmt.Sprintf("%q (%s)", key, name)
	}
	return fmt.Sprintf("%q", key)
}

// validateGrouping refuses a kanban board that cannot draw any columns.
//
// The check runs against the view as it will be stored, so it catches both
// "group by this object relation" and "turn this view into a kanban" where the
// grouping was set earlier. A grouping key on a non-kanban view is left alone:
// it is inert there, and views are routinely switched back and forth.
func (m *relationKeyMap) validateGrouping(view *model.BlockContentDataviewView) error {
	if view.Type != model.BlockContentDataviewView_Kanban {
		return nil
	}
	if strings.TrimSpace(view.GroupRelationKey) == "" {
		return errors.New(
			"a kanban view needs group_property_key, otherwise the board has no columns. " +
				"Group by a select, multi-select or checkbox property")
	}
	format, known := m.formats[view.GroupRelationKey]
	if !known {
		return nil
	}
	if kanbanGroupFormats[format] {
		return nil
	}
	return fmt.Errorf(
		"kanban cannot group by %s: it has the format %s, and anytype only groups boards by select (status), multi-select (tag) or checkbox properties. "+
			"The view would save and then render as an empty board. Group by a select property, or use a table or gallery layout instead",
		m.describe(view.GroupRelationKey), strings.TrimPrefix(format.String(), "RelationFormat_"))
}

// applyToView translates every property key a view configuration carries.
func (m *relationKeyMap) applyToView(spec *ViewSpec) error {
	for i := range spec.Filters {
		key, err := m.resolve(spec.Filters[i].RelationKey)
		if err != nil {
			return fmt.Errorf("filters[%d]: %w", i, err)
		}
		spec.Filters[i].RelationKey = key
	}
	for i := range spec.Sorts {
		key, err := m.resolve(spec.Sorts[i].RelationKey)
		if err != nil {
			return fmt.Errorf("sorts[%d]: %w", i, err)
		}
		spec.Sorts[i].RelationKey = key
	}
	for i := range spec.Relations {
		key, err := m.resolve(spec.Relations[i].Key)
		if err != nil {
			return fmt.Errorf("relations[%d]: %w", i, err)
		}
		spec.Relations[i].Key = key
	}
	if spec.GroupRelationKey != "" {
		key, err := m.resolve(spec.GroupRelationKey)
		if err != nil {
			return fmt.Errorf("group_property_key: %w", err)
		}
		spec.GroupRelationKey = key
	}
	if spec.CoverRelationKey != "" {
		key, err := m.resolve(spec.CoverRelationKey)
		if err != nil {
			return fmt.Errorf("cover_property_key: %w", err)
		}
		spec.CoverRelationKey = key
	}
	return nil
}

// LoadRelationKeyIndex maps each property's object id to its internal relation
// key, so a REST listing can be joined to what the index actually stores.
func (c *Client) LoadRelationKeyIndex(ctx context.Context, spaceID string) (map[string]string, error) {
	records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
		SpaceId: spaceID,
		Filters: []*model.BlockContentDataviewFilter{
			{
				RelationKey: "resolvedLayout",
				Condition:   model.BlockContentDataviewFilter_Equal,
				Value: &types.Value{Kind: &types.Value_NumberValue{
					NumberValue: float64(model.ObjectType_relation)}},
			},
		},
		Keys: []string{"id", "relationKey"},
	}, "the property definitions")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(records))
	for _, record := range records {
		id, _ := fromProtoValue(structField(record, "id")).(string)
		key, _ := fromProtoValue(structField(record, "relationKey")).(string)
		if id != "" && key != "" {
			out[id] = key
		}
	}
	return out, nil
}

// UnknownPropertyKeys answers which of the given keys name no property of this
// space, in the order they were passed and without duplicates.
//
// It exists for create-type and update-type, whose properties list is the one
// place where an unknown key is NOT an error: heart's buildRelationIds
// (core/api/service/type.go) creates a property for it instead, so a typo grows
// the space schema rather than failing. A caller that wants to link only
// existing properties has no way to say so; this lets the tool check first.
//
// Membership is deliberately as wide as heart's own lookup — both spellings,
// the property's object id, and the bundled relations that exist in every space
// whether or not the index holds an object for them. Being narrower here would
// reject calls that work today, which is the worse failure of the two: a missed
// typo only restores the old behaviour, a false alarm breaks a valid call.
func (c *Client) UnknownPropertyKeys(ctx context.Context, spaceID string, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	known, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(keys))
	unknown := make([]string, 0)
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if known.ids[key] {
			continue
		}
		if _, err := known.resolve(key); err != nil {
			unknown = append(unknown, key)
		}
	}
	return unknown, nil
}

// SuggestPropertyKey offers the near miss for an unknown key, or "" when there
// is none. The usual mistake is a misspelling of a real property, so naming the
// candidate turns a refusal into a correction.
func (c *Client) SuggestPropertyKey(ctx context.Context, spaceID, key string) string {
	known, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return ""
	}
	return known.suggest(key)
}
