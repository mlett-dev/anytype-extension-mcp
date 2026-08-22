package anytypefiles

// Space-level information, inline dataviews, version diffing, date objects,
// schema ordering and copying blocks back out as markdown.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

var verticalAligns = map[string]model.BlockVerticalAlign{
	"top":    model.Block_VerticalAlignTop,
	"middle": model.Block_VerticalAlignMiddle,
	"center": model.Block_VerticalAlignMiddle,
	"bottom": model.Block_VerticalAlignBottom,
}

// VerticalAlignNames lists the accepted vertical alignments, for tool schemas.
func VerticalAlignNames() []string { return []string{"top", "middle", "bottom"} }

// FileUsage is a space's storage consumption.
type FileUsage struct {
	FilesCount      uint64 `json:"files_count"`
	BytesUsage      uint64 `json:"bytes_used"`
	BytesLeft       uint64 `json:"bytes_left"`
	BytesLimit      uint64 `json:"bytes_limit"`
	LocalBytesUsage uint64 `json:"local_bytes_used"`
}

// VersionDiff is what changed between two versions of an object.
type VersionDiff struct {
	Added   []BlockInfo `json:"added,omitempty"`
	Removed []BlockInfo `json:"removed,omitempty"`
	Changed []BlockPair `json:"changed,omitempty"`
}

// BlockPair is one block before and after.
type BlockPair struct {
	ID     string `json:"id"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// SpaceFileUsage reports how much storage a space is using.
func (c *Client) SpaceFileUsage(ctx context.Context, spaceID string) (FileUsage, error) {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.FileSpaceUsage(callCtx, &pb.RpcFileSpaceUsageRequest{SpaceId: spaceID})
	if err != nil {
		return FileUsage{}, fmt.Errorf("gRPC FileSpaceUsage failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcFileSpaceUsageResponseError_NULL {
		return FileUsage{}, fmt.Errorf("FileSpaceUsage error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	u := resp.GetUsage()
	return FileUsage{
		FilesCount: u.GetFilesCount(), BytesUsage: u.GetBytesUsage(),
		BytesLeft: u.GetBytesLeft(), BytesLimit: u.GetBytesLimit(),
		LocalBytesUsage: u.GetLocalBytesUsage(),
	}, nil
}

// EmbedQueryBlock inserts an inline view of an existing query or collection,
// or refreshes one that is already there when blockID is given.
//
// Two steps for a new embed, because BlockDataviewCreateFromExistingObject only
// fills a block that is ALREADY a dataview ("block must contain dataView
// content"): create an empty dataview block first, then point it at the target
// object.
//
// The second step is a COPY, not a link. heart's CopyDataviewToBlock assigns
// the target's Views, RelationLinks, GroupOrders and ObjectOrders into the
// block and only TargetObjectId stays a reference — so which objects appear
// keeps following the query, while the layout, filters and columns are a
// snapshot taken now. Nothing in that RPC requires a fresh block, which is what
// makes the refresh possible: pointing it at the existing block a second time
// overwrites the snapshot with the query's current configuration.
func (c *Client) EmbedQueryBlock(ctx context.Context, objectID, targetID, position, queryObjectID, blockID string) (string, error) {
	if strings.TrimSpace(queryObjectID) == "" {
		return "", errors.New("query_object_id is required")
	}
	blockID = strings.TrimSpace(blockID)

	if blockID == "" {
		pos, err := lookupEnum(blockPositions, position, "position", true)
		if err != nil {
			return "", err
		}
		if pos == model.Block_None {
			pos = model.Block_Bottom
		}

		callCtx, cancel := c.contextWithAuth(ctx)
		created, err := c.rpc.BlockCreate(callCtx, &pb.RpcBlockCreateRequest{
			ContextId: objectID, TargetId: targetID, Position: pos,
			Block: &model.Block{Content: &model.BlockContentOfDataview{
				Dataview: &model.BlockContentDataview{},
			}},
		})
		cancel()
		if err != nil {
			return "", fmt.Errorf("gRPC BlockCreate(dataview) failed: %w", err)
		}
		if created.Error != nil && created.Error.Code != pb.RpcBlockCreateResponseError_NULL {
			return "", fmt.Errorf("BlockCreate(dataview) error (%s): %s", created.Error.Code, created.Error.Description)
		}
		blockID = created.BlockId
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockDataviewCreateFromExistingObject(callCtx,
		&pb.RpcBlockDataviewCreateFromExistingObjectRequest{
			ContextId: objectID, BlockId: blockID, TargetObjectId: queryObjectID,
		})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockDataviewCreateFromExistingObject failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockDataviewCreateFromExistingObjectResponseError_NULL {
		return "", fmt.Errorf("BlockDataviewCreateFromExistingObject error (%s): %s",
			resp.Error.Code, resp.Error.Description)
	}
	return blockID, nil
}

// SetSpaceHomepage decides which object a space opens on.
func (c *Client) SetSpaceHomepage(ctx context.Context, spaceID, objectID string) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.WorkspaceSetHomepage(callCtx, &pb.RpcWorkspaceSetHomepageRequest{
		SpaceId: spaceID, Homepage: objectID,
	})
	if err != nil {
		return fmt.Errorf("gRPC WorkspaceSetHomepage failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcWorkspaceSetHomepageResponseError_NULL {
		return fmt.Errorf("WorkspaceSetHomepage error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// SpaceHomepage is what a space opens on, as space-set-homepage sets it.
//
// It is not always an object. anytype stores the setting as the homepage
// relation of the space's workspace object, and heart accepts either an object
// id or one of four constants (core/domain/homepage.go) — "widgets" is what a
// newly created space gets, so the common answer is a constant rather than an
// id.
type SpaceHomepage struct {
	// Value is what is stored, whichever of the two it is.
	Value string `json:"value"`
	// ObjectID is set only when Value names an object.
	ObjectID string `json:"object_id,omitempty"`
	// Constant is set only when Value is one of the built-in screens.
	Constant string `json:"constant,omitempty"`
	Name     string `json:"name,omitempty"`
}

// HomepageConstants lists the built-in screens a space can open on, for tool
// schemas. Mirrors core/domain/homepage.go; lastOpened is deprecated there and
// left out rather than offered.
func HomepageConstants() []string {
	return []string{"widgets", "graph", "chat"}
}

func isHomepageConstant(value string) bool {
	switch value {
	case "widgets", "graph", "chat", "lastOpened":
		return true
	}
	return false
}

// ReadSpaceHomepage answers what a space opens on.
//
// The setting lives on the space's workspace object as the homepage relation, a
// hidden relation that no REST endpoint reports — get-space does not carry it —
// so without this there was a setter with nothing to read it back.
//
// The object holding it is the space's workspace object, which the index
// carries with the dashboard layout — verified against the running server
// rather than assumed, because the layout named "space" belongs to something
// else and finds nothing here.
//
// The explicit resolvedLayout filter is doing double duty: without any such
// filter heart appends "type not in [space]" to every search
// (pkg/lib/database/database.go, addDefaultFilters), and the workspace object
// is exactly what that would exclude.
func (c *Client) ReadSpaceHomepage(ctx context.Context, spaceID string) (SpaceHomepage, error) {
	if strings.TrimSpace(spaceID) == "" {
		return SpaceHomepage{}, errors.New("space_id is required")
	}
	records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
		SpaceId: spaceID,
		Filters: []*model.BlockContentDataviewFilter{
			{
				RelationKey: "resolvedLayout",
				Condition:   model.BlockContentDataviewFilter_Equal,
				Value: &types.Value{Kind: &types.Value_NumberValue{
					NumberValue: float64(model.ObjectType_dashboard)}},
			},
		},
		Keys: []string{"id", "homepage"},
	}, "the space settings")
	if err != nil {
		return SpaceHomepage{}, err
	}
	value := ""
	for _, record := range records {
		if v, _ := fromProtoValue(structField(record, "homepage")).(string); v != "" {
			value = v
			break
		}
	}
	if value == "" {
		return SpaceHomepage{}, nil
	}
	out := SpaceHomepage{Value: value}
	if isHomepageConstant(value) {
		out.Constant = value
		return out, nil
	}
	out.ObjectID = value
	// The name is a convenience, so failing to read it must not turn a
	// successful lookup into an error.
	if flags, err := c.ReadObjectFlags(ctx, spaceID, []string{value}); err == nil && len(flags) == 1 {
		out.Name = flags[0].Name
	}
	return out, nil
}

// ObjectFlags are the per-object switches that object-set-favorite and
// object-set-archived write.
//
// Both were write-only until now: isFavorite and isArchived are hidden
// relations, the REST object payload carries neither, and isArchived is not
// even filterable there — so a caller could set them and never confirm it, and
// could not do a safe read-modify-write.
type ObjectFlags struct {
	ObjectID string `json:"object_id"`
	Name     string `json:"name,omitempty"`
	Favorite bool   `json:"favorite"`
	Archived bool   `json:"archived"`
	Found    bool   `json:"found"`
}

// ReadObjectFlags reports the favorite and archived state of objects.
//
// Two searches rather than one, because a single search cannot see both states:
// heart appends "isArchived != true" to any search that does not mention
// isArchived itself (pkg/lib/database/database.go, addDefaultFilters), so the
// plain lookup returns live objects only. The second pass asks for the bin
// explicitly — the same trick ListArchived uses — and an object found there is
// reported as archived rather than as missing, which is the whole point of a
// flag reader that is meant to confirm object-set-archived.
func (c *Client) ReadObjectFlags(ctx context.Context, spaceID string, objectIDs []string) ([]ObjectFlags, error) {
	if strings.TrimSpace(spaceID) == "" {
		return nil, errors.New("space_id is required")
	}
	if len(objectIDs) == 0 {
		return nil, errors.New("object_ids is required")
	}
	values := make([]*types.Value, 0, len(objectIDs))
	for _, id := range objectIDs {
		values = append(values, &types.Value{Kind: &types.Value_StringValue{StringValue: id}})
	}
	idFilter := &model.BlockContentDataviewFilter{
		RelationKey: "id",
		Condition:   model.BlockContentDataviewFilter_In,
		Value:       &types.Value{Kind: &types.Value_ListValue{ListValue: &types.ListValue{Values: values}}},
	}
	archivedFilter := &model.BlockContentDataviewFilter{
		RelationKey: "isArchived",
		Condition:   model.BlockContentDataviewFilter_Equal,
		Value:       &types.Value{Kind: &types.Value_BoolValue{BoolValue: true}},
	}

	found := make(map[string]ObjectFlags, len(objectIDs))
	collect := func(filters []*model.BlockContentDataviewFilter) error {
		records, err := c.searchAll(ctx, &pb.RpcObjectSearchRequest{
			SpaceId: spaceID, Filters: filters,
			Keys: []string{"id", "name", "isFavorite", "isArchived"},
		}, "the object flags")
		if err != nil {
			return err
		}
		for _, record := range records {
			id, _ := fromProtoValue(structField(record, "id")).(string)
			if id == "" {
				continue
			}
			name, _ := fromProtoValue(structField(record, "name")).(string)
			favorite, _ := fromProtoValue(structField(record, "isFavorite")).(bool)
			archived, _ := fromProtoValue(structField(record, "isArchived")).(bool)
			found[id] = ObjectFlags{
				ObjectID: id, Name: name,
				Favorite: favorite, Archived: archived, Found: true,
			}
		}
		return nil
	}

	if err := collect([]*model.BlockContentDataviewFilter{idFilter}); err != nil {
		return nil, err
	}
	if err := collect([]*model.BlockContentDataviewFilter{idFilter, archivedFilter}); err != nil {
		return nil, err
	}

	// Answer in the order asked, and say so when an id matched nothing rather
	// than dropping it: a missing entry would read as "not favorited".
	out := make([]ObjectFlags, 0, len(objectIDs))
	for _, id := range objectIDs {
		if flags, ok := found[id]; ok {
			out = append(out, flags)
			continue
		}
		out = append(out, ObjectFlags{ObjectID: id})
	}
	return out, nil
}

// DiffVersions reports what changed between two versions of an object.
//
// This compares two snapshots rather than calling HistoryDiffVersions. That RPC
// answers with raw EventMessages — heart's internal event union, a hundred-odd
// variants of which a handful would have to be interpreted to say anything
// useful. Two ShowVersion reads give the same answer in terms the caller
// already understands: blocks that appeared, disappeared, or changed text.
func (c *Client) DiffVersions(ctx context.Context, objectID, fromVersionID, toVersionID string) (VersionDiff, error) {
	if strings.TrimSpace(fromVersionID) == "" || strings.TrimSpace(toVersionID) == "" {
		return VersionDiff{}, errors.New("from_version_id and to_version_id are required")
	}
	before, err := c.ShowVersion(ctx, objectID, fromVersionID)
	if err != nil {
		return VersionDiff{}, fmt.Errorf("reading %s: %w", fromVersionID, err)
	}
	after, err := c.ShowVersion(ctx, objectID, toVersionID)
	if err != nil {
		return VersionDiff{}, fmt.Errorf("reading %s: %w", toVersionID, err)
	}

	beforeByID := make(map[string]BlockInfo, len(before))
	for _, b := range before {
		beforeByID[b.ID] = b
	}
	afterByID := make(map[string]BlockInfo, len(after))
	for _, b := range after {
		afterByID[b.ID] = b
	}

	var diff VersionDiff
	for _, b := range after {
		old, existed := beforeByID[b.ID]
		if !existed {
			diff.Added = append(diff.Added, b)
			continue
		}
		if old.Text != b.Text {
			diff.Changed = append(diff.Changed, BlockPair{ID: b.ID, Before: old.Text, After: b.Text})
		}
	}
	for _, b := range before {
		if _, stillThere := afterByID[b.ID]; !stillThere {
			diff.Removed = append(diff.Removed, b)
		}
	}
	return diff, nil
}

// DateObject returns Anytype's object for one calendar day, creating the
// reference if needed. Linking notes to it is how daily-note style journals are
// built.
func (c *Client) DateObject(ctx context.Context, spaceID string, timestamp int64) (string, string, error) {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectDateByTimestamp(callCtx, &pb.RpcObjectDateByTimestampRequest{
		SpaceId: spaceID, Timestamp: timestamp,
	})
	if err != nil {
		return "", "", fmt.Errorf("gRPC ObjectDateByTimestamp failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectDateByTimestampResponseError_NULL {
		return "", "", fmt.Errorf("ObjectDateByTimestamp error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	details := resp.GetDetails()
	var id, name string
	if details != nil {
		if v, ok := details.Fields["id"]; ok {
			id, _ = fromProtoValue(v).(string)
		}
		if v, ok := details.Fields["name"]; ok {
			name, _ = fromProtoValue(v).(string)
		}
	}
	return id, name, nil
}

// SetTypeOrder fixes the order object types appear in.
func (c *Client) SetTypeOrder(ctx context.Context, spaceID string, typeIDs []string) error {
	if len(typeIDs) == 0 {
		return errors.New("type_ids is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectTypeSetOrder(callCtx, &pb.RpcObjectTypeSetOrderRequest{
		SpaceId: spaceID, TypeIds: typeIDs,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectTypeSetOrder failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectTypeSetOrderResponseError_NULL {
		return fmt.Errorf("ObjectTypeSetOrder error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// SetTagOrder fixes the order the options of one select/multi-select property
// appear in.
func (c *Client) SetTagOrder(ctx context.Context, spaceID, propertyKey string, tagIDs []string) error {
	if strings.TrimSpace(propertyKey) == "" {
		return errors.New("property_key is required")
	}
	if len(tagIDs) == 0 {
		return errors.New("tag_ids is required")
	}
	// RelationOptionSetOrder looks the property up by its internal key; the REST
	// spelling orders the options of nothing at all.
	propertyKey, err := c.resolvePropertyKey(ctx, spaceID, propertyKey)
	if err != nil {
		return err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.RelationOptionSetOrder(callCtx, &pb.RpcRelationOptionSetOrderRequest{
		SpaceId: spaceID, RelationKey: propertyKey, RelationOptionOrder: tagIDs,
	})
	if err != nil {
		return fmt.Errorf("gRPC RelationOptionSetOrder failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcRelationOptionSetOrderResponseError_NULL {
		return fmt.Errorf("RelationOptionSetOrder error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// CopyBlocksAsMarkdown renders selected blocks the way copying them in the GUI
// would, returning markdown and HTML.
//
// BlockCopy wants whole block models rather than ids, so the object is read
// first and the requested blocks are handed back verbatim.
func (c *Client) CopyBlocksAsMarkdown(ctx context.Context, spaceID, objectID string, blockIDs []string) (string, string, error) {
	if len(blockIDs) == 0 {
		return "", "", errors.New("block_ids is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	shown, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	cancel()
	if err != nil {
		return "", "", fmt.Errorf("gRPC ObjectShow failed: %w", err)
	}
	if shown.Error != nil && shown.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return "", "", fmt.Errorf("ObjectShow error (%s): %s", shown.Error.Code, shown.Error.Description)
	}

	wanted := make(map[string]bool, len(blockIDs))
	for _, id := range blockIDs {
		wanted[id] = true
	}
	var blocks []*model.Block
	for _, b := range shown.GetObjectView().GetBlocks() {
		if wanted[b.Id] {
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		return "", "", fmt.Errorf("none of the given block ids exist in %s", objectID)
	}

	callCtx, cancel = c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockCopy(callCtx, &pb.RpcBlockCopyRequest{
		ContextId: objectID, Blocks: blocks,
	})
	if err != nil {
		return "", "", fmt.Errorf("gRPC BlockCopy failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockCopyResponseError_NULL {
		return "", "", fmt.Errorf("BlockCopy error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.TextSlot, resp.HtmlSlot, nil
}

// ParseDate turns a date string into a Unix timestamp for DateObject.
func ParseDate(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().Unix(), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.Unix(), nil
		}
	}
	return 0, fmt.Errorf("cannot read %q as a date; use YYYY-MM-DD or a full ISO 8601 timestamp", value)
}

// ObjectRelationListAvailable is deliberately not wrapped.
//
// It looks like the answer to "which properties could this object have", and
// the RPC and its response type exist, but the service behind it is empty:
// core/block/editor.go's ListAvailableRelations is a "TODO: not implemented"
// that returns nil, so the call always reports success with zero relations
// (confirmed live for both page and task objects).
//
// The question is answerable with what is already here: get-type-compact
// returns the properties an object's type defines, and list-properties returns
// everything the space knows.

// DeleteObjectsPermanently erases archived objects for good.
//
// Two properties of the underlying RPC shape how this must be used, both read
// out of anytype-heart v0.50.8:
//
//   - ObjectListDelete calls DeleteArchivedObjects, so it operates on the bin.
//     Callers are expected to have archived the objects first, which keeps
//     destruction a deliberate second step rather than something one call can do.
//   - DeleteArchivedObjects returns nil as soon as ANY id succeeded and merely
//     logs the rest. A nil error therefore does not mean every object is gone,
//     which is why the caller should verify afterwards.
//
// There is no undo. Nothing in Anytype brings these objects back.
func (c *Client) DeleteObjectsPermanently(ctx context.Context, objectIDs []string) error {
	if len(objectIDs) == 0 {
		return errors.New("object_ids is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectListDelete(callCtx, &pb.RpcObjectListDeleteRequest{
		ObjectIds: objectIDs,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectListDelete failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectListDeleteResponseError_NULL {
		return fmt.Errorf("ObjectListDelete error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}
