# anytype-extension-mcp

An MCP STDIO server for Anytype with 144 tools: block-level editing, query and
view configuration, tables, import/export and file transfer over gRPC, plus the
REST surface of the official Anytype API.

Uses MCP `2025-11-25`, with `2025-06-18` compatibility. Initialization echoes
either supported revision; requests for other revisions are offered
`2025-11-25`. The client must support that revision to continue.

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

**Tables (10)** — `table-insert` `table-inspect` `table-delete` `table-duplicate`
`table-move` `table-set-cells` `table-row-clear` `table-row-header`
`table-sort` `table-column-width`

**Space operations (12)** — `block-embed-query` `block-export` `object-date`
`object-flags` `object-delete-permanently` `object-version-diff`
`space-get-homepage` `space-set-homepage` `space-file-usage` `get-type-layout`
`type-set-layout` `schema-set-order`

**Sidebar (9)** — `space-system-objects` `sidebar-inspect` `sidebar-widget-add`
`sidebar-widget-update` `sidebar-widget-remove` `sidebar-arrange`
`sidebar-snapshot` `sidebar-apply` `sidebar-prune`

**Transfer and versions (10)** — `object-import` `object-export`
`object-export-files` `object-duplicate` `object-versions`
`object-version-show` `object-version-restore` `objects-modify-property`
`template-create` `type-set-featured-properties`

**Files (14)** — `file-info` `file-list-input` `file-list-output` `file-upload`
`file-download` `file-upload-many` `file-download-many`
`file-stage-attachment` `file-upload-attachment` `file-export`
`file-export-many` `pdf-inspect` `pdf-render-pages` `object-render`

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
- `ANYTYPE_TRANSFER_TIMEOUT` (default: `2m`): fetching an attachment's
  download URL and uploading it to Anytype
- `ANYTYPE_API_BASE_URL` (default: `http://127.0.0.1:31012`)
- `ANYTYPE_API_KEY` — required for the REST tools
- `ANYTYPE_API_VERSION` (default: `2025-11-08`)
- `ANYTYPE_FILES_IN_ROOT` (default: `/data/in`)
- `ANYTYPE_FILES_OUT_ROOT` (default: `/data/out`)
- `ANYTYPE_FILES_SERVER_IN_ROOT` / `ANYTYPE_FILES_SERVER_OUT_ROOT` — the same
  directories as seen by the Anytype server, when its mount path differs
- `ANYTYPE_MCP_TOOLSET` — `full` (default) or `lean`
- `UNSPLASH_ACCESS_KEY` — enables `unsplash-search` / `unsplash-download`
- `ANYTYPE_RENDER_URL` — API of a renderer daemon; enables `object-render`
- `ANYTYPE_CALL_LOG_DIR` — directory for the call log (see below); unset, no
  log is written. `ANYTYPE_CALL_LOG_DAYS` (default `30`) days are kept, and
  `AEM_CONSUMER` is written into every line to name who started the server

The Unsplash key is deliberately read by this server rather than passed to
Anytype: anytype-heart authenticates with a Bearer header, which Unsplash
rejects for access keys, so the server calls `api.unsplash.com` directly.
Without a key the Unsplash tools report that none is configured.

### Call log

With `ANYTYPE_CALL_LOG_DIR` set, the server appends one JSON line per message
to `calls-YYYY-MM-DD.jsonl` (UTC) in that directory: time, a random id per
server process and a sequence number, the client from `initialize`, the
method and tool, the argument *names*, a hash over the arguments, every
`*_id` argument as a short hash, whether the tool writes, duration, result
size, ok or the error message (Anytype ids in it hashed), hashes of the
export links handed out and of the URIs read back, and notes such as a reused
render or export. Argument values, texts and results are never written.

One process can serve many conversations: the ChatGPT tunnel sends every chat
through a single one. ChatGPT names the conversation in each request's
`_meta` (`openai/session`, an anonymized id); its hash is the `chat` field.
No other `_meta` value is read, since it also carries the user's location.

Several server processes can share the directory: lines are appended whole,
files are never renamed, days past the retention are deleted at start, and a
day stops at 64 MiB after one marker line. A log that cannot be written
switches itself off instead of failing a call.

`scripts/calllog-report.py DIR [--days N] [--client TEXT] [--tool NAME] [--chat HASH]`
summarises it: sessions per client, ChatGPT conversations with their calls and
writes, calls, failures, p50/p95 time and size per tool, calls repeated with
the same arguments within 120 s in one conversation (split into reads and
writes), export links never read, the most frequent errors and the slowest
calls.

### Session token

The gRPC tools need a session token. Resolution order:

1. `ANYTYPE_SESSION_TOKEN`, if set (takes precedence).
2. Otherwise the OS keyring (service `anytype-cli`, user `session-token`), as
   stored by `anytype-cli login`.

In containers or headless environments without a Secret Service / DBus there is
no keyring — set `ANYTYPE_SESSION_TOKEN` there. The REST tools use
`ANYTYPE_API_KEY` instead and do not need a token.

## Getting files in and out

There are separate routes for local paths, runtime-held uploads and MCP exports.

**Files already on the server's disk.** `file-list-input` lists what is staged
under `ANYTYPE_FILES_IN_ROOT`; `file-upload` takes a `staged_path` from that
listing. Reuse the returned `relative_path` verbatim — spaces and umlauts are
allowed and must not be rewritten. Downloads land in `ANYTYPE_FILES_OUT_ROOT`.

**Files the calling runtime holds.** `file-stage-attachment` and
`file-upload-attachment` take a real file argument, and the server fetches the
bytes itself from a temporary URL the runtime supplies. The model does not
write the file bytes into its tool arguments. This upload route is limited to
100 MiB per file; the fetch and the Anytype upload after it share
`ANYTYPE_TRANSFER_TIMEOUT`.

There is no tool input that accepts base64. It was removed rather than deprecated: encoding a
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

## Tools: file-export, file-export-many and MCP resources

Call `file-export` with an Anytype file `object_id` and optionally a plain
`filename`. Any file type is accepted up to **6 MiB (6,291,456 bytes)**,
inclusive. The original bytes are preserved; larger files fail without
truncation or conversion. `file-preview` and larger-file transfer are not yet
implemented.

**PDF pages.** For a PDF, `pages` (e.g. `"1-5,9"`) exports a new PDF with only
those pages, in document order, named like `Report (pages 1-5,9).pdf` unless
`filename` is given. Only page numbers and `N-M` ranges are accepted; the
selection is sorted and merged. The source may be up to 256 MiB, and the
extract must still fit 6 MiB. The result carries `pages` and
`source_page_count`, and its bytes are a new file, not the original. A PDF that
is too large as a whole is refused with its page count, so the caller can
choose ranges. Pages are extracted with pdfcpu, which writes different bytes on
every run, so a repeated page export is matched by the source's SHA-256 and the
selection rather than by its own hash. `file-export-many` accepts `pages` per
item.

The result contains a native `content` block of type `resource_link` with an
`anytype-export://<opaque-handle>/<filename>` URI, plus small metadata in text
and `structuredContent`: name, MIME type, size, SHA-256 and `expires_at`.
The client fetches the bytes with `resources/read`; that response contains
`contents[].blob` (base64), never a text block or structured JSON containing
the encoded file. A supporting host can materialize the resource as a file;
ChatGPT attachment display and image previews depend on the client.

`file-export-many` takes `items`, 1 to 20 entries of `object_id` and optional
`filename`, and exports each like `file-export` over one gRPC connection. The
result holds one `resource_link` per exported file in item order, and
`structuredContent.results` lists every item by `index` with either the
export metadata or its `error`. A malformed item rejects the whole call before
anything is exported; a download, size or cache error affects only its item.
The call fails only when no file could be exported. Whether a client fetches
every link of one result is up to the client.

Exports expire after **15 minutes**. Snapshots and manifests live in the
reserved `.mcp-exports` directory under `ANYTYPE_FILES_OUT_ROOT`, so links
remain readable across connections and container restarts when the output
volume is shared. Expired snapshots are cleaned on the next export or when
their expired URI is read. Exporting the same unchanged file again (same
`object_id`, filename and SHA-256) returns the existing link while it has at
least 5 minutes left, instead of storing another snapshot. The cache holds up
to 384 MiB in at most 512 exports; beyond that, new exports are refused with
the time the next export expires. Private snapshots are excluded from
file-list-output.

The server advertises `resources: {}` with no subscriptions or list-change
notifications. `resources/list` and `resources/templates/list` return empty
lists: export links are returned on demand and are not a browsable catalog.
Only valid, unexpired export URIs are readable; arbitrary server paths are not
accepted. The existing `file-download` and `file-download-many` still return
local paths and retain their existing behavior.

## Tool: pdf-inspect

`pdf-inspect` describes a PDF stored in Anytype without handing it over. Every
answer starts with an overview whose size does not depend on the page count:
`page_count`, `page_sizes` (pages grouped by size, with `width_mm`,
`height_mm` and a paper `format` where one fits) and `text_layer`, which names
the pages with and without a text layer as ranges, e.g.
`"with_text": "1-35,40-412"`, plus their counts. A page counts as having a text
layer from 20 non-space characters on. Ranges are capped at 20 per list and
size groups at 10; `ranges_truncated` marks a cut, the counts stay complete.
`size_bytes` and `fits_file_export` tell whether `file-export` can hand over
the whole file.

- `pages` (e.g. `"37-38"`) adds per-page details for up to 20 pages per call;
  `text: true` adds their text layer, at most 20,000 characters per call. The
  rest is named in `next_pages`.
- `query` searches the text layer case-insensitively, within `pages` if given:
  `match_count`, `pages_with_matches`, up to 20 snippets and `not_searchable`,
  the pages without a text layer. The server reads text layers only; it does
  no OCR.

PDFium runs as WebAssembly (go-pdfium on wazero), so the scratch image needs
no C library. It starts on first use, which takes about 1.6 s, holds about
300 MB while loaded and stops after 10 idle minutes. Reading a document is
bounded by one minute; a session over that is killed, so a malformed PDF cannot
block the server. The pages of the last four documents stay cached by content
hash, so overview, text and search read a PDF once. Sources up to 256 MiB are
accepted.

## Tool: pdf-render-pages

`pdf-render-pages` renders pages of a PDF as images, one `resource_link` per
page in page order: `format` `jpeg` (default) or `png`, `dpi` 50-300 (default
150), up to 20 pages per call with the rest in `next_pages`. `include_text`
adds each page's text layer under the same 20,000-character budget as
`pdf-inspect`.

**Size limit per image.** Every page is its own image, link and
`resources/read`, so the 6 MiB export limit applies to each image separately.
A call of 20 pages can therefore hand over far more than a PDF extract from
`file-export` with `pages`, which has to fit all its pages into one 6 MiB file.
The 6 MiB itself is unchanged: it is the budget for one message on the client
connection, and a single image cannot exceed it either. A page whose image
would be larger is rendered again at a DPI scaled to fit, down to 50 DPI;
each result gives `dpi`, `width_px`, `height_px` and, when lowered,
`dpi_reduced`. A page that does not fit even then fails alone, the others are
still rendered. Images are capped at 40 megapixels. Twenty pages at the
largest size come to 120 MiB of the 384 MiB export cache.

Rendering the same page, format and DPI again returns the same link without
starting PDFium, which matters because ChatGPT sends each tool call twice.
Each page renders in its own PDFium session, bounded by 30 s. Measured on a
Letter page: 15-170 ms per image at 150-300 DPI.

## Tool: object-render

`object-render` shows an object as the Anytype GUI does: a `resource_link` to a
JPEG or PNG, plus a layout report measured in the rendered page:

- the layout (`edit`, `set`, `collection`, …), the content width and the
  content height (`content_height_px`: where the last visible element ends);
- per table block the rendered column widths, how many pixels are hidden at
  the side when it is wider than the page (`cut_off_px`), how many cells wrap,
  and which cells clip (row, column);
- per query or collection view (`dataviews`, embedded or the object itself)
  its kind and view type, what it shows (for an embedded view the query or
  collection it points at, `source_id` and `source_name`, and for a query the
  types it lists, `query_of`), the active view's name with its filter and sort
  counts, the rows shown in the image and the rows in total, and for a grid
  the columns with their widths, `cut_off_px`, and the cells whose text is cut
  off (row and column name) — or, with "wrap content" on, how many cells wrap;
- text blocks cut off at the side, and images that failed or were still
  loading, by block id.

`complete` says whether the image reaches the end of the object;
`incomplete_reason` says why not (the first screen with `full_height: false`,
or a page past the 16000 px limit, which also sets `truncated`). Warnings say
when the picture was taken while network, fonts, images or rows were still
loading, when `expand_toggles` left toggles closed, and when embedded views
hold more rows than they show. The report is the more reliable channel: it
states in numbers what the image shows, and it arrives even when a client does
not fetch the link; `image: false` returns the report alone.

An embedded view is not the query it points at opened on its own: it keeps
views of its own, with their own filters and sorts, and shows the first rows
(the view's page limit, 10 by default) above a "Load more" button.
`all_rows: true` presses that button until every row shows, up to 500 per
view, as a user scrolling through it would.

The rendering is not done here. This server forwards the request to a separate
daemon at `ANYTYPE_RENDER_URL` —
[anytype-render](https://github.com/mlett-dev/anytype-render) — which runs the
anytype-ts web build in a headless browser against the same anytype-heart and
hands back the image and the report. Without that variable the tool says it is not set up. What the
daemon must guarantee, because it shares heart with every other client:

- the browser reaches heart only through the daemon, which lets through an
  allowlist of reading calls and answers start-up calls with global effect
  (initial parameters, file auto-download, device state) itself;
- `AccountSelect` only for the account already running — another id makes
  heart stop the running account;
- its own session per render (`WalletCreateSession` with a token), closed
  afterwards, because heart keeps one event stream per token.

Before calling the daemon the server reads the object once: a wrong id fails
there (as in every tool, heart's "tree does not exist" and "space not exists"
come with a plain "object not found in this space" or "space not found" in
front), and a hash of the blocks lets a repeated identical call within 90 s
return the first result (ChatGPT sends calls twice) without ever answering an
edited object with the old picture. Any writing tool call clears these
remembered results, since a write can change what a page shows without
changing its blocks (the rows of an embedded query). `expand_toggles` opens every toggle and
toggle heading; opening the object sets its last-opened date, nothing else is
written.

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
- Anytype's REST search leaves file objects out unless a file type is
  requested. Without `types`, `search-space-compact` and
  `search-global-compact` therefore run a second search over the file types
  (`file`, `image`, `video`, `audio`; PDFs are `file`) and list those hits after
  the other objects, with one pagination over both. `include_files: false`
  restores the plain search. `list-objects-compact` still shows no file
  objects. Note that Anytype names file objects **without** their extension.

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
export ANYTYPE_LIVETEST_SPACE_ID=bafyrei...   # a space only this account is in
python3 livetest/run.py            # all suites
python3 livetest/run.py blocks     # matching suites only
```

The suites write into the space `ANYTYPE_LIVETEST_SPACE_ID` names and refuse
one with other members, who would see the test objects, homepage and widget
changes while a run lasts (`ANYTYPE_LIVETEST_SHARED_SPACE=1` overrides). The
space needs the library type Task. Everything they
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
138 — depends on `github.com/anyproto/anytype-heart`, licensed under the **Any
Source Available License 1.0**: Non-Commercial Use, or Commercial Use in
[Allowed Networks](https://networks.any.coop) only. A self-hosted any-sync
network is not an Allowed Network, so against one, only Non-Commercial Use is
licensed.

This source repository contains no anytype-heart code and only references it via
`go.mod`. **Compiled binaries and Docker images statically link it** and are
therefore subject to its terms — including that the linked anytype-heart code
stays under that license and cannot be redistributed as MIT. See `NOTICE`.
