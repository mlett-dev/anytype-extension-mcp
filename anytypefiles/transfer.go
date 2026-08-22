package anytypefiles

// Export, import and version history.
//
// None of this is reachable through the public REST API. Export and import work
// on the anytype-heart server's own filesystem, so paths handed to them must be
// server paths — the tool layer maps them from the shared input and output
// roots the file tools already use.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
)

var exportFormats = map[string]model.ExportFormat{
	"markdown": model.Export_Markdown,
	"md":       model.Export_Markdown,
	"protobuf": model.Export_Protobuf,
	"pb":       model.Export_Protobuf,
	"json":     model.Export_JSON,
}

var importTypes = map[string]model.ImportType{
	"notion":   model.Import_Notion,
	"markdown": model.Import_Markdown,
	"md":       model.Import_Markdown,
	"pb":       model.Import_Pb,
	"protobuf": model.Import_Pb,
	"html":     model.Import_Html,
	"txt":      model.Import_Txt,
	"csv":      model.Import_Csv,
	"obsidian": model.Import_Obsidian,
}

// ExportFormatNames lists the accepted export formats, for tool schemas.
func ExportFormatNames() []string { return []string{"markdown", "protobuf", "json"} }

// ImportTypeNames lists the accepted import sources, for tool schemas.
func ImportTypeNames() []string {
	return []string{"markdown", "html", "txt", "csv", "protobuf", "obsidian", "notion"}
}

// ExportOptions controls a multi-object export.
type ExportOptions struct {
	Path             string
	ObjectIDs        []string
	Format           string
	Zip              bool
	IncludeNested    bool
	IncludeFiles     bool
	IncludeArchived  bool
	IncludeBacklinks bool
	MarkdownSchema   bool
}

// ImportOptions controls an import run.
type ImportOptions struct {
	Type                  string
	Paths                 []string
	NotionAPIKey          string
	UpdateExistingObjects bool
	NoCollection          bool
	CollectionTitle       string
	CsvUseFirstRowAsNames bool
	CsvDelimiter          string
	CsvTransposed         bool
}

// ImportResult reports what an import produced — as far as anytype-heart is
// willing to say, which in v0.50.8 is nothing at all: see ImportObjects.
type ImportResult struct {
	ObjectsCount int64  `json:"objects_count"`
	CollectionID string `json:"collection_id,omitempty"`
}

// ObjectVersion is one entry of an object's version history.
type ObjectVersion struct {
	ID         string `json:"id"`
	AuthorName string `json:"author_name,omitempty"`
	AuthorID   string `json:"author_id,omitempty"`
	Time       string `json:"time,omitempty"`
	Unix       int64  `json:"unix,omitempty"`
	GroupID    int64  `json:"group_id,omitempty"`
}

// ExportObject renders one object and returns its content directly, without
// touching the filesystem. Handy for reading a page as markdown in one call.
func (c *Client) ExportObject(ctx context.Context, spaceID, objectID, format string) (string, error) {
	if strings.TrimSpace(objectID) == "" {
		return "", errors.New("object_id is required")
	}
	exportFormat, err := lookupEnum(exportFormats, format, "export format", true)
	if err != nil {
		return "", err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectExport(callCtx, &pb.RpcObjectExportRequest{
		SpaceId: spaceID, ObjectId: objectID, Format: exportFormat,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC ObjectExport failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectExportResponseError_NULL {
		return "", fmt.Errorf("ObjectExport error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.Result, nil
}

// ExportObjects writes objects to a directory on the server and reports where
// they landed and how many succeeded.
func (c *Client) ExportObjects(ctx context.Context, spaceID string, opts ExportOptions) (string, int32, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return "", 0, errors.New("an output path is required")
	}
	exportFormat, err := lookupEnum(exportFormats, opts.Format, "export format", true)
	if err != nil {
		return "", 0, err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectListExport(callCtx, &pb.RpcObjectListExportRequest{
		SpaceId: spaceID, Path: opts.Path, ObjectIds: opts.ObjectIDs,
		Format: exportFormat, Zip: opts.Zip,
		IncludeNested: opts.IncludeNested, IncludeFiles: opts.IncludeFiles,
		IncludeArchived: opts.IncludeArchived, IncludeBacklinks: opts.IncludeBacklinks,
		MdIncludePropertiesAndSchema: opts.MarkdownSchema,
		NoProgress:                   true,
	})
	if err != nil {
		return "", 0, fmt.Errorf("gRPC ObjectListExport failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectListExportResponseError_NULL {
		return "", 0, fmt.Errorf("ObjectListExport error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.Path, resp.Succeed, nil
}

// ImportObjects brings external content into a space.
//
// The import is fire-and-forget. anytype-heart runs it asynchronously and its
// middleware throws the result away — ObjectImport literally returns an empty
// response, so ObjectsCount, CollectionId and even the error field are always
// zero regardless of what happened (verified in core/object.go of v0.50.8, and
// live: a markdown import that created two pages still reported zero).
//
// A nil error therefore means "the import was accepted", never "the import
// succeeded". Callers must verify by looking for the objects afterwards, and
// should allow a moment for them to appear.
func (c *Client) ImportObjects(ctx context.Context, spaceID string, opts ImportOptions) (ImportResult, error) {
	importType, err := lookupEnum(importTypes, opts.Type, "import type", false)
	if err != nil {
		return ImportResult{}, err
	}

	req := &pb.RpcObjectImportRequest{
		SpaceId:               spaceID,
		Type:                  importType,
		UpdateExistingObjects: opts.UpdateExistingObjects,
		Mode:                  pb.RpcObjectImportRequest_IGNORE_ERRORS,
		NoProgress:            true,
	}

	// Notion pulls from its API and takes no paths; every other source reads
	// files, so a missing path there is a caller error rather than an empty run.
	if importType == model.Import_Notion {
		if strings.TrimSpace(opts.NotionAPIKey) == "" {
			return ImportResult{}, errors.New("notion_api_key is required for a Notion import")
		}
		req.Params = &pb.RpcObjectImportRequestParamsOfNotionParams{
			NotionParams: &pb.RpcObjectImportRequestNotionParams{ApiKey: opts.NotionAPIKey},
		}
	} else {
		if len(opts.Paths) == 0 {
			return ImportResult{}, errors.New("at least one path is required")
		}
		switch importType {
		case model.Import_Markdown, model.Import_Obsidian:
			req.Params = &pb.RpcObjectImportRequestParamsOfMarkdownParams{
				MarkdownParams: &pb.RpcObjectImportRequestMarkdownParams{
					Path: opts.Paths, NoCollection: opts.NoCollection,
				},
			}
		case model.Import_Html:
			req.Params = &pb.RpcObjectImportRequestParamsOfHtmlParams{
				HtmlParams: &pb.RpcObjectImportRequestHtmlParams{Path: opts.Paths},
			}
		case model.Import_Txt:
			req.Params = &pb.RpcObjectImportRequestParamsOfTxtParams{
				TxtParams: &pb.RpcObjectImportRequestTxtParams{Path: opts.Paths},
			}
		case model.Import_Csv:
			delimiter := opts.CsvDelimiter
			if delimiter == "" {
				delimiter = ","
			}
			req.Params = &pb.RpcObjectImportRequestParamsOfCsvParams{
				CsvParams: &pb.RpcObjectImportRequestCsvParams{
					Path: opts.Paths, Delimiter: delimiter,
					UseFirstRowForRelations: opts.CsvUseFirstRowAsNames,
					TransposeRowsAndColumns: opts.CsvTransposed,
				},
			}
		case model.Import_Pb:
			req.Params = &pb.RpcObjectImportRequestParamsOfPbParams{
				PbParams: &pb.RpcObjectImportRequestPbParams{
					Path: opts.Paths, NoCollection: opts.NoCollection,
					CollectionTitle: opts.CollectionTitle,
				},
			}
		default:
			return ImportResult{}, fmt.Errorf("unsupported import type %q", opts.Type)
		}
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectImport(callCtx, req)
	if err != nil {
		return ImportResult{}, fmt.Errorf("gRPC ObjectImport failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectImportResponseError_NULL {
		return ImportResult{}, fmt.Errorf("ObjectImport error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return ImportResult{ObjectsCount: resp.ObjectsCount, CollectionID: resp.CollectionId}, nil
}

// ListVersions returns an object's edit history, newest first.
func (c *Client) ListVersions(ctx context.Context, objectID, beforeVersionID string, limit int32) ([]ObjectVersion, error) {
	if strings.TrimSpace(objectID) == "" {
		return nil, errors.New("object_id is required")
	}
	if limit <= 0 {
		limit = 30
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.HistoryGetVersions(callCtx, &pb.RpcHistoryGetVersionsRequest{
		ObjectId: objectID, LastVersionId: beforeVersionID, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC HistoryGetVersions failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcHistoryGetVersionsResponseError_NULL {
		return nil, fmt.Errorf("HistoryGetVersions error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	out := make([]ObjectVersion, 0, len(resp.Versions))
	for _, v := range resp.Versions {
		entry := ObjectVersion{
			ID: v.Id, AuthorName: v.AuthorName, AuthorID: v.AuthorId,
			Unix: v.Time, GroupID: v.GroupId,
		}
		if v.Time > 0 {
			entry.Time = time.Unix(v.Time, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out, nil
}

// ShowVersion reads the blocks of an object as they were in one version,
// without changing anything.
func (c *Client) ShowVersion(ctx context.Context, objectID, versionID string) ([]BlockInfo, error) {
	if strings.TrimSpace(versionID) == "" {
		return nil, errors.New("version_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.HistoryShowVersion(callCtx, &pb.RpcHistoryShowVersionRequest{
		ObjectId: objectID, VersionId: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC HistoryShowVersion failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcHistoryShowVersionResponseError_NULL {
		return nil, fmt.Errorf("HistoryShowVersion error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	blocks := make([]BlockInfo, 0, len(resp.GetObjectView().GetBlocks()))
	for _, b := range resp.GetObjectView().GetBlocks() {
		blocks = append(blocks, blockFromModel(b))
	}
	return blocks, nil
}

// RestoreVersion rolls an object back to an earlier version.
func (c *Client) RestoreVersion(ctx context.Context, objectID, versionID string) error {
	if strings.TrimSpace(versionID) == "" {
		return errors.New("version_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.HistorySetVersion(callCtx, &pb.RpcHistorySetVersionRequest{
		ObjectId: objectID, VersionId: versionID,
	})
	if err != nil {
		return fmt.Errorf("gRPC HistorySetVersion failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcHistorySetVersionResponseError_NULL {
		return fmt.Errorf("HistorySetVersion error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// DuplicateObject copies an object and returns the new id.
func (c *Client) DuplicateObject(ctx context.Context, objectID string) (string, error) {
	if strings.TrimSpace(objectID) == "" {
		return "", errors.New("object_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectDuplicate(callCtx, &pb.RpcObjectDuplicateRequest{ContextId: objectID})
	if err != nil {
		return "", fmt.Errorf("gRPC ObjectDuplicate failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectDuplicateResponseError_NULL {
		return "", fmt.Errorf("ObjectDuplicate error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.Id, nil
}

// CreateTemplateFromObject turns an existing object into a template for its
// type and returns the new template's id.
func (c *Client) CreateTemplateFromObject(ctx context.Context, objectID string) (string, error) {
	if strings.TrimSpace(objectID) == "" {
		return "", errors.New("object_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.TemplateCreateFromObject(callCtx, &pb.RpcTemplateCreateFromObjectRequest{
		ContextId: objectID,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC TemplateCreateFromObject failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcTemplateCreateFromObjectResponseError_NULL {
		return "", fmt.Errorf("TemplateCreateFromObject error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.Id, nil
}

// CloneTemplate copies an existing template.
func (c *Client) CloneTemplate(ctx context.Context, spaceID, templateID string) (string, error) {
	if strings.TrimSpace(templateID) == "" {
		return "", errors.New("template_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.TemplateClone(callCtx, &pb.RpcTemplateCloneRequest{
		ContextId: templateID, SpaceId: spaceID,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC TemplateClone failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcTemplateCloneResponseError_NULL {
		return "", fmt.Errorf("TemplateClone error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.Id, nil
}

// SetTypeFeaturedProperties sets which properties appear directly under the
// title of every object of a type — the "featured relations" row.
//
// This is deliberately type-level. The per-object RPC pair
// ObjectRelationAddFeatured/RemoveFeatured exists but refuses everything except
// the description property ("only description relation is supported",
// anytype-heart v0.50.8), so it cannot express what the GUI actually does.
//
// The ids here are property OBJECT ids (bafyrei...) from list-properties, not
// property keys.
func (c *Client) SetTypeFeaturedProperties(ctx context.Context, typeID string, propertyIDs []string) error {
	if strings.TrimSpace(typeID) == "" {
		return errors.New("type_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectTypeRecommendedFeaturedRelationsSet(callCtx,
		&pb.RpcObjectTypeRecommendedFeaturedRelationsSetRequest{
			TypeObjectId: typeID, RelationObjectIds: propertyIDs,
		})
	if err != nil {
		return fmt.Errorf("gRPC ObjectTypeRecommendedFeaturedRelationsSet failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectTypeRecommendedFeaturedRelationsSetResponseError_NULL {
		return fmt.Errorf("ObjectTypeRecommendedFeaturedRelationsSet error (%s): %s",
			resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// DetailOperation is one add/remove/set on a property of many objects.
type DetailOperation struct {
	PropertyKey string `json:"property_key"`
	Add         any    `json:"add,omitempty"`
	Remove      any    `json:"remove,omitempty"`
	Set         any    `json:"set,omitempty"`
	HasAdd      bool   `json:"-"`
	HasRemove   bool   `json:"-"`
	HasSet      bool   `json:"-"`
}

// ModifyDetailValues adds to, removes from or overwrites a property across many
// objects at once.
//
// The point of add/remove is that they do not need a read first: adding one tag
// to fifty objects would otherwise mean fifty reads, fifty merges and fifty
// writes, with every one of them able to clobber a concurrent change.
func (c *Client) ModifyDetailValues(ctx context.Context, spaceID string, objectIDs []string, ops []DetailOperation) error {
	if len(objectIDs) == 0 {
		return errors.New("object_ids is required")
	}
	if len(ops) == 0 {
		return errors.New("operations is required")
	}
	// The RPC writes details straight into the object and heart validates the
	// key against the space index, which knows only the internal spelling. A
	// REST key reaches it as a relation that does not exist, and the whole call
	// fails with "object not found in space index" — for a set as much as for
	// an add, so the tool would be unusable for every property whose two
	// spellings differ.
	keys, err := c.loadRelationKeys(ctx, spaceID)
	if err != nil {
		return err
	}
	modelOps := make([]*pb.RpcObjectListModifyDetailValuesRequestOperation, 0, len(ops))
	for i, op := range ops {
		if strings.TrimSpace(op.PropertyKey) == "" {
			return fmt.Errorf("operations[%d]: property_key is required", i)
		}
		// At least one, deliberately not exactly one. heart
		// (core/block/detailservice/service.go, ModifyDetailsList) gives every
		// combination a defined meaning: a non-nil Set wins outright and skips
		// Add/Remove, otherwise Add is applied and then Remove. add+remove in
		// one operation is therefore a working tag swap, not a mistake, and
		// rejecting it here would remove a feature rather than prevent a bug.
		if !op.HasAdd && !op.HasRemove && !op.HasSet {
			return fmt.Errorf("operations[%d]: pass add, remove or set", i)
		}
		relationKey, err := keys.resolve(op.PropertyKey)
		if err != nil {
			return fmt.Errorf("operations[%d]: %w", i, err)
		}
		entry := &pb.RpcObjectListModifyDetailValuesRequestOperation{RelationKey: relationKey}
		if op.HasAdd {
			entry.Add = toProtoValue(op.Add)
		}
		if op.HasRemove {
			entry.Remove = toProtoValue(op.Remove)
		}
		if op.HasSet {
			entry.Set = toProtoValue(op.Set)
		}
		modelOps = append(modelOps, entry)
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectListModifyDetailValues(callCtx, &pb.RpcObjectListModifyDetailValuesRequest{
		ObjectIds: objectIDs, Operations: modelOps,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectListModifyDetailValues failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectListModifyDetailValuesResponseError_NULL {
		return fmt.Errorf("ObjectListModifyDetailValues error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}
