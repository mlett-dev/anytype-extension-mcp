package anytypefiles

// The remaining GUI actions: relation blocks, applying a template, extracting
// blocks into their own object, creating objects from a URL, link appearance,
// embed/LaTeX blocks, the graph and Unsplash covers.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anyproto/anytype-heart/pb"
	"github.com/anyproto/anytype-heart/pkg/lib/pb/model"
	"github.com/gogo/protobuf/types"
)

// typeUniqueKeyPrefix turns a type key into the unique key several RPCs want.
// They take "ot-page", not "page", and reject the bare key.
const typeUniqueKeyPrefix = "ot-"

var linkCardStyles = map[string]model.BlockContentLinkCardStyle{
	"text":   model.BlockContentLink_Text,
	"card":   model.BlockContentLink_Card,
	"inline": model.BlockContentLink_Inline,
}

var linkIconSizes = map[string]model.BlockContentLinkIconSize{
	"none":   model.BlockContentLink_SizeNone,
	"small":  model.BlockContentLink_SizeSmall,
	"medium": model.BlockContentLink_SizeMedium,
}

var linkDescriptions = map[string]model.BlockContentLinkDescription{
	"none":    model.BlockContentLink_None,
	"added":   model.BlockContentLink_Added,
	"content": model.BlockContentLink_Content,
}

var embedProcessors = map[string]model.BlockContentLatexProcessor{
	"latex":         model.BlockContentLatex_Latex,
	"mermaid":       model.BlockContentLatex_Mermaid,
	"chart":         model.BlockContentLatex_Chart,
	"youtube":       model.BlockContentLatex_Youtube,
	"vimeo":         model.BlockContentLatex_Vimeo,
	"soundcloud":    model.BlockContentLatex_Soundcloud,
	"googlemaps":    model.BlockContentLatex_GoogleMaps,
	"miro":          model.BlockContentLatex_Miro,
	"figma":         model.BlockContentLatex_Figma,
	"twitter":       model.BlockContentLatex_Twitter,
	"openstreetmap": model.BlockContentLatex_OpenStreetMap,
	"reddit":        model.BlockContentLatex_Reddit,
}

// LinkCardStyleNames lists link appearance styles, for tool schemas.
func LinkCardStyleNames() []string { return []string{"text", "card", "inline"} }

// LinkIconSizeNames lists link icon sizes, for tool schemas.
func LinkIconSizeNames() []string { return []string{"none", "small", "medium"} }

// LinkDescriptionNames lists link description modes, for tool schemas.
func LinkDescriptionNames() []string { return []string{"none", "added", "content"} }

// EmbedProcessorNames lists embed kinds, for tool schemas.
func EmbedProcessorNames() []string {
	return []string{"latex", "mermaid", "chart", "youtube", "vimeo", "soundcloud",
		"googlemaps", "miro", "figma", "twitter", "openstreetmap", "reddit"}
}

// GraphNode is one object in the graph.
type GraphNode struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
	Layout string `json:"layout,omitempty"`
}

// GraphEdge is one link between two objects.
type GraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
}

// CreateRelationBlock inserts a block that displays one property inline in the
// page body.
//
// It goes through BlockCreate rather than BlockRelationAdd: that RPC only
// re-keys a block that is already a relation block and errors with
// "unexpected block type" on anything else, so it cannot create one.
func (c *Client) CreateRelationBlock(ctx context.Context, spaceID, objectID, targetID, position, propertyKey string) (string, error) {
	if strings.TrimSpace(propertyKey) == "" {
		return "", errors.New("property_key is required")
	}
	// A relation block holds the property's internal key. Given the REST
	// spelling it renders as an empty row for a property that does not exist,
	// without any error to show for it.
	propertyKey, err := c.resolvePropertyKey(ctx, spaceID, propertyKey)
	if err != nil {
		return "", err
	}
	pos, err2 := lookupEnum(blockPositions, position, "position", true)
	if err2 != nil {
		return "", err2
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockCreate(callCtx, &pb.RpcBlockCreateRequest{
		ContextId: objectID, TargetId: targetID, Position: pos,
		Block: &model.Block{Content: &model.BlockContentOfRelation{
			Relation: &model.BlockContentRelation{Key: propertyKey},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("gRPC BlockCreate(relation) failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockCreateResponseError_NULL {
		return "", fmt.Errorf("BlockCreate(relation) error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

// SetRelationBlockKey points an existing relation block at another property.
//
// No tool calls this today. Whoever wires one up has to resolve the property key
// first, the way CreateRelationBlock does — the block stores the internal
// spelling, and the REST one leaves it pointing at nothing. The space id needed
// for that is not a parameter here yet.
func (c *Client) SetRelationBlockKey(ctx context.Context, objectID, blockID, propertyKey string) error {
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockRelationSetKey(callCtx, &pb.RpcBlockRelationSetKeyRequest{
		ContextId: objectID, BlockId: blockID, Key: propertyKey,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockRelationSetKey failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockRelationSetKeyResponseError_NULL {
		return fmt.Errorf("BlockRelationSetKey error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// ApplyTemplate applies a template to an object that already exists.
func (c *Client) ApplyTemplate(ctx context.Context, objectID, templateID string) error {
	if strings.TrimSpace(templateID) == "" {
		return errors.New("template_id is required")
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectApplyTemplate(callCtx, &pb.RpcObjectApplyTemplateRequest{
		ContextId: objectID, TemplateId: templateID,
	})
	if err != nil {
		return fmt.Errorf("gRPC ObjectApplyTemplate failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectApplyTemplateResponseError_NULL {
		return fmt.Errorf("ObjectApplyTemplate error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

// ExtractBlocksToObject moves blocks into a new object of their own and leaves
// links to it behind. Returns the ids of those link blocks.
func (c *Client) ExtractBlocksToObject(ctx context.Context, objectID string, blockIDs []string, typeKey, templateID string) ([]string, error) {
	if len(blockIDs) == 0 {
		return nil, errors.New("block_ids is required")
	}
	if strings.TrimSpace(typeKey) == "" {
		typeKey = "page"
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockListConvertToObjects(callCtx, &pb.RpcBlockListConvertToObjectsRequest{
		ContextId: objectID, BlockIds: blockIDs,
		ObjectTypeUniqueKey: typeUniqueKeyPrefix + typeKey,
		TemplateId:          templateID,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC BlockListConvertToObjects failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockListConvertToObjectsResponseError_NULL {
		return nil, fmt.Errorf("BlockListConvertToObjects error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.LinkIds, nil
}

// CreateObjectFromURL creates an object seeded from a web page. With
// typeKey "bookmark" this is the GUI's "create bookmark"; with "page" and
// withContent it pulls the article text into the body.
func (c *Client) CreateObjectFromURL(ctx context.Context, spaceID, url, typeKey string, withContent bool) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", errors.New("url is required")
	}
	if strings.TrimSpace(typeKey) == "" {
		typeKey = "bookmark"
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectCreateFromUrl(callCtx, &pb.RpcObjectCreateFromUrlRequest{
		SpaceId: spaceID, Url: url,
		ObjectTypeUniqueKey: typeUniqueKeyPrefix + typeKey,
		AddPageContent:      withContent,
	})
	if err != nil {
		return "", fmt.Errorf("gRPC ObjectCreateFromUrl failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectCreateFromUrlResponseError_NULL {
		return "", fmt.Errorf("ObjectCreateFromUrl error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.ObjectId, nil
}

// LinkAppearanceSpec is a partial change to how link blocks render. An empty
// enum field means "leave that setting alone"; RelationsSet tells an omitted
// property list from one the caller deliberately emptied.
type LinkAppearanceSpec struct {
	CardStyle    string
	IconSize     string
	Description  string
	Relations    []string
	RelationsSet bool
}

// Empty reports whether the spec asks for nothing.
func (s LinkAppearanceSpec) Empty() bool {
	return s.CardStyle == "" && s.IconSize == "" && s.Description == "" && !s.RelationsSet
}

// SetLinkAppearance changes how link blocks render.
//
// BlockLinkListSetAppearance carries all four settings and replaces all four:
// heart's link.SetAppearance assigns every one of them unconditionally. So the
// blocks are read first and only the supplied settings are overwritten —
// otherwise setting just the icon size would drop the card style back to text
// and clear the property list. One RPC per block, because the merged result
// differs per block while the request holds a single appearance for the whole
// list.
func (c *Client) SetLinkAppearance(ctx context.Context, spaceID, objectID string, blockIDs []string, spec LinkAppearanceSpec) error {
	if len(blockIDs) == 0 {
		return errors.New("block_ids is required")
	}
	// Validate every enum before the first write, so a typo cannot leave half
	// the blocks changed.
	style, err := lookupEnum(linkCardStyles, spec.CardStyle, "card style", true)
	if err != nil {
		return err
	}
	size, err := lookupEnum(linkIconSizes, spec.IconSize, "icon size", true)
	if err != nil {
		return err
	}
	desc, err := lookupEnum(linkDescriptions, spec.Description, "description mode", true)
	if err != nil {
		return err
	}

	// The appearance stores the property keys of the columns shown on the card,
	// and the block is read back by the index under the internal spelling.
	relations, err := c.resolvePropertyKeys(ctx, spaceID, "property_keys", spec.Relations)
	if err != nil {
		return err
	}
	spec.Relations = relations

	current, err := c.linkContents(ctx, spaceID, objectID, blockIDs)
	if err != nil {
		return err
	}
	for _, blockID := range blockIDs {
		link, ok := current[blockID]
		if !ok {
			return fmt.Errorf("block %s is not a link block on object %s", blockID, objectID)
		}
		content := &model.BlockContentLink{
			CardStyle:   link.CardStyle,
			IconSize:    link.IconSize,
			Description: link.Description,
			Relations:   link.Relations,
		}
		if spec.CardStyle != "" {
			content.CardStyle = style
		}
		if spec.IconSize != "" {
			content.IconSize = size
		}
		if spec.Description != "" {
			content.Description = desc
		}
		if spec.RelationsSet {
			content.Relations = spec.Relations
		}

		callCtx, cancel := c.contextWithAuth(ctx)
		resp, err := c.rpc.BlockLinkListSetAppearance(callCtx, &pb.RpcBlockLinkListSetAppearanceRequest{
			ContextId: objectID, BlockIds: []string{blockID},
			CardStyle: content.CardStyle, IconSize: content.IconSize,
			Description: content.Description, Relations: content.Relations,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("gRPC BlockLinkListSetAppearance failed: %w", err)
		}
		if resp.Error != nil && resp.Error.Code != pb.RpcBlockLinkListSetAppearanceResponseError_NULL {
			return fmt.Errorf("BlockLinkListSetAppearance error (%s): %s", resp.Error.Code, resp.Error.Description)
		}
	}
	return nil
}

// linkContents returns the stored link content of the given blocks, keyed by
// block id. Blocks that are not link blocks are absent from the result.
func (c *Client) linkContents(ctx context.Context, spaceID, objectID string, blockIDs []string) (map[string]*model.BlockContentLink, error) {
	wanted := make(map[string]bool, len(blockIDs))
	for _, id := range blockIDs {
		wanted[id] = true
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectShow(callCtx, &pb.RpcObjectShowRequest{
		SpaceId: spaceID, ObjectId: objectID, ContextId: objectID,
	})
	if err != nil {
		return nil, fmt.Errorf("gRPC ObjectShow failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectShowResponseError_NULL {
		return nil, fmt.Errorf("ObjectShow error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	out := make(map[string]*model.BlockContentLink, len(blockIDs))
	for _, block := range resp.GetObjectView().GetBlocks() {
		if !wanted[block.Id] {
			continue
		}
		if link := block.GetLink(); link != nil {
			out[block.Id] = link
		}
	}
	return out, nil
}

// There is no SetObjectLayout wrapper on purpose.
//
// ObjectSetLayout is not a stub — it is fully implemented — but it does not do
// what its name promises. Traced through anytype-heart v0.50.8:
//
//	SetLayoutInState reads the current layout via state.Layout(), which returns
//	the *resolved* layout: a derived local detail computed from the object's
//	TYPE (state.go, RelationKeyResolvedLayout). It then hands the difference to
//	layoutConverter.Convert, and that converter never writes a layout anywhere
//	— converter/layout.go contains no assignment to RelationKeyLayout or
//	RelationKeyResolvedLayout. All it does is reshape the object: add a title,
//	add the done relation for todo, add bookmark blocks, and so on. The
//	per-object layout relation is itself on the way out; details.go nulls it.
//
// So an object's layout follows its type and this RPC cannot change it. Two of
// the conversions a caller would most want are refused anyway: isConversionAllowed
// permits only page-like layouts among each other and set to collection, so
// basic to collection or set errors out. Live checks matched exactly that.
//
// The route that works is changing the TYPE — update-object-compact with
// type_key, which moves the resolved layout with it (verified: page to task
// moved the reported layout from basic to action).

// CreateEmbedBlock inserts a LaTeX formula, a Mermaid diagram or an embedded
// service. All of them share one block type in Anytype, told apart by the
// processor.
func (c *Client) CreateEmbedBlock(ctx context.Context, spaceID, objectID, targetID, position, processor, text string) (string, error) {
	proc, err := lookupEnum(embedProcessors, processor, "embed kind", true)
	if err != nil {
		return "", err
	}
	pos, err := lookupEnum(blockPositions, position, "position", true)
	if err != nil {
		return "", err
	}
	if pos == model.Block_None {
		pos = model.Block_Bottom
	}

	callCtx, cancel := c.contextWithAuth(ctx)
	resp, err := c.rpc.BlockCreate(callCtx, &pb.RpcBlockCreateRequest{
		ContextId: objectID, TargetId: targetID, Position: pos,
		Block: &model.Block{Content: &model.BlockContentOfLatex{
			Latex: &model.BlockContentLatex{Text: text, Processor: proc},
		}},
	})
	cancel()
	if err != nil {
		return "", fmt.Errorf("gRPC BlockCreate(embed) failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockCreateResponseError_NULL {
		return "", fmt.Errorf("BlockCreate(embed) error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return resp.BlockId, nil
}

// SetEmbedText replaces the formula or source of an embed block.
func (c *Client) SetEmbedText(ctx context.Context, objectID, blockID, processor, text string) error {
	proc, err := lookupEnum(embedProcessors, processor, "embed kind", true)
	if err != nil {
		return err
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.BlockLatexSetText(callCtx, &pb.RpcBlockLatexSetTextRequest{
		ContextId: objectID, BlockId: blockID, Text: text, Processor: proc,
	})
	if err != nil {
		return fmt.Errorf("gRPC BlockLatexSetText failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcBlockLatexSetTextResponseError_NULL {
		return fmt.Errorf("BlockLatexSetText error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	return nil
}

func structString(s *types.Struct, key string) string {
	if s == nil {
		return ""
	}
	if v, ok := s.Fields[key]; ok {
		if sv, ok := v.Kind.(*types.Value_StringValue); ok {
			return sv.StringValue
		}
	}
	return ""
}

// Graph returns the objects of a space and the links between them.
func (c *Client) Graph(ctx context.Context, spaceID string, typeFilter []string, limit int32) ([]GraphNode, []GraphEdge, error) {
	if limit <= 0 {
		limit = 200
	}
	callCtx, cancel := c.contextWithAuth(ctx)
	defer cancel()
	resp, err := c.rpc.ObjectGraph(callCtx, &pb.RpcObjectGraphRequest{
		SpaceId: spaceID, Limit: limit, ObjectTypeFilter: typeFilter,
		Keys: []string{"id", "name", "type", "layout"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("gRPC ObjectGraph failed: %w", err)
	}
	if resp.Error != nil && resp.Error.Code != pb.RpcObjectGraphResponseError_NULL {
		return nil, nil, fmt.Errorf("ObjectGraph error (%s): %s", resp.Error.Code, resp.Error.Description)
	}
	nodes := make([]GraphNode, 0, len(resp.Nodes))
	for _, n := range resp.Nodes {
		nodes = append(nodes, GraphNode{
			ID:     structString(n, "id"),
			Name:   structString(n, "name"),
			Type:   structString(n, "type"),
			Layout: structString(n, "layout"),
		})
	}
	edges := make([]GraphEdge, 0, len(resp.Edges))
	for _, e := range resp.Edges {
		edges = append(edges, GraphEdge{
			Source: e.Source, Target: e.Target, Name: e.Name,
			Type: strings.ToLower(e.Type.String()),
		})
	}
	return nodes, edges, nil
}

// Unsplash is deliberately not wrapped here. anytype-heart's own Unsplash
// client authenticates with "Authorization: Bearer", which Unsplash rejects for
// ordinary Access Keys (they require "Client-ID"), and the Bearer tokens that
// its client_credentials grant does issue expire after 30 minutes while heart
// reads UNSPLASH_KEY once at package init. The MCP server therefore talks to
// api.unsplash.com directly — see tools/anytype-extension-mcp/unsplash.go.
