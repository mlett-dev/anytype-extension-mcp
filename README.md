# anytype-extension-mcp

An MCP STDIO server for Anytype with 129 tools: block-level editing, query and
view configuration, tables, import/export and file transfer over gRPC, plus the
REST surface of the official Anytype API.

## Relationship to the official server

[`@anyproto/anytype-mcp`](https://github.com/anyproto/anytype-mcp) generates its
tools from the Anytype REST OpenAPI spec. That spec covers spaces, objects,
properties, tags, types and templates — it has no block endpoints, and list
views are read-only. Anything that edits page content or configures a query has
to go through anytype-heart's gRPC interface instead, which is what this server
does.

It started as a sidecar and grew to replace the official server outright,
because some clients only ever connect to a single MCP endpoint. Tool names
follow the official schema where they overlap; there are no duplicates — a tool
that has a `-compact` variant here was not also added under its official name.

**gRPC is loopback-only.** anytype-heart binds its gRPC port to `127.0.0.1` and
offers no flag to change it. A containerised deployment therefore has to share
the network namespace of the Anytype container
(`docker run --network container:<name>`); a published port will not work.

## Tool set

All tools are visible by default. Hiding destructive operations does not
prevent damage, it only prevents repair: a model that just created the wrong
tag or property could no longer clean up after itself. The goal is parity with
what the GUI can do.

`ANYTYPE_MCP_TOOLSET=lean` hides four rarely needed tools (`create-space`,
`update-space`, `list-members`, `get-member`). They remain callable by name.

**Spaces, types, properties, tags (24)** — `list-spaces` `get-space`
`create-space` `update-space` `list-members` `get-member` `list-properties`
`get-property` `create-property` `update-property` `delete-property`
`list-tags` `get-tag` `create-tag` `update-tag` `delete-tag` `create-type`
`update-type` `delete-type` `list-templates` `get-template` `delete-object`
`add-list-objects` `remove-list-object`

**Compact REST wrappers (12)** — `get-object-compact` `get-objects-compact-many`
`update-object-compact` `update-objects-compact-many`
`create-objects-compact-many` `list-objects-compact` `list-types-compact`
`get-type-compact` `search-space-compact` `search-global-compact`
`get-list-views-compact` `get-list-objects-compact`

**Blocks (14)** — `block-create` `block-delete` `block-duplicate` `block-list`
`block-mark` `block-merge` `block-move` `block-paste` `block-split`
`block-style` `block-turn-into` `block-set-text` `block-set-checked`
`block-column-width`

**Queries and views (14)** — `query-create` `query-inspect` `query-set-source`
`query-filter-add` `query-filter-remove` `query-sort-add` `query-sort-remove`
`query-order-set` `query-view-create` `query-view-update` `query-view-delete`
`query-view-arrange` `object-to-query` `object-to-collection`

**Tables (9)** — `table-insert` `table-inspect` `table-delete` `table-duplicate`
`table-move` `table-set-cells` `table-row-clear` `table-row-header`
`table-sort`

**Space operations (12)** — `block-embed-query` `block-export` `object-date`
`object-flags` `object-delete-permanently` `object-version-diff`
`space-get-homepage` `space-set-homepage` `space-file-usage` `get-type-layout`
`type-set-layout` `schema-set-order`

**Transfer and versions (10)** — `object-import` `object-export`
`object-export-files` `object-duplicate` `object-versions`
`object-version-show` `object-version-restore` `objects-modify-property`
`template-create` `type-set-featured-properties`

**Files (9)** — `file-info` `file-list-input` `file-list-output` `file-upload`
`file-download` `file-upload-many` `file-download-many`
`file-stage-attachment` `file-upload-attachment`

**Extras (10)** — `block-embed-create` `block-embed-set-text`
`block-extract-to-object` `block-link-appearance` `block-relation-add`
`object-apply-template` `object-create-from-url` `object-graph`
`unsplash-search` `unsplash-download`

**Covers and object state (8)** — `object-get-cover` `object-set-cover`
`object-set-cover-from-file` `object-set-cover-from-attachment`
`block-file-create` `object-set-archived` `object-set-favorite` `object-undo`

**Usage analysis (4)** — `analyze-property-usage` `analyze-schema-usage`
`clean-unused-tags` `list-archived`

**Diagnostics (3)** — `server-info` `debug-receive-file`
`debug-receive-openai-file`

`server-info` reports this server's version, its tool count and the
anytype-heart build it is connected to. The MCP handshake carries a version
too, but connectors keep that to themselves, and the answer decides which
behaviours to expect — so it is also a tool.

## Why the tools work the way they do

`docs/anytype-heart-notes.md` records verified anytype-heart behaviour — an RPC
that reports success and does nothing, a write invisible to the next read, a
filter that silently matches everything. Most of the roundabout-looking choices
in this server trace back to an entry there, and the entries are expensive to
rediscover.

## IDs, not keys, in path parameters

`property_id`, `type_id` and `tag_id` require the `bafyrei…` ID, **not** the
key. A key yields HTTP 404 or 500 (`GET /v1/spaces/<s>/types/page` returns 500
`failed to retrieve type`). This holds without exception, `get-type-compact`
included. IDs come from `list-properties`, `list-types-compact` and `list-tags`.

The `key` in those responses is meant for filter and property *values*
(`property_key`), never for a path.

## Batch processing

Tools whose name ends in `-many`, and most write tools, accept an `items` array
so a batch runs in one call. Batch and single path share the same handler, so
they cannot drift apart.

Compact output options (`fields`, `property_keys`, `include_properties`,
`include_type`, `include_icon`, `max_properties`) belong at the **top level** of
the call and apply to every returned object — not inside `items`.

## Environment

- `ANYTYPE_GRPC_ADDR` (default: `dns:///127.0.0.1:31010`)
- `ANYTYPE_SESSION_TOKEN` — token for the gRPC tools
- `ANYTYPE_TIMEOUT` (default: `30s`)
- `ANYTYPE_API_BASE_URL` (default: `http://127.0.0.1:31012`)
- `ANYTYPE_API_KEY` — required for the REST tools
- `ANYTYPE_API_VERSION` (default: `2025-11-08`)
- `ANYTYPE_FILES_IN_ROOT` (default: `/data/in`)
- `ANYTYPE_FILES_OUT_ROOT` (default: `/data/out`)
- `ANYTYPE_FILES_SERVER_IN_ROOT` / `ANYTYPE_FILES_SERVER_OUT_ROOT` — the same
  directories as seen by the Anytype server, when its mount path differs
- `ANYTYPE_MCP_TOOLSET` — `full` (default) or `lean`
- `UNSPLASH_ACCESS_KEY` — enables `unsplash-search` / `unsplash-download`

The Unsplash key is deliberately read by this server rather than passed to
Anytype: anytype-heart authenticates with a Bearer header, which Unsplash
rejects for access keys, so the server calls `api.unsplash.com` directly.
Without a key the Unsplash tools report that none is configured.

### Session token

The gRPC tools need a session token. Resolution order:

1. `ANYTYPE_SESSION_TOKEN`, if set (takes precedence).
2. Otherwise the OS keyring (service `anytype-cli`, user `session-token`), as
   stored by `anytype-cli login`.

In containers or headless environments without a Secret Service / DBus there is
no keyring — set `ANYTYPE_SESSION_TOKEN` there. The REST tools use
`ANYTYPE_API_KEY` instead and do not need a token.

## Getting files in and out

There are two separate routes, and they solve different problems.

**Files already on the server's disk.** `file-list-input` lists what is staged
under `ANYTYPE_FILES_IN_ROOT`; `file-upload` takes a `staged_path` from that
listing. Reuse the returned `relative_path` verbatim — spaces and umlauts are
allowed and must not be rewritten. Downloads land in `ANYTYPE_FILES_OUT_ROOT`.

**Files the calling runtime holds.** `file-stage-attachment` and
`file-upload-attachment` take a real file argument, and the server fetches the
bytes itself from a temporary URL the runtime supplies. Nothing about the file
is paid for as tokens, and there is no practical size limit.

There is no base64 route. It was removed rather than deprecated: encoding a
2 MB image into tool arguments costs roughly 700 000 tokens, and a tool that
does not exist cannot be chosen.

### Client-specific tools

`file-stage-attachment`, `file-upload-attachment`,
`object-set-cover-from-attachment` and `debug-receive-openai-file` declare
`_meta: {"openai/fileParams": ["file"]}`. That marker is an OpenAI connector
convention: the runtime resolves a file into
`{download_url, file_id, mime_type, file_name}` before the call is made.

**Clients that do not implement it will see a `file` object they cannot fill.**
For those, use `file-upload` with a staged path instead.

`block-file-create` deliberately takes no file argument: it hands a URL to
Anytype, which loads it asynchronously, and a URL that fails there stays on
`file_state: uploading` forever instead of reporting an error. Stage the file
first, then pass `staged_path`.

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

`update-object-compact`, `update-objects-compact-many`, and
`create-objects-compact-many` pass `properties` to the Anytype REST API. Each
property item must be a typed property-link value:

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

Do not send generic `value` fields or `null` values for updates. This payload is
invalid:

```json
{"key": "attachments", "value": null}
```

Anytype returns `could not determine property link value type` for such payloads
because `null` does not say whether the property is text, files, objects,
select, date, or another format. To clear links, use a typed empty array, for
example `{"key":"attachments","files":[]}` or `{"key":"related","objects":[]}`.

Exactly one typed value field per item. Anytype's own decoder takes the first
field it recognises and silently discards the rest while still answering 200, so
this server rejects ambiguous items before they are sent.

## Compact output options

Most `*-compact` tools accept response-shaping options:

- `fields` selects top-level fields such as `id`, `name`, `snippet`, `layout`,
  `archived`, `space_id`, `object`, `type`, `icon`, `properties`, or `markdown`.
- `property_keys` selects entries inside the returned `properties` object. It
  accepts technical property keys, property IDs, or visible property names and
  automatically enables property output.
- `include_properties` returns a limited set of properties when exact
  `property_keys` are not known.

Do not put property names in `fields`, and do not put top-level fields such as
`id` or `name` in `property_keys`.

## List, search, and view tools

- Use `search-space-compact` when `space_id` is known; use
  `search-global-compact` only when the space is unknown.
- Use `list-objects-compact` for broad object listing in a space.
- Use `get-list-views-compact` to find a collection/set `view_id`, then
  `get-list-objects-compact` to fetch rows from that view.
- `filters` on list/search tools are raw URL query parameters for the REST
  endpoint, not Notion-style or Anytype view filter definitions.
- File objects appear in neither `list-objects-compact` nor full-text search.
  Find them with `search-space-compact` and `types: ["image"]` or `["file"]`.
  Note that Anytype names file objects **without** their extension.

## Partial updates

Several anytype-heart RPCs replace an entire struct rather than patching fields,
and assign every simple field unconditionally. A view update that omits `layout`
would therefore turn a kanban board into a table and drop its grouping,
silently.

This server reads the stored object first and overwrites only what the caller
actually supplied — tracked by the *presence* of an argument, not its value.
This applies to `query-view-update`, view column widths, and
`block-link-appearance`.

## Live tests

`livetest/` holds MCP-over-stdio suites that run against a **real** Anytype
instance. That is deliberate: nearly every bug found in this project was a quirk
of anytype-heart that only appears in operation — an RPC that reports success
and does nothing, a write delayed by three seconds, an ID that changes on move.
A mock would have found none of them.

```bash
export ANYTYPE_MCP_LAUNCHER=/path/to/launcher.sh
export ANYTYPE_LIVETEST_SHARED_ROOT=/path/to/shared/files
python3 livetest/run.py            # all suites
python3 livetest/run.py blocks     # matching suites only
```

The suites write into the **first space** of the instance. Everything they
create is named `LIVETEST …`, archived at the end and then permanently deleted.
Only IDs the run created itself are deleted; leftovers found by name prefix are
archived but never deleted, because a matching name is not proof that an object
came from a test.

## Before you commit

This repository is public and every push is immediate, so a pre-commit check
scans staged content for things that should not be published — credentials,
absolute home directories, real content IDs, non-loopback addresses:

```bash
scripts/check-private.sh          # staged content, what a commit would record
scripts/check-private.sh --all    # the whole working tree

git config core.hooksPath .githooks   # or install it as a pre-commit hook
```

Installation-specific strings — host paths, container names, space names —
belong in a git-ignored `.private-patterns` file (one extended regex per line),
not in the script itself: writing them there would publish exactly what they
are meant to keep out.

## Build locally (static)

```bash
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o ./bin/anytype-extension-mcp .
```

## Docker build (static)

```bash
docker build -t anytype-extension-mcp:local .
```

Run it inside the Anytype container's network namespace, since gRPC is
loopback-only:

```bash
docker run --rm -i \
  --network container:<anytype-container> \
  -e ANYTYPE_SESSION_TOKEN="$TOKEN" \
  -e ANYTYPE_API_KEY="$KEY" \
  -v /path/to/files:/data \
  anytype-extension-mcp:local
```

## License

This project's own code is licensed under the MIT License (see `LICENSE`).

Most of this server — every gRPC-backed tool, which is the large majority of the
129 — depends on `github.com/anyproto/anytype-heart`, licensed under the **Any
Source Available License 1.0**: Non-Commercial Use, or Commercial Use in
[Allowed Networks](https://networks.any.coop) only. A self-hosted any-sync
network is not an Allowed Network, so against one, only Non-Commercial Use is
licensed.

This source repository contains no anytype-heart code and only references it via
`go.mod`. **Compiled binaries and Docker images statically link it** and are
therefore subject to its terms — including that the linked anytype-heart code
stays under that license and cannot be redistributed as MIT. See `NOTICE`.
