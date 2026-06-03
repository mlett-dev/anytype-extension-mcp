package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	serverName    = "anytype-extension-mcp"
	serverVersion = "0.4.0"
	protoVersion  = "2025-03-26"
)

const propertyLinkValueDescription = "Property updates must use the typed Anytype property-link shape. Every item needs key plus exactly one typed value field: text, number, select, multi_select, date, files, checkbox, url, email, phone, or objects. Do not send {key, value:null}; Anytype cannot infer the value type from null. Use objects for relation/object links, files for file attachments, [] to clear object/file/multi-select links, and an empty string/false only for formats that accept that type."

const compactOutputOptionsDescription = "Compact output options are response-shaping only. fields selects top-level object fields such as id, name, snippet, layout, archived, space_id, object, type, icon, properties, or markdown. property_keys selects entries inside the returned properties object. Do not put property names in fields, and do not put top-level fields in property_keys."

const bulkCompactOptionsDescription = "For many tools, compact output options such as fields, property_keys, include_properties, include_type, include_icon, and max_properties belong at the top level of the tool call and apply to every returned object. Do not put these compact options inside items."

const queryFiltersDescription = "Additional URL query parameters passed directly to the Anytype REST endpoint. This is not the same as Anytype list-view filter definitions. Use only known API query keys, for example {\"done\":\"false\"} or {\"created_date[gte]\":\"2024-01-01\"}."

type rpcRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type serverConfig struct {
	grpcAddr      string
	token         string
	timeout       time.Duration
	apiBaseURL    string
	apiKey        string
	apiVersion    string
	inRoot        string
	outRoot       string
	serverInRoot  string
	serverOutRoot string
}

type messageFraming int

const (
	messageFramingContentLength messageFraming = iota
	messageFramingLine
)

type mcpServer struct {
	cfg serverConfig
}

func main() {
	cfg := serverConfig{
		grpcAddr:      getEnv("ANYTYPE_GRPC_ADDR", DefaultGRPCAddress),
		token:         strings.TrimSpace(os.Getenv("ANYTYPE_SESSION_TOKEN")),
		timeout:       parseDurationEnv("ANYTYPE_TIMEOUT", DefaultTimeout),
		apiBaseURL:    getEnv("ANYTYPE_API_BASE_URL", "http://127.0.0.1:31012"),
		apiKey:        strings.TrimSpace(os.Getenv("ANYTYPE_API_KEY")),
		apiVersion:    getEnv("ANYTYPE_API_VERSION", "2025-11-08"),
		inRoot:        getEnv("ANYTYPE_FILES_IN_ROOT", "/data/in"),
		outRoot:       getEnv("ANYTYPE_FILES_OUT_ROOT", "/data/out"),
		serverInRoot:  getEnv("ANYTYPE_FILES_SERVER_IN_ROOT", "/data/in"),
		serverOutRoot: getEnv("ANYTYPE_FILES_SERVER_OUT_ROOT", "/data/out"),
	}

	srv := &mcpServer{cfg: cfg}
	if err := srv.run(os.Stdin, os.Stdout); err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serverName, err)
		os.Exit(1)
	}
}

func (s *mcpServer) run(stdin io.Reader, stdout io.Writer) error {
	reader := bufio.NewReader(stdin)
	writer := bufio.NewWriter(stdout)

	for {
		body, framing, err := readMessage(reader)
		if err != nil {
			return err
		}

		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			if err := writeResponse(writer, framing, rpcResponse{
				JSONRPC: "2.0",
				Error: &rpcError{
					Code:    -32700,
					Message: "parse error: invalid JSON",
				},
			}); err != nil {
				return err
			}
			continue
		}

		resp, send := s.handleRequest(req)
		if !send {
			continue
		}
		if err := writeResponse(writer, framing, resp); err != nil {
			return err
		}
	}
}

func (s *mcpServer) handleRequest(req rpcRequest) (rpcResponse, bool) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return s.errorResponse(req.ID, -32600, "invalid request: unsupported jsonrpc version"), req.ID != nil
	}

	switch req.Method {
	case "initialize":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": protoVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    serverName,
					"version": serverVersion,
				},
			},
		}, req.ID != nil
	case "notifications/initialized":
		return rpcResponse{}, false
	case "ping":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}, req.ID != nil
	case "tools/list":
		inputRootDesc := fmt.Sprintf("List files staged under the configured input root %q. Use this before upload. Reuse returned relative_path values verbatim for file-upload or file-upload-many, especially for spaces, umlauts, and special characters.", s.cfg.inRoot)
		outputRootDesc := fmt.Sprintf("List files under the configured output root %q after downloads. This tool only lists local output files; it does not download from Anytype.", s.cfg.outRoot)
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": []any{
					map[string]any{
						"name":        "file-info",
						"description": fmt.Sprintf("Show the currently configured roots and exact usage hints. input_root=%q, output_root=%q, server_input_root=%q, server_output_root=%q.", s.cfg.inRoot, s.cfg.outRoot, s.cfg.serverInRoot, s.cfg.serverOutRoot),
						"inputSchema": map[string]any{
							"type":                 "object",
							"properties":           map[string]any{},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "file-list-input",
						"description": inputRootDesc,
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"recursive": map[string]any{
									"type":        "boolean",
									"description": "Whether to recurse into subdirectories.",
								},
								"include_dirs": map[string]any{
									"type":        "boolean",
									"description": "Whether to include directories in the result.",
								},
								"limit": map[string]any{
									"type":        "integer",
									"description": "Maximum number of entries to return.",
								},
							},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "file-list-output",
						"description": outputRootDesc,
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"recursive": map[string]any{
									"type":        "boolean",
									"description": "Whether to recurse into subdirectories.",
								},
								"include_dirs": map[string]any{
									"type":        "boolean",
									"description": "Whether to include directories in the result.",
								},
								"limit": map[string]any{
									"type":        "integer",
									"description": "Maximum number of entries to return.",
								},
							},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "file-upload",
						"description": fmt.Sprintf("Upload a staged local file from the configured input root to Anytype. input_root=%q, server_input_root=%q. staged_path may be absolute under input_root or a relative_path previously returned by file-list-input. IMPORTANT: use the exact filename/path as listed on disk. Spaces are allowed and must not be rewritten to underscores.", s.cfg.inRoot, s.cfg.serverInRoot),
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"space_id": map[string]any{
									"type":        "string",
									"description": "Target Anytype space ID.",
								},
								"staged_path": map[string]any{
									"type":        "string",
									"description": fmt.Sprintf("Absolute path under %q or a relative path beneath it. Prefer relative_path values returned by file-list-input and reuse them verbatim. Spaces are allowed and must not be rewritten.", s.cfg.inRoot),
								},
								"type": map[string]any{
									"type":        "string",
									"description": "File type hint.",
									"enum":        []string{"file", "image", "video", "audio", "none"},
								},
								"style": map[string]any{
									"type":        "string",
									"description": "Display style hint.",
									"enum":        []string{"auto", "link", "embed"},
								},
							},
							"required":             []string{"space_id", "staged_path"},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "file-download",
						"description": fmt.Sprintf("Download a file object from Anytype into the configured output root. output_root=%q, server_output_root=%q. If target_name is set, it must be a plain filename only (no path separators).", s.cfg.outRoot, s.cfg.serverOutRoot),
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"object_id": map[string]any{
									"type":        "string",
									"description": "Anytype file object ID.",
								},
								"target_name": map[string]any{
									"type":        "string",
									"description": fmt.Sprintf("Optional output filename under %q.", s.cfg.outRoot),
								},
							},
							"required":             []string{"object_id"},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "file-upload-many",
						"description": fmt.Sprintf("Upload multiple staged files in one call. input_root=%q, server_input_root=%q. Use relative_path values returned by file-list-input verbatim. Spaces are allowed and must not be rewritten to underscores. default_type/default_style are top-level defaults; per-item type/style override only that item.", s.cfg.inRoot, s.cfg.serverInRoot),
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"space_id": map[string]any{
									"type":        "string",
									"description": "Target Anytype space ID.",
								},
								"default_type": map[string]any{
									"type":        "string",
									"description": "Default file type hint.",
									"enum":        []string{"file", "image", "video", "audio", "none"},
								},
								"default_style": map[string]any{
									"type":        "string",
									"description": "Default display style hint.",
									"enum":        []string{"auto", "link", "embed"},
								},
								"stop_on_error": map[string]any{
									"type":        "boolean",
									"description": "Stop at first error instead of continuing.",
								},
								"items": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"staged_path": map[string]any{
												"type":        "string",
												"description": fmt.Sprintf("Absolute path under %q or a relative_path from file-list-input. Reuse the path verbatim; do not rename or normalize spaces, umlauts, or special characters.", s.cfg.inRoot),
											},
											"type": map[string]any{
												"type": "string",
												"enum": []string{"file", "image", "video", "audio", "none"},
											},
											"style": map[string]any{
												"type": "string",
												"enum": []string{"auto", "link", "embed"},
											},
										},
										"required":             []string{"staged_path"},
										"additionalProperties": false,
									},
								},
							},
							"required":             []string{"space_id", "items"},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "file-download-many",
						"description": fmt.Sprintf("Download multiple Anytype file objects into the configured output root %q. Each item needs an Anytype file object_id. target_name, when provided, must be a plain filename only, not a path.", s.cfg.outRoot),
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"stop_on_error": map[string]any{
									"type":        "boolean",
									"description": "Stop at first error instead of continuing.",
								},
								"items": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"object_id": map[string]any{
												"type":        "string",
												"description": "Anytype file object ID.",
											},
											"target_name": map[string]any{
												"type":        "string",
												"description": fmt.Sprintf("Optional output filename under %q.", s.cfg.outRoot),
											},
										},
										"required":             []string{"object_id"},
										"additionalProperties": false,
									},
								},
							},
							"required":             []string{"items"},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "get-list-objects-compact",
						"description": "REST wrapper for Anytype get-list-objects with compact output for a collection/set view. Calls the local Anytype API and returns stable top-level fields plus optional selected properties. Requires list_id and view_id; use get-list-views-compact first if the view_id is unknown. " + compactOutputOptionsDescription,
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"space_id": map[string]any{
									"type":        "string",
									"description": "Anytype space ID.",
								},
								"list_id": map[string]any{
									"type":        "string",
									"description": "Collection or set object ID.",
								},
								"view_id": map[string]any{
									"type":        "string",
									"description": "View ID used by Anytype's get-list-objects endpoint.",
								},
								"offset": map[string]any{
									"type":        "integer",
									"description": "Number of items to skip.",
									"default":     0,
								},
								"limit": map[string]any{
									"type":        "integer",
									"description": "Maximum number of items to request.",
									"default":     50,
								},
								"fields": map[string]any{
									"type":        "array",
									"description": "Top-level fields to include. Defaults to id, name, snippet, layout, archived, space_id, object.",
									"items":       map[string]any{"type": "string"},
								},
								"property_keys": map[string]any{
									"type":        "array",
									"description": "Property keys, IDs, or visible names to include in the compact properties object.",
									"items":       map[string]any{"type": "string"},
								},
								"include_properties": map[string]any{
									"type":        "boolean",
									"description": "Include simplified properties. If property_keys is empty, only max_properties are included.",
									"default":     false,
								},
								"include_type": map[string]any{
									"type":        "boolean",
									"description": "Include compact object type metadata.",
									"default":     false,
								},
								"include_icon": map[string]any{
									"type":        "boolean",
									"description": "Include icon data.",
									"default":     false,
								},
								"max_properties": map[string]any{
									"type":        "integer",
									"description": "Maximum number of properties to include when include_properties is true and property_keys is empty.",
									"default":     20,
								},
								"max_string_length": map[string]any{
									"type":        "integer",
									"description": "Truncate returned string values longer than this many characters. Use 0 to disable truncation.",
									"default":     500,
								},
								"filters": map[string]any{
									"type":                 "object",
									"description":          queryFiltersDescription,
									"additionalProperties": true,
								},
							},
							"required":             []string{"space_id", "list_id", "view_id"},
							"additionalProperties": false,
						},
					},
					map[string]any{
						"name":        "get-object-compact",
						"description": "REST wrapper for Anytype get-object with compact output. Returns stable object fields by default and optional markdown/type/icon/properties. Use format:\"md\" only when markdown body is needed. " + compactOutputOptionsDescription,
						"inputSchema": compactObjectToolSchema([]string{"space_id", "object_id"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID.",
							},
							"object_id": map[string]any{
								"type":        "string",
								"description": "Anytype object ID.",
							},
							"format": map[string]any{
								"type":        "string",
								"description": "Optional object body format.",
								"enum":        []string{"md"},
							},
						}),
					},
					map[string]any{
						"name":        "get-objects-compact-many",
						"description": "REST wrapper for fetching multiple Anytype objects in one MCP call. Uses the same compact options as get-object-compact and returns per-object results with index, object_id, object or error. " + bulkCompactOptionsDescription,
						"inputSchema": compactObjectToolSchema([]string{"space_id", "object_ids"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID shared by all objects.",
							},
							"object_ids": map[string]any{
								"type":        "array",
								"description": "Anytype object IDs to fetch. Use this instead of many repeated get-object-compact calls when the same compact options apply.",
								"items":       map[string]any{"type": "string"},
							},
							"format": map[string]any{
								"type":        "string",
								"description": "Optional object body format applied to every fetched object.",
								"enum":        []string{"md"},
							},
							"stop_on_error": map[string]any{
								"type":        "boolean",
								"description": "Stop at first failed object instead of continuing.",
								"default":     false,
							},
						}),
					},
					map[string]any{
						"name":        "create-objects-compact-many",
						"description": "Create multiple Anytype objects in one MCP call and return compact per-object results. Each item is sent as one create-object request. Each item must include type_key. Use body for initial content; update tools use markdown instead. " + bulkCompactOptionsDescription,
						"inputSchema": compactObjectToolSchema([]string{"space_id", "items"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID where all objects are created.",
							},
							"stop_on_error": map[string]any{
								"type":        "boolean",
								"description": "Stop at first failed object instead of continuing.",
								"default":     false,
							},
							"items": map[string]any{
								"type":        "array",
								"description": "Objects to create. Each item must include type_key and may include name, body, icon, properties, and template_id. Do not put response compact options inside items.",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"type_key": map[string]any{
											"type":        "string",
											"description": "Object type key, for example page, task, or another type key from list-types-compact/get-type-compact.",
										},
										"name": map[string]any{
											"type":        "string",
											"description": "Name of the new object.",
										},
										"body": map[string]any{
											"type":        "string",
											"description": "Initial object body. Markdown syntax is supported by Anytype.",
										},
										"icon": map[string]any{
											"type":                 "object",
											"description":          "Icon payload accepted by Anytype create-object.",
											"additionalProperties": true,
										},
										"properties": map[string]any{
											"type":        "array",
											"description": propertyLinkValueDescription + " Examples: {\"key\":\"description\",\"text\":\"Some text\"}, {\"key\":\"attachments\",\"files\":[\"FILE_OBJECT_ID\"]}, {\"key\":\"related\",\"objects\":[\"OBJECT_ID\"]}.",
											"items":       propertyLinkValueSchema(),
										},
										"template_id": map[string]any{
											"type":        "string",
											"description": "Optional template ID to use for creation.",
										},
									},
									"required":             []string{"type_key"},
									"additionalProperties": false,
								},
							},
						}),
					},
					map[string]any{
						"name":        "update-object-compact",
						"description": "REST wrapper for Anytype update-object with compact acknowledgement. Sends update fields to Anytype and returns a compact updated object. For properties, use typed fields such as text, number, select, multi_select, date, files, checkbox, url, email, phone, or objects; never send value:null because Anytype cannot determine the property link value type.",
						"inputSchema": compactObjectToolSchema([]string{"space_id", "object_id"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID.",
							},
							"object_id": map[string]any{
								"type":        "string",
								"description": "Anytype object ID.",
							},
							"icon": map[string]any{
								"type":                 "object",
								"description":          "Icon payload accepted by Anytype update-object.",
								"additionalProperties": true,
							},
							"markdown": map[string]any{
								"type":        "string",
								"description": "Updated markdown body.",
							},
							"name": map[string]any{
								"type":        "string",
								"description": "Updated object name.",
							},
							"properties": map[string]any{
								"type":        "array",
								"description": propertyLinkValueDescription + " Examples: {\"key\":\"description\",\"text\":\"Some text\"}, {\"key\":\"attachments\",\"files\":[\"FILE_OBJECT_ID\"]}, {\"key\":\"related\",\"objects\":[\"OBJECT_ID\"]}.",
								"items":       propertyLinkValueSchema(),
							},
							"type_key": map[string]any{
								"type":        "string",
								"description": "Updated object type key.",
							},
						}),
					},
					map[string]any{
						"name":        "update-objects-compact-many",
						"description": "Update multiple Anytype objects in one MCP call and return compact per-object results. Each item is sent as one update-object request. For properties, use typed fields such as text, number, select, multi_select, date, files, checkbox, url, email, phone, or objects; never send value:null because Anytype cannot determine the property link value type. " + bulkCompactOptionsDescription,
						"inputSchema": compactObjectToolSchema([]string{"space_id", "items"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID shared by all objects.",
							},
							"stop_on_error": map[string]any{
								"type":        "boolean",
								"description": "Stop at first failed object instead of continuing.",
								"default":     false,
							},
							"items": map[string]any{
								"type":        "array",
								"description": "Object updates. Each item must include object_id and at least one update field: icon, markdown, name, properties, or type_key. Do not put response compact options inside items.",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"object_id": map[string]any{
											"type":        "string",
											"description": "Anytype object ID to update.",
										},
										"icon": map[string]any{
											"type":                 "object",
											"description":          "Icon payload accepted by Anytype update-object.",
											"additionalProperties": true,
										},
										"markdown": map[string]any{
											"type":        "string",
											"description": "Updated markdown body.",
										},
										"name": map[string]any{
											"type":        "string",
											"description": "Updated object name.",
										},
										"properties": map[string]any{
											"type":        "array",
											"description": propertyLinkValueDescription + " Examples: {\"key\":\"description\",\"text\":\"Some text\"}, {\"key\":\"attachments\",\"files\":[\"FILE_OBJECT_ID\"]}, {\"key\":\"related\",\"objects\":[\"OBJECT_ID\"]}.",
											"items":       propertyLinkValueSchema(),
										},
										"type_key": map[string]any{
											"type":        "string",
											"description": "Updated object type key.",
										},
									},
									"required":             []string{"object_id"},
									"additionalProperties": false,
								},
							},
						}),
					},
					map[string]any{
						"name":        "search-global-compact",
						"description": "REST wrapper for Anytype global search with compact object results across accessible spaces. Use query for text search and types for object type keys. " + compactOutputOptionsDescription,
						"inputSchema": compactObjectToolSchema([]string{}, map[string]any{
							"offset": map[string]any{
								"type":        "integer",
								"description": "Number of items to skip.",
								"default":     0,
							},
							"limit": map[string]any{
								"type":        "integer",
								"description": "Maximum number of items to request.",
								"default":     50,
							},
							"query": map[string]any{
								"type":        "string",
								"description": "Search query.",
							},
							"sort": map[string]any{
								"type":                 "object",
								"description":          "Sort payload accepted by Anytype search.",
								"additionalProperties": true,
							},
							"types": map[string]any{
								"type":        "array",
								"description": "Object type keys to include.",
								"items":       map[string]any{"type": "string"},
							},
						}),
					},
					map[string]any{
						"name":        "search-space-compact",
						"description": "REST wrapper for Anytype search within one space with compact object results. Use query for text search and types for object type keys. Prefer this over global search when space_id is known. " + compactOutputOptionsDescription,
						"inputSchema": compactObjectToolSchema([]string{"space_id"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID.",
							},
							"offset": map[string]any{
								"type":        "integer",
								"description": "Number of items to skip.",
								"default":     0,
							},
							"limit": map[string]any{
								"type":        "integer",
								"description": "Maximum number of items to request.",
								"default":     50,
							},
							"query": map[string]any{
								"type":        "string",
								"description": "Search query.",
							},
							"sort": map[string]any{
								"type":                 "object",
								"description":          "Sort payload accepted by Anytype search.",
								"additionalProperties": true,
							},
							"types": map[string]any{
								"type":        "array",
								"description": "Object type keys to include.",
								"items":       map[string]any{"type": "string"},
							},
						}),
					},
					map[string]any{
						"name":        "list-objects-compact",
						"description": "REST wrapper for Anytype list-objects with compact object results from one space. Use this for broad object listing, not for collection/set view rows; use get-list-objects-compact when list_id and view_id matter. " + compactOutputOptionsDescription,
						"inputSchema": compactObjectToolSchema([]string{"space_id"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID.",
							},
							"offset": map[string]any{
								"type":        "integer",
								"description": "Number of items to skip.",
								"default":     0,
							},
							"limit": map[string]any{
								"type":        "integer",
								"description": "Maximum number of items to request.",
								"default":     50,
							},
							"filters": map[string]any{
								"type":                 "object",
								"description":          queryFiltersDescription,
								"additionalProperties": true,
							},
						}),
					},
					map[string]any{
						"name":        "list-types-compact",
						"description": "REST wrapper for Anytype list-types with compact type metadata. Omits icon and linked property definitions unless requested. Use this to discover type keys for create-objects-compact-many or search types. For linked property definitions, use property_keys or include_properties. Do not confuse type fields with object fields.",
						"inputSchema": compactTypeToolSchema([]string{"space_id"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID.",
							},
							"offset": map[string]any{
								"type":        "integer",
								"description": "Number of items to skip.",
								"default":     0,
							},
							"limit": map[string]any{
								"type":        "integer",
								"description": "Maximum number of items to request.",
								"default":     50,
							},
							"filters": map[string]any{
								"type":                 "object",
								"description":          queryFiltersDescription,
								"additionalProperties": true,
							},
						}),
					},
					map[string]any{
						"name":        "get-type-compact",
						"description": "REST wrapper for Anytype get-type with compact type metadata. Use this before property updates to inspect linked property keys and formats. type_id accepts a type ID or type key. For property definitions, use property_keys or include_properties.",
						"inputSchema": compactTypeToolSchema([]string{"space_id", "type_id"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID.",
							},
							"type_id": map[string]any{
								"type":        "string",
								"description": "Anytype type ID or key accepted by the API.",
							},
						}),
					},
					map[string]any{
						"name":        "get-list-views-compact",
						"description": "REST wrapper for Anytype get-list-views with compact view metadata for a collection/set. Use this to find the view_id required by get-list-objects-compact. Omits filters and sorts unless include_filters/include_sorts are true.",
						"inputSchema": compactViewToolSchema([]string{"space_id", "list_id"}, map[string]any{
							"space_id": map[string]any{
								"type":        "string",
								"description": "Anytype space ID.",
							},
							"list_id": map[string]any{
								"type":        "string",
								"description": "Collection or set object ID.",
							},
							"offset": map[string]any{
								"type":        "integer",
								"description": "Number of items to skip.",
								"default":     0,
							},
							"limit": map[string]any{
								"type":        "integer",
								"description": "Maximum number of items to request.",
								"default":     50,
							},
						}),
					},
				},
			},
		}, req.ID != nil
	case "tools/call":
		if req.ID == nil {
			return rpcResponse{}, false
		}
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.errorResponse(req.ID, -32602, "invalid params: expected tools/call payload"), true
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}

		payload, err := s.callTool(params.Name, params.Arguments)
		if err != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  toolResult(map[string]any{"error": err.Error()}, true),
			}, true
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  toolResult(payload, false),
		}, true
	default:
		if req.ID == nil {
			return rpcResponse{}, false
		}
		return s.errorResponse(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method)), true
	}
}

func (s *mcpServer) callTool(name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "file-info":
		return s.toolInfo(args)
	case "file-list-input":
		return s.toolListInput(args)
	case "file-list-output":
		return s.toolListOutput(args)
	case "file-upload":
		return s.toolUpload(args)
	case "file-download":
		return s.toolDownload(args)
	case "file-upload-many":
		return s.toolUploadMany(args)
	case "file-download-many":
		return s.toolDownloadMany(args)
	case "get-list-objects-compact":
		return s.toolListObjectsCompact(args)
	case "get-object-compact":
		return s.toolObjectGetCompact(args)
	case "get-objects-compact-many":
		return s.toolObjectsGetCompactMany(args)
	case "create-objects-compact-many":
		return s.toolObjectsCreateCompactMany(args)
	case "update-object-compact":
		return s.toolObjectUpdateCompact(args)
	case "update-objects-compact-many":
		return s.toolObjectsUpdateCompactMany(args)
	case "search-global-compact":
		return s.toolSearchCompact(args, false)
	case "search-space-compact":
		return s.toolSearchCompact(args, true)
	case "list-objects-compact":
		return s.toolSpaceObjectsCompact(args)
	case "list-types-compact":
		return s.toolListTypesCompact(args)
	case "get-type-compact":
		return s.toolGetTypeCompact(args)
	case "get-list-views-compact":
		return s.toolGetListViewsCompact(args)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func toolResult(payload any, isError bool) map[string]any {
	encoded, err := json.Marshal(payload)
	text := ""
	if err != nil {
		text = fmt.Sprintf(`{"error":"failed to encode result: %v"}`, err)
	} else {
		text = string(encoded)
	}

	return map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": text,
			},
		},
		"structuredContent": payload,
		"isError":           isError,
	}
}

func requiredString(args map[string]any, key string) (string, error) {
	value := optionalString(args, key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func requiredRawString(args map[string]any, key string) (string, error) {
	value := rawOptionalString(args, key)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func optionalString(args map[string]any, key string) string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func rawOptionalString(args map[string]any, key string) string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func optionalBool(args map[string]any, key string, fallback bool) bool {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback
	}
	v, ok := raw.(bool)
	if !ok {
		return fallback
	}
	return v
}

func optionalInt(args map[string]any, key string, fallback int) int {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return fallback
	}
}

func asObjectSlice(raw any) ([]map[string]any, error) {
	rawSlice, ok := raw.([]any)
	if !ok {
		return nil, errors.New("must be an array")
	}
	out := make([]map[string]any, 0, len(rawSlice))
	for i, item := range rawSlice {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d must be an object", i)
		}
		out = append(out, obj)
	}
	return out, nil
}

func ensureDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty directory path")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absPath, 0755); err != nil {
		return "", err
	}
	return absPath, nil
}

func resolveUnderRoot(root string, value string, allowDir bool) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(value) == "" {
		return "", errors.New("empty path")
	}

	candidate := value
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}

	rel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside root %q", candidateAbs, rootAbs)
	}

	info, err := os.Stat(candidateAbs)
	if err != nil {
		return "", err
	}
	if !allowDir && info.IsDir() {
		return "", fmt.Errorf("path %q is a directory", candidateAbs)
	}

	return candidateAbs, nil
}

func validateTargetName(name string) error {
	if name == "" {
		return errors.New("empty filename")
	}
	if name != filepath.Base(name) {
		return errors.New("must be a plain filename, not a path")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return errors.New("must not contain path separators")
	}
	if name == "." || name == ".." {
		return errors.New("invalid filename")
	}
	return nil
}

func readMessage(r *bufio.Reader) ([]byte, messageFraming, error) {
	line, err := readNonEmptyLine(r)
	if err != nil {
		return nil, messageFramingContentLength, err
	}

	trimmedLine := strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(strings.TrimLeft(trimmedLine, " \t"), "{") {
		return []byte(trimmedLine), messageFramingLine, nil
	}

	contentLength := -1

	for {
		if trimmedLine == "" {
			break
		}

		parts := strings.SplitN(trimmedLine, ":", 2)
		if len(parts) != 2 {
			return nil, messageFramingContentLength, fmt.Errorf("invalid header line %q", trimmedLine)
		}
		headerName := strings.ToLower(strings.TrimSpace(parts[0]))
		headerValue := strings.TrimSpace(parts[1])
		if headerName == "content-length" {
			parsed, err := strconv.Atoi(headerValue)
			if err != nil {
				return nil, messageFramingContentLength, fmt.Errorf("invalid Content-Length: %w", err)
			}
			contentLength = parsed
		}

		line, err = r.ReadString('\n')
		if err != nil {
			return nil, messageFramingContentLength, err
		}
		trimmedLine = strings.TrimRight(line, "\r\n")
	}

	if contentLength < 0 {
		return nil, messageFramingContentLength, errors.New("missing Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, messageFramingContentLength, err
	}
	return body, messageFramingContentLength, nil
}

func readNonEmptyLine(r *bufio.Reader) (string, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(line) != "" {
			return line, nil
		}
	}
}

func writeResponse(w *bufio.Writer, framing messageFraming, response rpcResponse) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}

	if framing == messageFramingLine {
		if _, err := w.Write(data); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return w.Flush()
}

func (s *mcpServer) errorResponse(id *json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &rpcError{
			Code:    code,
			Message: message,
		},
	}
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
