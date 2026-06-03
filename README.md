# anytype-extension-mcp

Minimaler MCP-STDIO-Server fuer Anytype Dateioperationen ueber gRPC und kompakte REST-Wrapper fuer grosse Anytype API Antworten.

Der Server ist als Sidecar zum offiziellen Anytype MCP gedacht. Tool-Namen orientieren sich am offiziellen Schema und nutzen fuer kompakte REST-Wrapper das Suffix `-compact`.

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

## Umgebung

Der Server liest folgende Umgebungsvariablen:

- `ANYTYPE_GRPC_ADDR` (default: `dns:///127.0.0.1:31010`)
- `ANYTYPE_SESSION_TOKEN` (Token fuer die gRPC-Datei-Tools)
- `ANYTYPE_TIMEOUT` (default: `30s`)
- `ANYTYPE_API_BASE_URL` (default: `http://127.0.0.1:31012`)
- `ANYTYPE_API_KEY` (Pflicht fuer `*-compact` REST-Tools)
- `ANYTYPE_API_VERSION` (default: `2025-11-08`)
- `ANYTYPE_FILES_IN_ROOT` (default: `/data/in`)
- `ANYTYPE_FILES_OUT_ROOT` (default: `/data/out`)

### Session-Token

Die gRPC-Datei-Tools brauchen einen Session-Token. Reihenfolge:

1. `ANYTYPE_SESSION_TOKEN`, falls gesetzt (hat Vorrang).
2. Sonst der OS-Keyring (Service `anytype-cli`, User `session-token`), wie ihn
   `anytype-cli login` ablegt.

In Containern oder headless-Umgebungen ohne Secret-Service/DBus gibt es keinen
Keyring &ndash; dort `ANYTYPE_SESSION_TOKEN` setzen. Die REST-`*-compact`-Tools
nutzen stattdessen `ANYTYPE_API_KEY` und brauchen keinen Token.

## Tool: file-upload

Pflichtfelder:

- `space_id`
- `staged_path` (muss unter `ANYTYPE_FILES_IN_ROOT` liegen)

Optionale Felder:

- `type`: `file|image|video|audio|none`
- `style`: `auto|link|embed`

## Tool: file-download

Pflichtfelder:

- `object_id`

Optional:

- `target_name` (nur Dateiname, kein Pfad)

Download-Ziel ist immer `ANYTYPE_FILES_OUT_ROOT`.

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

## Lokal bauen (statisch)

```bash
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o ./bin/anytype-extension-mcp .
```

## Docker Build (statisch)

```bash
docker build -t anytype-extension-mcp:local .
```

## Lizenz

Eigener Code steht unter der MIT-Lizenz (siehe `LICENSE`).

Die gRPC-Datei-Tools haengen von `github.com/anyproto/anytype-heart` ab, das
unter der **Any Source Available License 1.0** steht (nur Non-Commercial bzw.
Allowed Networks). Das Quell-Repo referenziert anytype-heart nur ueber `go.mod`,
aber **kompilierte Binaries/Docker-Images** linken dessen Code und unterliegen
damit dessen Lizenzbedingungen. Details in `NOTICE`.
