# Verified anytype-heart behaviour

Notes on how anytype-heart actually behaves, as opposed to how its API reads.
Every entry here was established empirically — against a running instance, or by
following the call through heart's source — and most of them explain why a tool
in this server does something that looks roundabout.

They are recorded because they are expensive to rediscover. A returned success
that changed nothing, a filter that matches everything, a write that is
invisible to the next read: none of these announce themselves.

Unless stated otherwise, findings were verified against **anytype-heart
v0.50.8**. Paths in `code font` refer to files in the anytype-heart repository.

---

## Views and dataviews

### `BlockDataviewViewUpdate` writes metadata only

It **ignores** filters, sorts and relations passed along with the view.
`query-view-update` therefore reconciles those afterwards through the dedicated
RPCs: lists that are supplied replace the stored ones, lists that are omitted
stay untouched.

### View metadata is a full replace, and that used to destroy data

`SetViewFields` (`core/block/simple/dataview/dataview.go`) assigns *every*
simple field of the view it is handed, unconditionally. There is no field mask.

A view built from optional arguments therefore wrote zero values over stored
ones. Without `name` the view was left called `""` — the GUI shows "Untitled".
Without `layout` it became a table, because layout 0 is Table, and a kanban board
lost its grouping in the same call. No error was reported for either.

The fix has to patch **the stored model**, not a struct rebuilt from the tool's
own view type. Six fields have no equivalent in that type — `cover_fit`,
`group_background_colors`, `end_relation_key`, `wrap_content`, `list_size`,
`alternate_rows` — and would be nulled on *every* update regardless of what the
caller sent. `UpdateView` reads the stored view first and overwrites only fields
the caller actually supplied, tracked by the *presence* of the argument rather
than its value. When only filters, sorts or columns change, the metadata call is
skipped entirely.

### A column without an explicit width keeps its old one

Same root cause. `BlockDataviewViewRelationReplace` replaces the whole relation
entry and stores a `0` literally.
`syncViewRelationsAndRelationLinks` inserts `DefaultViewRelationWidth` only for
*missing* relations, so it never repaired a stored zero. Every GUI column width
was lost by a call that merely reordered columns.

### Columns are shown and hidden, not added and removed

A view holds an entry for *every* property the dataview knows; `IsVisible`
decides whether it becomes a column. Removal does not stick: heart runs
`syncViewRelationsAndRelationLinks` after each change and immediately re-inserts
missing relations with `IsVisible=false`. Verified — after removing five of
seven, all seven were still present.

`query-view-update` therefore shows the requested columns, hides the rest, and
sorts afterwards. `query-inspect` lists all relations; the `visible` field says
which are really columns.

### A visible column also needs an entry in the block's `RelationLinks`

`syncViewRelationsAndRelationLinks` runs in one direction only — it carries the
block's `RelationLinks` into the view, never a view relation back into the block
— and `BlockDataviewViewRelationReplace` does not touch `RelationLinks` at all.

A view then lists the property as `visible` while the block does not know it and
the client has no schema for it: the column is simply not rendered. Measured
live — two dataviews with six visible columns each carried only `name` and the
five system properties in `relationLinks`.

`query-view-create` and `query-view-update` therefore register columns, grouping
and cover property on the block first, via `BlockDataviewRelationAdd`. That call
is idempotent (`RelationLinks.Has(key)`), so it repairs existing queries on their
next update.

### A query's source is not in the dataview block

The block stays empty; the source lives in the object's `setOf` detail.
`query-inspect` reads it from there so that an empty block is not mistaken for
"no source".

### `query-set-source` rebuilds the dataview

`SetSourceInSet` leaves the views standing but re-derives every view relation
from the type through `MakeDataviewContent` — column widths and visibilities fall
back to defaults — and clears `DefaultObjectTypeId` and `DefaultTemplateId`.

`SetQuerySource` reads those two defaults beforehand and writes them back after.
Set the source first, then configure views.

### Manual view order is presentation state only

anytype-heart **never** evaluates `object_orders` when running a query — no
occurrence in `pkg/lib/database` or `core/subscription`. The Anytype clients
render it; server-side reads such as `get-list-objects-compact` still return the
view's sort order. Use `query-inspect` to check it.

### Kanban groups by `status`, `tag` or `checkbox` only

`core/kanban/service.go` registers exactly those three groupers. Anything else
yields "unsupported relation format", the group list stays empty, and the client
draws a board with no columns — while the view itself saves without complaint.

`query-view-create` and `query-view-update` check the grouping property's format
beforehand, against the view *as it would be stored*, so that switching to kanban
without touching the grouping is caught too.

### Blocks are found by `block_id`, not "the first dataview"

A page can embed several queries. An implementation that always took the first
dataview would read from one block and write into another within the same tool
call. `query-inspect` lists all of them under `dataview_blocks` and warns when
there is more than one.

---

## Embedded queries

### An embed is half reference, half copy

`CopyDataviewToBlock` assigns the target's `Views`, `RelationLinks`,
`GroupOrders` and `ObjectOrders`, leaving only `TargetObjectId` as a reference.

*Which* objects appear therefore still follows the query; the *presentation* is a
snapshot taken at embed time and drifts afterwards. The RPC does not require a
fresh block, which is why `block-embed-query` accepts a `block_id`: calling it
again on the same block copies the query's current configuration over it and
resynchronises the two sides.

`active_view` is not carried across, neither on embed nor on refresh. Use
`query-view-arrange(set_active=true)` on the page's own block.

### An embed's `Source` is empty on purpose

For an inline set, `TargetObjectId` *is* the source. Reading the *page's* `setOf`
as a substitute answered `null` and made a healthy embed look broken.
`InspectDataview` now follows the target (`source_from`) and reports
`target_object_id` alongside — as does `block-list`.

### Creating an embed takes two steps

`BlockDataviewCreateFromExistingObject` only fills a block that is **already** a
dataview ("block must contain dataView content"). So an empty dataview block is
created first and configured after. `block-relation-add` follows the same
pattern.

---

## Filters and sorts

### A filter `operator` is a group marker, not a conjunction

In `MakeFilter` (`pkg/lib/database/filter.go`): with `Operator_No`,
`RelationKey`, `Condition` and `Value` count. With anything else they are
**ignored entirely** and the filter is built from `NestedFilters`.

A leaf filter carrying `"and"` therefore loses its predicate and becomes an empty
group. An empty AND matches everything — both `FiltersAnd.FilterObject` and
any-store's `query.And{}`. An empty OR is worse than inconsistent: heart answers
`true`, any-store `false`. Either way the filter constrains nothing while the
write reports success.

Anytype always joins a view's filters with AND. The field is gone from the
schemas; a value supplied anyway is rejected with a reason. `query-inspect`
reports already-stored filters that carry an operator under `warnings`.

### "This Object" is a magic string, not a missing feature

The Anytype apps offer "This Object" as a filter value and store it as the
literal string `_filter_template_1_`. Measured by building the filter in the app
and reading the object back: an inline query on tasks with Project = This Object
stores `{relationKey: linkedProjects, condition: In, value:
["_filter_template_1_"], format: object}`. Writing the same string through
`query-view-update` produces a **byte-identical** filter.

This is what a template needs: a project template with a task query on
`Project = _filter_template_1_` works for every project created from it, whereas
a real object ID would pin every project to one.

Two caveats. It is an **undocumented client constant** — heart does not know the
string (no match in v0.50.8) — so it can change upstream. And it is resolved
**only at render time**: server-side it is an ordinary string, so
`get-list-objects-compact` on such a view returns 0 results (measured). That is
the correct outcome of a server-side read, not a broken filter; it can only be
cross-checked in the app.

### Filter enums are readable as well as writable

The read paths used to emit protobuf names — `NotEqual` became `notequal` —
while the write side only accepted `not_equal`. A filter list read with
`query-inspect` could therefore not be written back with `query-view-update`.

`filterFromModel` and `sortFromModel` now report the canonical spelling; the old
protobuf names (`notequal`, `notempty`, `notin`, `greaterorequal`, `allin`,
`notspecified`, …) are still accepted as input so stored calls keep working.

### Filter formats: the write enums were too narrow

A dataview stores its own format vocabulary — `object`, `status`, `longtext`,
`tag` — where `list-properties` says `objects`, `select`, `text`,
`multi_select`, and the read path reports what is stored. The `format` enum in
the filter and sort schemas offered only the REST names: **eight** of the
fourteen values a read can produce were forbidden there, `emoji` and `relations`
included.

At runtime both always worked. But a schema-validating client — OpenAI strict
function calling, for instance — could not write back a filter it had just read.
The schemas now derive from `relationFormats`, so they cannot go stale.

`create-property` and the type property links deliberately keep the REST names:
they talk to the REST API, which accepts only its own vocabulary.

`text` and `longtext` are **not** folded together. They are different formats,
short and long, and collapsing them would lose information.

### `get-list-views-compact` returns filter values lossily

Not the wrapper's doing: the REST endpoint types `value` as a string. A checkbox
filter comes back as `""` instead of `false`, an object filter as `""` instead of
its ID list — verified with raw curl against the same view. Conditions and
layouts are in the REST dialect there as well (`eq`, `ne`, `nempty`, `grid`).

When the values matter, use `query-inspect`.

---

## Properties and relation keys

### A property has two names, and the wrong one fails silently

The REST API reports `due_date` and `linked_projects`. The index — and **every**
dataview filter, sort and column — uses the internal relation key `dueDate` and
`linkedProjects`.

For many properties the two are identical (`done`, `name`, `tag`), which is why
it is easy to miss. A filter in the REST spelling is accepted, stored, and
reported back unchanged — and then matches **nothing**. Verified: a filter on
`linked_projects` returned 0 rows, the same filter on `linkedProjects` returned
the expected task. For a sort there is no visible symptom at all.

The server translates both spellings, and a key that exists in neither spelling
is an **error** rather than a silent miss — with a suggestion when it was only
the spelling.

### The two names can be completely unrelated

A property created through the API or in the app gets a generated key such as
`6a87593ad7f55319fc7b1d73`, while REST reports the transliterated name as
`apiObjectKey`: the property "Person" is `person` over REST and something else
entirely underneath (`objectcreator/relation.go` and `util.go`).

The internal key **cannot be looked up over REST at all**: `apimodel.Property`
carries it as `RelationKey` with `json:"-"`.

This made `objects-modify-property` unusable for any custom property.
`ObjectListModifyDetailValues` validates the key against the space index, and the
whole call fails with `failed to validate relation: object not found in space
index` — for `set` as much as for `add`, with an empty value as much as a full
one. Suspicion first fell on the empty array, because
`update-objects-compact-many` could make the same change; but the REST layer
translates beforehand (`ResolvePropertyApiKey`). Reproduced: with
`property_key = 6a87593ad7f55319fc7b1d73`, all four of `set`, `set []`, `add` and
`remove` ran without error.

Tools outside the dataview world translate too: `objects-modify-property`,
`block-relation-add`, `block-link-appearance`, and `schema-set-order` with
`kind=tags`. `type-set-featured-properties` was never affected — it takes
`bafyrei` IDs.

### Translation must not be stricter than Anytype itself

A bundle relation exists in **every** space, even when the space index holds no
object for it. `FetchRelationByKey` asks the bundle first and the index second.
Without the same fallback, `url` or `starred` would appear missing in a space
that has never used them, although Anytype accepts them.

The asymmetry decides the design: a missed typo only restores the old state, a
false alarm breaks a working call.

### Both spellings are reported back

Where the internal spelling differs from the one `list-properties` reports,
`query-inspect` puts the public one alongside as `api_property_key` —
`6a83275740bea100015faae7` → `assigned_to`, `dueDate` → `due_date`. When the
field is absent, the two are the same.

A hex-looking key is **not** automatically the internal one: in one measured
space, 49 of 103 properties carried a generated ID as their public key.

### `update-type.properties` and `create-type.properties` replace, they do not merge

`buildUpdatedTypeDetails` (`core/api/service/type.go`) rewrites
`recommendedRelations` wholesale; anything missing from the list is unlinked.
Only the *featured* properties live in a different relation and survive.

And a key that does not exist is not rejected but **created as a new property**
by `buildRelationIds`: a typo makes the schema grow. On `update-type` that hits
twice — `due_dat` fails to link `due_date` *and* leaves a "Due Dat" property
behind in the space.

`update-type` therefore validates keys up front and rejects unknown ones;
`allow_new_properties=true` disables the check. `create-type` is unchanged,
because auto-creation is the point there (`Recipe` with `cooking_time` and
`calories` in one call).

Validation goes through gRPC, not `list-properties`: no REST endpoint reports the
internal spelling, so a REST-based check would reject valid calls. Membership is
deliberately at least as wide as heart's own (`cache_manager.go` keys by `Id`,
`RelationKey` **and** `Key`): both spellings, the object ID, and the bundle
relations.

### Exactly one typed value per property link

`PropertyLinkWithValue.UnmarshalJSON` (`core/api/model/property.go`) is a
first-match switch over `text, number, select, multi_select, date, files,
checkbox, url, email, phone, objects`. Two value fields are **not** rejected:
heart takes the first, discards the rest **without an error**, and answers 200.

The loud case — no value field at all → 400 — is the harmless one. The quiet case
is silent data loss, which is why this server validates property links before
sending them.

### `objects-modify-property` accepts more than one operation at a time

`ModifyDetailsList` (`core/block/detailservice/service.go`) gives every
combination a defined meaning: a non-nil `Set` wins and skips `Add`/`Remove`,
otherwise `Add` is applied first and `Remove` second.

`add` + `remove` in one operation is therefore a **working tag swap in a single
step**, not an error. A `oneOf` in the schema would have removed a feature.

---

## Blocks and text

### `BlockTextSetText` does not write immediately

anytype-heart holds the change for three seconds and commits it only when another
block is edited or the object is closed. Without a flush, a write is invisible to
the very next read — exactly what a tool server must never return.

`SetBlockText` and the table batches therefore close the object afterwards via
`ObjectClose`, the same action the GUI triggers when leaving a page.

### `block-paste` into `title` renames the object

It inserts no blocks. A page called "ORIGINALNAME" was called
"PastedORIGINALNAME" afterwards — silent damage to a field the caller did not
mean. `title` and `description` are therefore rejected.

Pasting into a code block or a table cell inserts the text literally on purpose,
without markdown parsing.

### A cross-object `block-move` assigns new block IDs

Within one object the blocks keep their IDs; moved into another object they are
created anew there. Verified: `…879` became `…87a`.

`block-move` therefore returns the new IDs as `moved_block_ids` and the old ones
as `previous_block_ids`. `block-duplicate` reports `new_block_ids` for the same
reason — Anytype names created IDs in no response, so they are determined by a
before/after comparison.

### `BlockLinkListSetAppearance` assigns all four settings unconditionally

heart's `link.SetAppearance` writes `CardStyle`, `IconSize`, `Description` and
`Relations` every time. A call with only `icon_size` used to reset the card style
to `text` and empty the property list.

`block-link-appearance` reads the blocks first (`linkContents`) and overwrites
only what was supplied. So the state can be seen at all, `block-list` now reports
`card_style`, `icon_size`, `description` and `property_keys` for link blocks.

### `block-extract-to-object` creates one object per selection root

Not one object for the whole selection (`SelectRoots`). Two sibling blocks give
two objects; one block with nested children gives one.

### Page columns exist through `position`, but the API hid them

`Block_Left` / `Block_Right` make heart build the row/column structure
automatically (`InsertTo` → `moveFromSide` → `wrapToRow`), and the position parser
could always read both values. But `BlockPositionNames()` did not list them — and
that list is the `enum` in the JSON schema. A caller validating arguments against
the schema, which all of them do, could not even send the value. The capability
was there; the API surface concealed it.

Two embeds side by side are therefore `block-move` with `position="right"` and
`drop_target_id` set to the other block. `left`/`right` need a target: without
`target_id`, heart falls back to `inner` and the block lands at the end of the
page instead of beside it.

### Column widths are a share of the row

Anytype stores them as a `width` field on the column block.
`BlockListSetFields` **replaces** a block's fields wholesale
(`b.Model().Fields = fr.Fields` in `basic.SetFields`), so the existing state is
read and merged first.

Supplied shares are normalised (`[2, 1]` == `[0.667, 0.333]`); all zeros reset to
equal widths, which is what heart itself writes when a row's column count changes
(`normalizeLayoutRow`). Any block *in* the row serves as the reference; the row is
found upwards from there, because a caller knows its own blocks, not the layout
scaffolding around them.

`block-list` reports `width` on every column block — when absent, the columns
share the row equally, which is exactly how Anytype stores that case.

### Only `Move` checks whether the drop target can have children

`canHaveChildren` (`core/block/editor/basic/basic.go:613`) allows exactly ten
text styles: `paragraph`, `quote`, `checkbox`, `marked`, `numbered`, `toggle`,
`callout` and `toggleHeader1/2/3`. Headings (`header1`–`header4`), `code`,
`title` and `description` are not among them, so `block-move` with
`position=inner` or `inner_first` onto one of those fails with *cannot move to
block that cannot have children*. The check runs only when the target is a text
block; a non-text target is never tested.

**`block-create` does not apply the same rule.** `CreateBlock` (`basic.go:133`)
goes straight to `state.InsertTo` (`core/block/editor/state/position.go:18`),
which has no such guard — and neither does `Duplicate`. Measured against a live
instance: creating a block with `position=inner` on a `header3` succeeds, the
child appears in the heading's `children_ids`, is still there after the write
settles and a fresh read, and can be moved back out afterwards, leaving the
heading childless again. So the nesting is real and persistent, not silently
normalised away; heart simply enforces the rule in one of the two paths.

Because heart's message names neither the styles that would work nor a way out,
`block-move` vets an inner drop target itself before the RPC and answers with
both. `block-move` must **not** paper the asymmetry over by falling back to
create+delete: it
promises that block ids survive a move within one object, and a fallback would
break that promise silently — the caller would get `moved: true` and dead ids.
What it offers instead is the opt-in `convert_target_to_toggle`, which does what
a user does by hand: turn `header1/2/3` into the matching `toggle_header*` and
then move for real, so every id stays. `header4` has no counterpart (Anytype has
no `toggle_header4`) and is refused with that reason rather than converted to
something else.

### `block-list` reports styles under the schema's names, not protobuf's

`BlockContentTextStyle.String()` spells a bulleted list `Marked` and an
underline mark `Underscored`. Those names are not in `TextStyleNames()` /
`MarkTypeNames()`, so a client that validates arguments against the tool schema —
all of them do — could not pass back a value it had just read. `blockFromModel`
therefore maps through `textStyleNames` / `markTypeNames`, and `block-turn-into`
and `block-mark` answer with the resolved name rather than the alias the caller
sent. A turn-into that reported `bulleted` while the readback said `marked` was
reported as a write that did nothing; it had worked all along.

The reverse maps are written out instead of reversing the parse tables with
`enumName`: those tables hold aliases on the same value (`bulleted`/`marked`,
`checkbox`/`todo`, `code`/`keyboard`, `underline`/`underscored`), and Go's map
iteration order would pick between them differently from run to run.

`title` is the one style reported but not accepted: it is the object's name
block, not a style to convert a paragraph into.

---

## Tables

### `BlockTableRowListClean` deletes no content

Despite the name, it only removes cell blocks that are already empty.
`table-row-clear` therefore writes explicitly empty text into every cell.

### An intact table can have holes

Cell blocks come into existence on first write, and anytype-heart removes emptied
cells again when the object is closed.

`table-inspect` still returns the complete `row_count × column_count` grid;
missing cells carry `"exists": false` and the ID they will receive on the next
write.

---

## Types and layout

### Full width is per type, not per object

The three switches in the type editor are hidden details on the type object:
`layoutWidth` (full width), `layoutAlign` (header position) and
`headerRelationsLayout` (properties as a row or a list — heart calls the second
value "column" in its own description while the app shows "List"; the tools use
the app's wording and accept both). All are `format: number`, all
`ReadOnly: false`, all `Hidden: true`. The last is why they never appear over
REST and `update-type` does not know them.

heart evaluates them **nowhere**: the only place in the whole repository that
mentions `RelationKeyLayoutWidth` is the publish whitelist, and there it sits in
`objectTypeWhiteList`, not `documentRelationsWhiteList`. A single page cannot
have its own width; wanting *one* page wide means giving it its own type.

The values are read and drawn by the clients, so the numbers are a convention of
the apps, not of the protocol. `type-set-layout` translates names to numbers in
exactly one place and reports the state before and after — without a readback a
caller would have no way to see what they replaced, since no listing shows these
relations.

### `ObjectSetLayout` is deliberately not offered

The RPC is fully implemented but does not do what its name says.
`SetLayoutInState` reads the layout through `state.Layout()`, which returns the
**derived** layout from the **type** (`RelationKeyResolvedLayout`). Then
`layoutConverter.Convert` runs, and the converter writes no layout anywhere —
`converter/layout.go` contains no assignment to `RelationKeyLayout` or
`RelationKeyResolvedLayout`, it only restructures (add a title, add the `done`
relation for todo, bookmark blocks). The object's own layout relation is even set
to `Null` in `details.go`. On top of that, `isConversionAllowed` permits only
page-like layouts among themselves plus Set→Collection, so Basic→Collection/Set
simply fails.

An object's layout follows its **type**. Change it with `update-object-compact`
and `type_key` — verified: Page→Task moved the reported layout from `basic` to
`action`.

### Featured properties are a type setting

`ObjectRelationAddFeatured` supports only the `description` property ("only
description relation is supported"). Hence `type-set-featured-properties` using
`ObjectTypeRecommendedFeaturedRelationsSet`, rather than an object-level tool.

### `delete-property`, `delete-tag` and `delete-type` archive, they do not delete

The effect is visible only through the listing tools: after roughly a second the
entry disappears from `list-properties` / `list-tags` / `list-types-compact`,
while `get-property` / `get-tag` / `get-type-compact` still serve it.

Only types carry `archived: true` in that response. For tags and properties, a
deleted object is **indistinguishable** from a live one in the `get-` response.
Always check with the listing tool.

### Archived schema objects can be brought back

`object-set-archived` with `archived=false` undoes it, and it takes any object
ID, properties, tags and types included. In heart, archiving is a plain
collection-add on the space's archive object
(`core/block/detailservice/set_details.go`, `setIsArchivedForObjects`) with no
branch by object kind; only the bundled system types and relations are excluded,
and `checkArchivedRestriction` checks nothing at all when un-archiving.

Verified end to end: `create-property` → `delete-property` (gone from
`list-properties`) → `object-set-archived archived=false` → back.

---

## Search, the index, and the bin

These three traps are all silent — they return "nothing found", which is
indistinguishable from "not used". Together they nearly made the usage analysis
delete things that were in use, three times.

### `Limit` is not optional

Without a limit, `ObjectSearch` returns **zero** records rather than all of them.
Every page states its size explicitly, and `NeedTotal` is checked against the
collected set; a mismatch aborts the scan with an error instead of returning a
short result.

### `ObjectSearch` silently appends `isArchived != true`

Whenever the caller supplies no archive filter of their own
(`injectDefaultFilters` in `pkg/lib/database`). A tag used by a single object in
the bin would look unused. The scan therefore runs twice, with opposite archive
filters.

### The filter key is not the REST key

REST names a property `prb_usage_prop`; the index stores its values under an
internal relation key such as `6a779fce40bea1000164ea39`, found in the
`relationKey` detail of the property object. A search with the REST key matches
**nothing**.

The layout is called `resolvedLayout` in the index, not `layout`.

### A reference can live in a filter rather than an object

A query can carry an option in its **filter**. There the search is by option ID
rather than relation key — which key hangs off a filter varies by whoever created
it, whereas the IDs are unambiguous.

### The bin is not listable over REST

`isArchived` is on the list of never-exposed relations, so the search endpoints
cannot filter on it and silently return live objects instead. `list-archived`
goes through gRPC.

Each entry names the member who created the object as `created_by` — the same
column the desktop shows in the bin. anytype-heart derives it from the identity
that signed the object's first change (`treeSource.GetCreationInfo`), so it
distinguishes accounts reliably in a shared space. This matters because an MCP
server has an identity of its own: its objects do not belong to whichever account
is looking at the GUI.

### `clean-unused-tags` and the bin

Without `ignore_bin`, the bin pins options permanently: anything once used by a
since-deleted object could never be cleaned up. The dry run reports such options
separately under `only_referenced_from_bin`, so the consequence is visible before
the switch is set. Filters of **live** queries always block.

---

## Files, covers and import/export

### The REST API has no concept of a cover

Its `UpdateObjectRequest` carries name, icon, type, properties and markdown, and
its `Object` model has no cover field — a cover can be neither written nor read
there. Anytype stores it in the hidden details `coverId` and `coverType`, where
`coverType: 1` means `coverId` is a file.

The cover tools write exactly those two details and read them back through
`ObjectShow`. `get-object-compact` can never show a cover, however it is asked.

### Icons are three relations pretending to be one field

An icon is stored in `iconEmoji`, `iconImage`, or the `iconName`/`iconOption`
pair, and nothing keeps them exclusive. `processIconFields`
(`core/api/service/object.go`) writes only the relation the requested format
needs and never clears the others; `getIcon` (`core/api/service/icon.go`)
resolves them in a fixed order: `iconName`, then `iconEmoji`, then `iconImage`.

So a file icon written over an existing emoji is stored and invisible — the
emoji keeps winning, `PATCH` answers 200 and the response shows the old icon.
The opposite direction looks fine and is not: the emoji shows, the file icon
stays underneath, and it reappears the moment the emoji is removed.

Two things follow that are not guessable:

- `"icon": null` is a no-op, not a removal. `buildUpdatedObjectDetails` tests
  `request.Icon != nil`, so null means "field not sent". Clearing an emoji is
  `{"format": "emoji", "emoji": ""}`; the REST API refuses the equivalent for
  the other two (`400 invalid icon name`, `400 icon file is not valid`), so
  only gRPC `ObjectSetDetails` can empty those.
- **Types never render a file icon.** `getIcon` is called for a type with a
  hardcoded empty image (`core/api/service/type.go`), so `iconImage` on a type
  is stored and unreadable no matter what else is cleared.

The icon tools therefore write through REST — which is what validates a file id
against the space via `isValidFileReference` — and then clear the competing
relations over gRPC. See `anytypefiles/icons.go` and `icon_apply.go`.

### `BlockFileSetName` is an empty stub

In v0.50.8 it reports success and changes nothing. There is deliberately no
`block-file-set-name`; rename through the file object instead
(`target_object_id` from `block-list`) with `update-object-compact`.

### A failed download stays on `uploading` forever

`block-file-create` with a `url` whose download fails — 404, 403, hotlink
protection — remains at `file_state: uploading` and does **not** move to `error`.
A status that never changes is a failed download.

### `ObjectImport` reports nothing at all

The middleware discards the result (`return &pb.RpcObjectImportResponse{}`) and
imports asynchronously. Object count, collection ID and even the error field are
always empty. A successful call means "accepted", not "worked". `object-import`
says so explicitly and invents no numbers; verify with `search-space-compact`.

### Exported files belong to the container user

Files written by `object-export-files` are owned by root inside the container,
not by the host user. Cleaning them up on the host needs matching permissions.

---

## Unsplash

heart has its own Unsplash support, and it **cannot** be used with an ordinary
API key:

- heart builds its client with `oauth2.StaticTokenSource`, sending
  `Authorization: Bearer`. Unsplash rejects access keys in that form; they have
  to arrive as `Authorization: Client-ID`. Same key, same endpoint: Bearer gives
  401, Client-ID gives results.
- A bearer token can be minted from the key pair via `client_credentials`, but
  Unsplash issues those with `expires_in: 1800`. heart reads `UNSPLASH_KEY` once
  in `init()`, so a static environment value would work for half an hour and then
  begin failing silently.
- heart routes through Anytype's own proxy `unsplash.anytype.io`.

This server therefore talks to `api.unsplash.com` directly. All it needs is
`UNSPLASH_ACCESS_KEY` **in its own environment**; nothing changes on the Anytype
server. Image import uses the same staging path as `file-upload`: download, place
in the input directory, upload, remove the file.

`unsplash-download` also calls Unsplash's `download_location` and returns the
attribution — their terms require both once an image is actually used.

The image needs CA certificates for this; `scratch` ships none. They are copied
from the build stage in the Dockerfile, or TLS fails with "certificate signed by
unknown authority".

---

## RPCs deliberately not offered

### `ObjectRelationListAvailable`

The RPC exists, but `ListAvailableRelations` in `core/block/editor.go` is a
"TODO: not implemented" that always returns nil — confirmed live for Page and
Task. Which properties suit an object is answered by `get-type-compact`
(properties of the type) and `list-properties` (everything in the space).

### `HistoryDiffVersions`

`object-version-diff` compares **two snapshots** instead. The RPC answers with
raw `EventMessage`s — heart's internal event union with over a hundred variants,
several of which would have to be interpreted before anything could be said. Two
`ShowVersion` calls give the same answer in terms the caller already knows:
blocks added, removed, or changed in text.
