package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mlett-dev/anytype-extension-mcp/anytypefiles"
)

// The last of the GUI actions: relation blocks, applying a template, pulling
// blocks out into their own object, objects from a URL, link appearance,
// embeds, the graph and Unsplash covers.
//
// There is deliberately no object-set-layout tool — see the note in
// internal/anytypefiles/extras.go for why that RPC cannot be trusted.

// chartTextNote covers the one embed kind whose text has no obvious form.
//
// anytype-heart does not interpret embed text at all — it stores the string on
// the block and the clients render it — so there is no server-side format to
// document and no validation to lean on. Saying that plainly beats inventing a
// syntax the renderer may not accept.
const chartTextNote = "kind=chart takes the chart definition the Anytype client renders; anytype-heart stores embed text verbatim and never validates it, so a wrong format fails silently at render time rather than here. If in doubt, build one chart in the app and read its text back with block-list before writing more."

func (s *mcpServer) extrasToolDefs() []any {
	return []any{
		map[string]any{
			"name":        "block-relation-add",
			"description": "Insert a block that shows one of the object's properties inline in the page body, the way the GUI's 'relation' block does. The value stays editable through the normal property tools; this only decides that it is displayed here. Pass a property key, not an id.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "property_key"}, map[string]any{
				"space_id":     spaceIDProp(),
				"object_id":    strProp("Object to insert into."),
				"property_key": strProp("Property key to display, e.g. \"due_date\". Keys come from list-properties or get-type-compact. " + relationKeySpellingNote),
				"target_id":    strProp("Block id to insert relative to. Omit to append at the end."),
				"position":     enumProp("Where to insert relative to target_id. Defaults to bottom.", anytypefiles.BlockPositionNames()),
			}),
		},
		map[string]any{
			"name":        "object-apply-template",
			"description": "Apply a template to an object that already exists, replacing its body with the template's. Creating a NEW object from a template does not need this — pass template_id to create-objects-compact-many instead.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "template_id"}, map[string]any{
				"space_id":    spaceIDProp(),
				"object_id":   strProp("Object to apply the template to."),
				"template_id": strProp("Template id, from list-templates or template-create."),
			}),
		},
		map[string]any{
			"name":        "block-extract-to-object",
			"description": "Move blocks out of a page into objects of their own, leaving links behind — the GUI's 'extract to a new page'. One new object is created per TOP-LEVEL block in the selection, and each takes its nested children with it: passing a heading and a separate paragraph yields two objects, while passing one heading that has the paragraph nested under it yields one. Returns the new object ids.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object the blocks currently live in."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Blocks to move into the new object.",
					"items":       map[string]any{"type": "string"},
				},
				"type_key":    strProp("Type key for the new object. Defaults to page."),
				"template_id": strProp("Template to base the new object on."),
			}),
		},
		map[string]any{
			"name":        "object-create-from-url",
			"description": "Create an object from a web address. With type_key=bookmark this is the GUI's bookmark: Anytype fetches title, description and preview image. With type_key=page and with_content=true it pulls the article text into the body. This creates an OBJECT; block-create with kind=bookmark puts a bookmark inside an existing page instead.",
			"inputSchema": restSchema([]string{"space_id", "url"}, map[string]any{
				"space_id":     spaceIDProp(),
				"url":          strProp("Address to fetch."),
				"type_key":     strProp("Type key for the new object. Defaults to bookmark."),
				"with_content": map[string]any{"type": "boolean", "description": "Pull the page's text into the object body. Only meaningful for document types.", "default": false},
			}),
		},
		map[string]any{
			"name":        "block-link-appearance",
			"description": "Change how link blocks are rendered: as a plain text link, as a card with icon and description, or inline. Applies to blocks created with block-create kind=link. Only the settings you pass are written; the ones you omit keep their stored value, which block-list reports for every link block.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_ids"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the link blocks."),
				"block_ids": map[string]any{
					"type":        "array",
					"description": "Link block ids from block-list.",
					"items":       map[string]any{"type": "string"},
				},
				"card_style":  enumProp("How the link is drawn. Omit to keep the current style.", anytypefiles.LinkCardStyleNames()),
				"icon_size":   enumProp("Icon size. Omit to keep the current size.", anytypefiles.LinkIconSizeNames()),
				"description": enumProp("Which description to show: none, the object's description (added), or a snippet of its content. Omit to keep the current mode.", anytypefiles.LinkDescriptionNames()),
				"property_keys": map[string]any{
					"type":        "array",
					"description": "Properties to show on the card, e.g. [\"type\"]. Replaces the current list; omit the field to leave it alone, pass an empty array to clear it. " + relationKeySpellingNote,
					"items":       map[string]any{"type": "string"},
				},
			}),
		},
		map[string]any{
			"name":        "block-embed-create",
			"description": "Insert a LaTeX formula, a Mermaid diagram, a chart, or an embedded service (YouTube, Figma, Miro, Google Maps and friends). Anytype stores all of these as one block type, told apart by kind. text is the formula, the diagram source, the chart definition, or the URL, depending on kind.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "kind", "text"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object to insert into."),
				"kind":      enumProp("What to embed.", anytypefiles.EmbedProcessorNames()),
				"text":      strProp("LaTeX source for kind=latex, Mermaid source for kind=mermaid, the page URL for a service embed. " + chartTextNote),
				"target_id": strProp("Block id to insert relative to. Omit to append at the end."),
				"position":  enumProp("Where to insert relative to target_id. Defaults to bottom.", anytypefiles.BlockPositionNames()),
			}),
		},
		map[string]any{
			"name":        "block-embed-set-text",
			"description": "Replace the source of an existing embed block of any kind — the LaTeX formula, the Mermaid diagram, the chart definition or the URL of a service embed.",
			"inputSchema": restSchema([]string{"space_id", "object_id", "block_id", "text"}, map[string]any{
				"space_id":  spaceIDProp(),
				"object_id": strProp("Object containing the block."),
				"block_id":  strProp("Embed block id from block-list. Every embed kind lives in the same block type, so this is the id of a latex, mermaid, chart, youtube, figma, miro or any other embed block."),
				"text":      strProp("New source, in whatever form the block's kind takes. " + chartTextNote),
				"kind":      enumProp("Embed kind. Pass the block's existing kind unless you mean to change it.", anytypefiles.EmbedProcessorNames()),
			}),
		},
		map[string]any{
			"name":        "object-graph",
			"description": "Read the space as a graph: objects as nodes and the links between them as edges. Use it to see how things are connected — which notes are orphaned, what a topic links to — rather than listing objects one by one.",
			"inputSchema": restSchema([]string{"space_id"}, map[string]any{
				"space_id": spaceIDProp(),
				"type_ids": map[string]any{
					"type":        "array",
					"description": "Restrict to these type ids.",
					"items":       map[string]any{"type": "string"},
				},
				"limit": map[string]any{"type": "integer", "description": "Maximum number of nodes. Defaults to 200.", "default": 200},
			}),
		},
		map[string]any{
			"name":        "unsplash-search",
			"description": "Search Unsplash for a cover image. Returns picture ids for unsplash-download, plus the photographer's name and profile link — Unsplash's terms require crediting them wherever the image is shown.",
			"inputSchema": restSchema([]string{"query"}, map[string]any{
				"query": strProp("What to look for, e.g. \"mountains\"."),
				"limit": map[string]any{"type": "integer", "description": "How many results. Defaults to 10.", "default": 10},
			}),
		},
		map[string]any{
			"name":        "unsplash-download",
			"description": "Download an Unsplash picture into the space and return the image object id. Hand that id to object-set-cover to use it as an object's cover — update-object-compact cannot do it, because the REST API has no cover field. Also reports Unsplash's attribution notice for the photographer, which their terms require you to show wherever the image appears.",
			"inputSchema": restSchema([]string{"space_id", "picture_id"}, map[string]any{
				"space_id":   spaceIDProp(),
				"picture_id": strProp("Picture id from unsplash-search."),
			}),
		},
	}
}

func (s *mcpServer) dispatchExtrasTool(name string, args map[string]any) (map[string]any, error, bool) {
	switch name {
	case "block-relation-add":
		res, err := s.toolBlockRelationAdd(args)
		return res, err, true
	case "object-apply-template":
		res, err := s.toolApplyTemplate(args)
		return res, err, true
	case "block-extract-to-object":
		res, err := s.toolExtractToObject(args)
		return res, err, true
	case "object-create-from-url":
		res, err := s.toolCreateFromURL(args)
		return res, err, true
	case "block-link-appearance":
		res, err := s.toolLinkAppearance(args)
		return res, err, true
	case "block-embed-create":
		res, err := s.toolEmbedCreate(args)
		return res, err, true
	case "block-embed-set-text":
		res, err := s.toolEmbedSetText(args)
		return res, err, true
	case "object-graph":
		res, err := s.toolObjectGraph(args)
		return res, err, true
	case "unsplash-search":
		res, err := s.toolUnsplashSearch(args)
		return res, err, true
	case "unsplash-download":
		res, err := s.toolUnsplashDownload(args)
		return res, err, true
	}
	return nil, nil, false
}

func (s *mcpServer) toolBlockRelationAdd(args map[string]any) (map[string]any, error) {
	propertyKey, err := requiredString(args, "property_key")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	blockID, err := client.CreateRelationBlock(context.Background(), spaceID, objectID,
		optionalString(args, "target_id"), optionalString(args, "position"), propertyKey)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": blockID, "property_key": propertyKey, "created": true,
	}, nil
}

func (s *mcpServer) toolApplyTemplate(args map[string]any) (map[string]any, error) {
	templateID, err := requiredString(args, "template_id")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.ApplyTemplate(context.Background(), objectID, templateID); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"template_id": templateID, "applied": true,
	}, nil
}

func (s *mcpServer) toolExtractToObject(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	linkIDs, err := client.ExtractBlocksToObject(context.Background(), objectID, blockIDs,
		optionalString(args, "type_key"), optionalString(args, "template_id"))
	if err != nil {
		return nil, err
	}

	// The RPC returns the ids of the LINK blocks it left behind, not of the
	// object it created — that has to be read off the links.
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"link_block_ids": linkIDs, "extracted": true,
	}
	blocks, _, err := client.ReadBlocks(context.Background(), spaceID, objectID)
	if err == nil {
		var created []string
		for _, b := range blocks {
			for _, id := range linkIDs {
				if b.ID == id && b.TargetObjectID != "" {
					created = append(created, b.TargetObjectID)
				}
			}
		}
		if len(created) > 0 {
			out["new_object_ids"] = created
		}
	}
	return out, nil
}

func (s *mcpServer) toolCreateFromURL(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	url, err := requiredString(args, "url")
	if err != nil {
		return nil, err
	}
	typeKey := optionalString(args, "type_key")
	if typeKey == "" {
		typeKey = "bookmark"
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	objectID, err := client.CreateObjectFromURL(context.Background(), spaceID, url,
		typeKey, optionalBool(args, "with_content", false))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"url": url, "type_key": typeKey, "created": true,
		"note": "the preview is fetched in the background; re-read the object if the title or description is still empty",
	}, nil
}

func (s *mcpServer) toolLinkAppearance(args map[string]any) (map[string]any, error) {
	blockIDs := optionalStringSlice(args, "block_ids")
	if len(blockIDs) == 0 {
		return nil, fmt.Errorf("block_ids is required")
	}
	spec := anytypefiles.LinkAppearanceSpec{
		CardStyle:   optionalString(args, "card_style"),
		IconSize:    optionalString(args, "icon_size"),
		Description: optionalString(args, "description"),
		Relations:   optionalStringSlice(args, "property_keys"),
	}
	// An omitted property list has to stay distinguishable from one the caller
	// emptied on purpose, the same way the view tools treat their lists.
	if raw, ok := args["property_keys"]; ok && raw != nil {
		spec.RelationsSet = true
	}
	if spec.Empty() {
		return nil, fmt.Errorf("block-link-appearance needs at least one of card_style, icon_size, description or property_keys")
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetLinkAppearance(context.Background(), spaceID, objectID, blockIDs, spec); err != nil {
		return nil, err
	}
	out := map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_ids": blockIDs, "updated": true,
		"note": "settings passed in this call were written, the omitted ones kept their stored value",
	}
	return out, nil
}

func (s *mcpServer) toolEmbedCreate(args map[string]any) (map[string]any, error) {
	kind, err := requiredString(args, "kind")
	if err != nil {
		return nil, err
	}
	text, err := requiredString(args, "text")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	blockID, err := client.CreateEmbedBlock(context.Background(), spaceID, objectID,
		optionalString(args, "target_id"), optionalString(args, "position"), kind, text)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": blockID, "kind": kind, "created": true,
	}, nil
}

func (s *mcpServer) toolEmbedSetText(args map[string]any) (map[string]any, error) {
	blockID, err := requiredString(args, "block_id")
	if err != nil {
		return nil, err
	}
	text, err := requiredString(args, "text")
	if err != nil {
		return nil, err
	}
	client, spaceID, objectID, err := s.blockTarget(args)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if err := client.SetEmbedText(context.Background(), objectID, blockID,
		optionalString(args, "kind"), text); err != nil {
		return nil, err
	}
	return map[string]any{
		"space_id": spaceID, "object_id": objectID,
		"block_id": blockID, "updated": true,
	}, nil
}

func (s *mcpServer) toolObjectGraph(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	nodes, edges, err := client.Graph(context.Background(), spaceID,
		optionalStringSlice(args, "type_ids"), int32(optionalInt(args, "limit", 200)))
	if err != nil {
		return nil, err
	}
	nodeOut := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		entry := map[string]any{"id": n.ID}
		if n.Name != "" {
			entry["name"] = n.Name
		}
		if n.Layout != "" {
			entry["layout"] = n.Layout
		}
		nodeOut = append(nodeOut, entry)
	}
	edgeOut := make([]map[string]any, 0, len(edges))
	for _, e := range edges {
		entry := map[string]any{"source": e.Source, "target": e.Target}
		if e.Name != "" {
			entry["name"] = e.Name
		}
		edgeOut = append(edgeOut, entry)
	}
	return map[string]any{
		"space_id": spaceID,
		"nodes":    nodeOut, "edges": edgeOut,
		"node_count": len(nodeOut), "edge_count": len(edgeOut),
	}, nil
}

func (s *mcpServer) toolUnsplashSearch(args map[string]any) (map[string]any, error) {
	query, err := requiredString(args, "query")
	if err != nil {
		return nil, err
	}
	photos, err := s.unsplashSearch(query, optionalInt(args, "limit", 10))
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(photos))
	for _, p := range photos {
		entry := map[string]any{"picture_id": p.ID, "url": p.ImageURL}
		if p.Description != "" {
			entry["description"] = p.Description
		}
		if p.Artist != "" {
			entry["artist"] = p.Artist
		}
		if p.ArtistURL != "" {
			entry["artist_url"] = p.ArtistURL
		}
		out = append(out, entry)
	}
	return map[string]any{
		"query": query, "pictures": out, "count": len(out),
	}, nil
}

func (s *mcpServer) toolUnsplashDownload(args map[string]any) (map[string]any, error) {
	spaceID, err := requiredString(args, "space_id")
	if err != nil {
		return nil, err
	}
	pictureID, err := requiredString(args, "picture_id")
	if err != nil {
		return nil, err
	}

	photo, err := s.unsplashPhoto(pictureID)
	if err != nil {
		return nil, err
	}
	// Required by Unsplash's terms before the image is put to use.
	trackErr := s.trackDownload(photo)

	hostPath, serverPath, err := s.fetchUnsplashImage(photo)
	if err != nil {
		return nil, err
	}
	// The staged copy is only a handover to the Anytype server; it must not be
	// left behind in the shared input directory.
	defer os.Remove(hostPath)

	client, err := s.grpcClient()
	if err != nil {
		return nil, err
	}
	defer client.Close()

	fileType, err := anytypefiles.ParseFileType("image")
	if err != nil {
		return nil, err
	}
	fileStyle, err := anytypefiles.ParseFileStyle("auto")
	if err != nil {
		return nil, err
	}
	uploaded, err := client.UploadFile(context.Background(), anytypefiles.UploadRequest{
		SpaceID:   spaceID,
		LocalPath: serverPath,
		Type:      fileType,
		Style:     fileStyle,
	})
	if err != nil {
		return nil, err
	}

	attribution := ""
	if photo.Artist != "" {
		attribution = fmt.Sprintf("Photo by %s on Unsplash", photo.Artist)
	}
	out := map[string]any{
		"space_id": spaceID, "picture_id": pictureID,
		"object_id": uploaded.ObjectID, "downloaded": true,
	}
	if attribution != "" {
		out["attribution"] = attribution
		out["artist"] = photo.Artist
		out["artist_url"] = photo.ArtistURL
	}
	if trackErr != nil {
		// Worth surfacing: repeated failures here can cost the API key.
		out["download_tracking_warning"] = trackErr.Error()
	}
	return out, nil
}
