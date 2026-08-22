package anytypefiles

// Working out which options of a select/multi-select property are actually in
// use — the analysis a cleanup has to get right, because acting on it deletes
// things.
//
// Two traps decide whether the answer is trustworthy:
//
//  1. ObjectSearch silently appends "isArchived != true" whenever the caller
//     supplies no filter on that relation of their own (see injectDefaultFilters
//     in pkg/lib/database). A single naive search therefore never sees the bin,
//     and an option used only by an archived object would look unused. The scan
//     below runs twice with explicit, opposite archive filters and takes the
//     union, so nothing is hidden.
//
//  2. An option can also be referenced by a view's filter rather than by an
//     object's value — a Set that filters on "status is Done" holds that
//     option's id in its dataview block, where no object search will find it.
//     Deleting it would quietly break the query, so those references are
//     collected separately.
//
//  3. The key a property is filtered by is NOT the key the REST API reports.
//     REST calls it prb_usage_prop; the index stores the value under an
//     internal relation key such as 6a779fce40bea1000164ea39, carried by the
//     property object in its relationKey detail. Searching by the REST key
//     matches nothing at all — and "nothing" reads exactly like "unused".
//
// The one thing deliberately not counted is permanently deleted objects: they
// are gone, and their references with them.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

// ObjectUsage is one object that references an option.
type ObjectUsage struct {
	ObjectID string `json:"object_id"`
	Name     string `json:"name,omitempty"`
	TypeID   string `json:"type_id,omitempty"`
	Archived bool   `json:"archived,omitempty"`
}

// PropertyUsage is the complete picture for one property.
type PropertyUsage struct {
	// ByOption maps an option id to the objects using it.
	ByOption map[string][]ObjectUsage
	// ViewRefs maps an option id to the number of view filters mentioning it.
	ViewRefs map[string]int
	// LiveViewRefs counts only the filters of queries that are not in the bin.
	LiveViewRefs map[string]int
	// ScannedObjects is how many objects carried a value for the property.
	ScannedObjects int
	// ScannedViews is how many query/collection objects were inspected.
	ScannedViews int
	// RelationKey is the internal key the values were found under.
	RelationKey string
}

// searchPageSize is how many records one ObjectSearch call fetches.
const searchPageSize = 200

// searchAll runs a search to completion and refuses to return a partial answer.
//
// Two things make this fiddly, and both would silently produce a short list —
// which for a cleanup means deleting something that is in use:
//
//   - Limit is not optional. Leaving it at zero returns no records at all
//     rather than everything, so every page states its size explicitly.
//   - A single page is not the whole answer. The search reports the full count
//     when asked, so the pages are collected until that count is reached and an
//     error is raised if they never are.
func (c *Client) searchAll(ctx context.Context, req *pb.RpcObjectSearchRequest, what string) ([]*types.Struct, error) {
	var collected []*types.Struct
	total := int64(-1)

	for offset := int32(0); ; offset += searchPageSize {
		page := *req
		page.Offset = offset
		page.Limit = searchPageSize
		page.NeedTotal = true

		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.ObjectSearch(callCtx, &page)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("gRPC ObjectSearch(%s) failed: %w", what, err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcObjectSearchResponseError_NULL {
			return nil, fmt.Errorf("ObjectSearch(%s) error (%s): %s", what, resp.Error.Code, resp.Error.Description)
		}
		if total < 0 {
			total = resp.Total
		}
		collected = append(collected, resp.Records...)

		if len(resp.Records) == 0 || int64(len(collected)) >= total {
			break
		}
	}

	if total >= 0 && int64(len(collected)) != total {
		return nil, fmt.Errorf(
			"the scan of %s is incomplete: %d of %d records were returned, so an option could look unused when it is not",
			what, len(collected), total)
	}
	return collected, nil
}

// resolveRelationKey translates a property's object id into the key its values
// are actually stored under.
//
// Getting this wrong is silent: a search on the wrong key returns no records,
// which a usage count cannot tell apart from an option nobody uses.
func (c *Client) resolveRelationKey(ctx context.Context, spaceID, propertyObjectID string) (string, error) {
	records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
		SpaceId: spaceID,
		Filters: []*model.BlockContentDataviewFilter{
			{
				RelationKey: "id",
				Condition:   model.BlockContentDataviewFilter_Equal,
				Value:       &types.Value{Kind: &types.Value_StringValue{StringValue: propertyObjectID}},
			},
		},
		Keys: []string{"id", "relationKey", "name"},
	}, "the property definition")
	if err != nil {
		return "", err
	}
	if len(records) == 0 {
		return "", fmt.Errorf("no property with the id %s exists in this space", propertyObjectID)
	}
	key, _ := fromProtoValue(structField(records[0], "relationKey")).(string)
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("the property %s carries no relationKey, so its usage cannot be counted safely", propertyObjectID)
	}
	return key, nil
}

// searchByRelation returns the objects that hold any value for a relation.
//
// archived selects which half of the space to look at. The filter on
// isArchived is always explicit, both to pick the half and to stop heart from
// quietly adding its own.
func (c *Client) searchByRelation(ctx context.Context, spaceID, relationKey string, archived bool) ([]*types.Struct, error) {
	archivedFilter := &model.BlockContentDataviewFilter{
		RelationKey: "isArchived",
		Condition:   model.BlockContentDataviewFilter_NotEqual,
		Value:       &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
	}
	what := "live objects"
	if archived {
		archivedFilter.Condition = model.BlockContentDataviewFilter_Equal
		what = "objects in the bin"
	}
	return c.searchAll(ctx, &pb.RpcObjectSearchRequest{
		SpaceId: spaceID,
		Filters: []*model.BlockContentDataviewFilter{
			{
				RelationKey: relationKey,
				Condition:   model.BlockContentDataviewFilter_NotEmpty,
			},
			archivedFilter,
		},
		Keys: []string{"id", "name", "type", "isArchived", relationKey},
	}, what)
}

// optionIDsFrom reads a relation value as a list of option ids, covering both
// single select (one string) and multi-select (a list).
func optionIDsFrom(value *types.Value) []string {
	if value == nil {
		return nil
	}
	switch kind := value.Kind.(type) {
	case *types.Value_StringValue:
		if kind.StringValue == "" {
			return nil
		}
		return []string{kind.StringValue}
	case *types.Value_ListValue:
		out := make([]string, 0, len(kind.ListValue.GetValues()))
		for _, item := range kind.ListValue.GetValues() {
			if s, ok := item.Kind.(*types.Value_StringValue); ok && s.StringValue != "" {
				out = append(out, s.StringValue)
			}
		}
		return out
	}
	return nil
}

func structField(s *types.Struct, key string) *types.Value {
	if s == nil {
		return nil
	}
	return s.Fields[key]
}

// collectViewReferences counts every id mentioned in the filters of queries and
// collections, which no object search can see.
//
// It deliberately does NOT restrict itself to filters on the property being
// analysed. A dataview filter stores whichever relation key it was created
// with — the GUI writes the internal one, this server's query-filter-add writes
// the one the caller passed — so matching on the key is unreliable in exactly
// the direction that gets data deleted. Option ids are unique, so a filter
// mentioning one is a reference to it whatever relation it hangs off, and
// counting every value is both simpler and strictly safer.
func (c *Client) collectViewReferences(ctx context.Context, spaceID string) (map[string]int, map[string]int, int, error) {
	refs := make(map[string]int)
	live := make(map[string]int)
	scanned := 0

	// Archived queries are inspected too: an archived query still holds its
	// filters, and restoring it would resurrect a reference to an option
	// removed in the meantime. They are counted separately from live ones so
	// clean-unused-tags can offer to disregard the bin.
	for _, archived := range []bool{false, true} {
		condition := model.BlockContentDataviewFilter_NotEqual
		if archived {
			condition = model.BlockContentDataviewFilter_Equal
		}
		records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
			SpaceId: spaceID,
			Filters: []*model.BlockContentDataviewFilter{
				{
					RelationKey: "resolvedLayout",
					Condition:   model.BlockContentDataviewFilter_In,
					Value: &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: []*types.Value{
						{Kind: &types.Value_NumberValue{NumberValue: float64(model.ObjectType_set)}},
						{Kind: &types.Value_NumberValue{NumberValue: float64(model.ObjectType_collection)}},
					}}}},
				},
				{
					RelationKey: "isArchived",
					Condition:   condition,
					Value:       &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
				},
			},
			Keys: []string{"id", "name"},
		}, "queries and collections")
		if err != nil {
			return nil, nil, scanned, err
		}

		for _, record := range records {
			objectID, _ := fromProtoValue(structField(record, "id")).(string)
			if objectID == "" {
				continue
			}
			info, err := c.InspectDataview(ctx, spaceID, objectID, "")
			if err != nil {
				// A query whose dataview cannot be read is not proof of
				// absence, so it is reported rather than quietly treated as
				// holding no references.
				return nil, nil, scanned, fmt.Errorf("could not inspect the views of %s: %w", objectID, err)
			}
			scanned++
			for _, view := range info.Views {
				for _, filter := range view.Filters {
					for _, id := range filterValueStrings(filter.Value) {
						refs[id]++
						if !archived {
							live[id]++
						}
					}
				}
			}
		}
	}
	return refs, live, scanned, nil
}

// filterValueStrings pulls option ids out of a decoded filter value, which may
// be one id or a list of them.
func filterValueStrings(value any) []string {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// AnalysePropertyUsage counts, for every option of a property, how many objects
// and how many view filters reference it.
func (c *Client) AnalysePropertyUsage(ctx context.Context, spaceID, propertyObjectID string) (PropertyUsage, error) {
	if strings.TrimSpace(propertyObjectID) == "" {
		return PropertyUsage{}, errors.New("the property id is required")
	}
	relationKey, err := c.resolveRelationKey(ctx, spaceID, propertyObjectID)
	if err != nil {
		return PropertyUsage{}, err
	}
	usage := PropertyUsage{
		RelationKey:  relationKey,
		ByOption:     make(map[string][]ObjectUsage),
		ViewRefs:     make(map[string]int),
		LiveViewRefs: make(map[string]int),
	}

	for _, archived := range []bool{false, true} {
		records, err := c.searchByRelation(ctx, spaceID, relationKey, archived)
		if err != nil {
			return PropertyUsage{}, err
		}
		for _, record := range records {
			entry := ObjectUsage{Archived: archived}
			if v := structField(record, "id"); v != nil {
				entry.ObjectID, _ = fromProtoValue(v).(string)
			}
			if v := structField(record, "name"); v != nil {
				entry.Name, _ = fromProtoValue(v).(string)
			}
			if v := structField(record, "type"); v != nil {
				entry.TypeID, _ = fromProtoValue(v).(string)
			}
			ids := optionIDsFrom(structField(record, relationKey))
			if len(ids) == 0 {
				continue
			}
			usage.ScannedObjects++
			for _, id := range ids {
				usage.ByOption[id] = append(usage.ByOption[id], entry)
			}
		}
	}

	refs, liveRefs, scannedViews, err := c.collectViewReferences(ctx, spaceID)
	if err != nil {
		return PropertyUsage{}, err
	}
	usage.ViewRefs = refs
	usage.LiveViewRefs = liveRefs
	usage.ScannedViews = scannedViews
	return usage, nil
}

// ArchivedObject is one entry of the bin.
type ArchivedObject struct {
	ObjectID string `json:"object_id"`
	Name     string `json:"name,omitempty"`
	TypeID   string `json:"type_id,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	// CreatedBy names the member who created the object, the same column the
	// desktop bin shows. It is derived by anytype-heart from the identity that
	// signed the object's first change, so in a space several accounts write to
	// it says which one produced the object.
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedByID string `json:"created_by_id,omitempty"`
}

// loadParticipantNames maps participant object ids to member names, so a
// creator can be reported as a name rather than an opaque id.
func (c *Client) loadParticipantNames(ctx context.Context, spaceID string) map[string]string {
	records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
		SpaceId: spaceID,
		Filters: []*model.BlockContentDataviewFilter{
			{
				RelationKey: "resolvedLayout",
				Condition:   model.BlockContentDataviewFilter_Equal,
				Value: &types.Value{Kind: &types.Value_NumberValue{
					NumberValue: float64(model.ObjectType_participant)}},
			},
		},
		Keys: []string{"id", "name"},
	}, "the space members")
	if err != nil {
		return nil // a missing name is not worth failing the listing over
	}
	out := make(map[string]string, len(records))
	for _, record := range records {
		id, _ := fromProtoValue(structField(record, "id")).(string)
		name, _ := fromProtoValue(structField(record, "name")).(string)
		if id != "" {
			out[id] = name
		}
	}
	return out
}

// ListArchived returns everything in a space's bin.
//
// There is no way to do this through the REST API: isArchived sits on its list
// of relations that are never exposed as filterable properties, so the search
// endpoints cannot select on it. Over gRPC it just needs an explicit filter,
// which also stops heart from adding its usual "not archived" default.
func (c *Client) ListArchived(ctx context.Context, spaceID string, typeIDs []string) ([]ArchivedObject, error) {
	filters := []*model.BlockContentDataviewFilter{
		{
			RelationKey: "isArchived",
			Condition:   model.BlockContentDataviewFilter_Equal,
			Value:       &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
		},
	}
	if len(typeIDs) > 0 {
		values := make([]*types.Value, 0, len(typeIDs))
		for _, id := range typeIDs {
			values = append(values, &types.Value{Kind: &types.Value_StringValue{StringValue: id}})
		}
		filters = append(filters, &model.BlockContentDataviewFilter{
			RelationKey: "type",
			Condition:   model.BlockContentDataviewFilter_In,
			Value:       &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: values}}},
		})
	}

	records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
		SpaceId: spaceID, Filters: filters,
		Keys: []string{"id", "name", "type", "snippet", "creator"},
	}, "the bin")
	if err != nil {
		return nil, err
	}
	participants := c.loadParticipantNames(ctx, spaceID)
	out := make([]ArchivedObject, 0, len(records))
	for _, record := range records {
		entry := ArchivedObject{}
		entry.ObjectID, _ = fromProtoValue(structField(record, "id")).(string)
		entry.Name, _ = fromProtoValue(structField(record, "name")).(string)
		entry.TypeID, _ = fromProtoValue(structField(record, "type")).(string)
		entry.Snippet, _ = fromProtoValue(structField(record, "snippet")).(string)
		entry.CreatedByID, _ = fromProtoValue(structField(record, "creator")).(string)
		entry.CreatedBy = participants[entry.CreatedByID]
		if entry.ObjectID != "" {
			out = append(out, entry)
		}
	}
	return out, nil
}

// SchemaUsage counts how much of a space's schema is actually in use.
type SchemaUsage struct {
	// ObjectsByType maps a type id to how many objects carry it.
	ObjectsByType map[string]int
	// ObjectsByRelation maps an internal relation key to how many objects hold a value.
	ObjectsByRelation map[string]int
	// ViewRelations holds the relation keys any view filters, sorts or shows.
	ViewRelations map[string]int
	// TypesInViewSources holds the type ids any query draws from.
	TypesInViewSources map[string]int
	ScannedObjects     int
	ScannedViews       int
}

// AnalyseSchemaUsage walks every object and every view once, so the question
// "what does nothing use any more" can be answered for types and properties
// without a separate pass per candidate.
//
// Archived objects and archived queries count as usage, for the same reason as
// in the tag analysis: restoring one would resurrect the reference.
func (c *Client) AnalyseSchemaUsage(ctx context.Context, spaceID string) (SchemaUsage, error) {
	usage := SchemaUsage{
		ObjectsByType:      map[string]int{},
		ObjectsByRelation:  map[string]int{},
		ViewRelations:      map[string]int{},
		TypesInViewSources: map[string]int{},
	}

	for _, archived := range []bool{false, true} {
		condition := model.BlockContentDataviewFilter_NotEqual
		what := "live objects"
		if archived {
			condition = model.BlockContentDataviewFilter_Equal
			what = "objects in the bin"
		}
		records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
			SpaceId: spaceID,
			Filters: []*model.BlockContentDataviewFilter{
				{
					RelationKey: "isArchived",
					Condition:   condition,
					Value:       &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
				},
			},
			// No Keys: every field of every object is needed, since the point is
			// to see which relations carry a value anywhere.
		}, what)
		if err != nil {
			return SchemaUsage{}, err
		}
		for _, record := range records {
			usage.ScannedObjects++
			if typeID, ok := fromProtoValue(structField(record, "type")).(string); ok && typeID != "" {
				usage.ObjectsByType[typeID]++
			}
			for key, value := range record.GetFields() {
				if isEmptyValue(value) {
					continue
				}
				usage.ObjectsByRelation[key]++
			}
		}
	}

	views, scanned, err := c.collectViewSchemaUsage(ctx, spaceID)
	if err != nil {
		return SchemaUsage{}, err
	}
	usage.ViewRelations = views.relations
	usage.TypesInViewSources = views.sources
	usage.ScannedViews = scanned
	return usage, nil
}

type viewSchemaUsage struct {
	relations map[string]int
	sources   map[string]int
}

// collectViewSchemaUsage records which relations and types the queries of a
// space depend on, which no object scan reveals.
func (c *Client) collectViewSchemaUsage(ctx context.Context, spaceID string) (viewSchemaUsage, int, error) {
	out := viewSchemaUsage{relations: map[string]int{}, sources: map[string]int{}}
	scanned := 0

	for _, archived := range []bool{false, true} {
		condition := model.BlockContentDataviewFilter_NotEqual
		if archived {
			condition = model.BlockContentDataviewFilter_Equal
		}
		records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
			SpaceId: spaceID,
			Filters: []*model.BlockContentDataviewFilter{
				{
					RelationKey: "resolvedLayout",
					Condition:   model.BlockContentDataviewFilter_In,
					Value: &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: []*types.Value{
						{Kind: &types.Value_NumberValue{NumberValue: float64(model.ObjectType_set)}},
						{Kind: &types.Value_NumberValue{NumberValue: float64(model.ObjectType_collection)}},
					}}}},
				},
				{
					RelationKey: "isArchived",
					Condition:   condition,
					Value:       &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
				},
			},
			Keys: []string{"id", "name"},
		}, "queries and collections")
		if err != nil {
			return viewSchemaUsage{}, scanned, err
		}
		for _, record := range records {
			objectID, _ := fromProtoValue(structField(record, "id")).(string)
			if objectID == "" {
				continue
			}
			info, err := c.InspectDataview(ctx, spaceID, objectID, "")
			if err != nil {
				return viewSchemaUsage{}, scanned, fmt.Errorf("could not inspect the views of %s: %w", objectID, err)
			}
			scanned++
			for _, source := range info.Source {
				out.sources[source]++
			}
			for _, view := range info.Views {
				for _, filter := range view.Filters {
					out.relations[filter.RelationKey]++
				}
				for _, s := range view.Sorts {
					out.relations[s.RelationKey]++
				}
				for _, r := range view.Relations {
					if r.IsVisible {
						out.relations[r.Key]++
					}
				}
				if view.GroupRelationKey != "" {
					out.relations[view.GroupRelationKey]++
				}
			}
		}
	}
	return out, scanned, nil
}

// isEmptyValue reports whether a detail carries nothing worth counting.
func isEmptyValue(value *types.Value) bool {
	if value == nil {
		return true
	}
	switch kind := value.Kind.(type) {
	case *types.Value_NullValue:
		return true
	case *types.Value_StringValue:
		return kind.StringValue == ""
	case *types.Value_ListValue:
		return len(kind.ListValue.GetValues()) == 0
	case *types.Value_BoolValue:
		// A checkbox set to false is still a value the property is used for.
		return false
	}
	return false
}
