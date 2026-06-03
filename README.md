# anytype-extension-mcp

Minimal MCP STDIO server for Anytype file operations over gRPC, plus compact REST wrappers for large Anytype API responses.

The server is meant as a sidecar to the official Anytype MCP. Tool names follow the official schema; compact REST wrappers use the `-compact` suffix.

- `file-upload`
- `file-download`
- `file-upload-many`
- `file-download-many`
- `file-list-input`
- `file-list-output`
- `file-info`
- `get-list-objects-compact`
- `list-objects-compact`
- `get-object-compact`
- `get-objects-compact-many`
- `update-object-compact`
- `update-objects-compact-many`
- `create-objects-compact-many`
- `search-global-compact`
- `search-space-compact`
- `list-types-compact`
- `get-type-compact`
- `get-list-views-compact`

## Environment

The server reads the following environment variables:

- `ANYTYPE_GRPC_ADDR` (default: `dns:///127.0.0.1:31010`)
- `ANYTYPE_SESSION_TOKEN` (token for the gRPC file tools)
- `ANYTYPE_TIMEOUT` (default: `30s`)
- `ANYTYPE_API_BASE_URL` (default: `http://127.0.0.1:31012`)
- `ANYTYPE_API_KEY` (required for `*-compact` REST tools)
- `ANYTYPE_API_VERSION` (default: `2025-11-08`)
- `ANYTYPE_FILES_IN_ROOT` (default: `/data/in`)
- `ANYTYPE_FILES_OUT_ROOT` (default: `/data/out`)

### Session token

The gRPC file tools need a session token. Resolution order:

1. `ANYTYPE_SESSION_TOKEN`, if set (takes precedence).
2. Otherwise the OS keyring (service `anytype-cli`, user `session-token`), as
   stored by `anytype-cli login`.

In containers or headless environments without a Secret Service / DBus there is
no keyring &ndash; set `ANYTYPE_SESSION_TOKEN` there. The REST `*-compact` tools
use `ANYTYPE_API_KEY` instead and do not need a token.

## Tool: file-upload

Required fields:

- `space_id`
- `staged_path` (must be located under `ANYTYPE_FILES_IN_ROOT`)

Optional fields:

- `type`: `file|image|video|audio|none`
- `style`: `auto|link|embed`

## Tool: file-download

Required fields:

- `object_id`

Optional:

- `target_name` (filename only, no path)

The download target is always `ANYTYPE_FILES_OUT_ROOT`.

## Compact property updates

`update-object-compact`, `update-objects-compact-many`, and `create-objects-compact-many` pass `properties` to the Anytype REST API. Each property item must be a typed property-link value:

```json
{"key": "description", "text": "Some text"}
{"key": "amount", "number": 42}
{"key": "status", "select": "done"}
{"key": "tags", "multi_select": ["important"]}
{"key": "due_date", "date": "2026-04-27"}
{"key": "attachments", "files": ["FILE_OBJECT_ID"]}
{"key": "related", "objects": ["OBJECT_ID"]}
{"key": "done", "checkbox": true}
```

Do not send generic `value` fields or `null` values for updates. This payload is invalid:

```json
{"key": "attachments", "value": null}
```

Anytype returns `could not determine property link value type` for such payloads because `null` does not say whether the property is text, files, objects, select, date, or another format. To clear links, use a typed empty array, for example `{"key":"attachments","files":[]}` or `{"key":"related","objects":[]}`.

## Compact output options

Most `*-compact` tools accept response-shaping options:

- `fields` selects top-level fields such as `id`, `name`, `snippet`, `layout`, `archived`, `space_id`, `object`, `type`, `icon`, `properties`, or `markdown`.
- `property_keys` selects entries inside the returned `properties` object. It accepts technical property keys, property IDs, or visible property names and automatically enables property output.
- `include_properties` returns a limited set of properties when exact `property_keys` are not known.

Do not put property names in `fields`, and do not put top-level fields such as `id` or `name` in `property_keys`.

For `get-objects-compact-many`, `update-objects-compact-many`, and `create-objects-compact-many`, compact output options belong at the top level of the tool call, not inside `items`.

## List, search, and view tools

- Use `search-space-compact` when `space_id` is known; use `search-global-compact` only when the space is unknown.
- Use `list-objects-compact` for broad object listing in a space.
- Use `get-list-views-compact` to find a collection/set `view_id`, then `get-list-objects-compact` to fetch rows from that view.
- `filters` on list/search tools are raw URL query parameters for the REST endpoint, not Notion-style or Anytype view filter definitions.

## Build locally (static)

```bash
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o ./bin/anytype-extension-mcp .
```

## Docker build (static)

```bash
docker build -t anytype-extension-mcp:local .
```

## License

This project's own code is licensed under the MIT License (see `LICENSE`).

The gRPC file tools depend on `github.com/anyproto/anytype-heart`, which is
licensed under the **Any Source Available License 1.0** (Non-Commercial use or
Commercial use in Allowed Networks only). This source repository only references
anytype-heart via `go.mod`, but **compiled binaries / Docker images** link its
code and are therefore subject to its license terms. See `NOTICE` for details.
