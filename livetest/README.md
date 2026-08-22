# livetest

Live tests for `anytype-extension-mcp`. They drive the server through the very
launcher a client uses, against a **real** Anytype instance.

That is deliberate: practically every bug found in this project was a quirk of
anytype-heart that only shows up in operation — an RPC that reports success and
does nothing, a write delayed by three seconds, an ID that changes when an
object is moved. A mock would have found none of them.

## Running

```bash
export ANYTYPE_MCP_LAUNCHER=/path/to/launcher.sh
export ANYTYPE_LIVETEST_SHARED_ROOT=/path/to/shared/files

python3 livetest/run.py            # all suites
python3 livetest/run.py blocks     # matching suites only
```

The exit code is non-zero if any check fails, so a rebuild can be gated on it.

Prerequisites: a running Anytype instance and a launcher script that starts the
server on stdio with the right environment. Rebuild the server before running,
or you are testing the previous state.

### Environment

| Variable | Required | Meaning |
|---|---|---|
| `ANYTYPE_MCP_LAUNCHER` | yes | Script that starts `anytype-extension-mcp` on stdio |
| `ANYTYPE_LIVETEST_SHARED_ROOT` | yes | Host path of the directory shared with the Anytype server (mounted there as `/data`) |
| `ANYTYPE_LIVETEST_IN_ROOT` | no | Staging directory; defaults to `$ANYTYPE_LIVETEST_SHARED_ROOT/in` |
| `ANYTYPE_LIVETEST_KEEP` | no | `1` archives but does not delete — useful for inspecting a failed run |

The two required variables have no defaults on purpose. A default carries the
layout of whatever machine it was written on, and a wrong path fails deep inside
a suite rather than at startup.

## What the tests do to your data

They write into the **first space** of the instance. Everything they create is
named `LIVETEST …`, archived at the end and then **permanently deleted**, so the
bin does not grow with every run.

The selection for deletion is deliberately narrow:

- Only **IDs the run created itself** are deleted, collected through
  `Suite.page()` and `Suite.track()`.
- Anything found by **searching for the name prefix** — leftovers from aborted
  runs — is **only archived, never deleted**: a matching name is not proof that
  an object came from a test.
- Every object is read once more before deletion, and system types (`date`,
  `participant`, `spaceView`, `profile`) are skipped. Date objects are the
  realistic trap, because `object-date` returns a shared space object that looks
  like an ordinary one.

### The cleanup checks itself

Twice, because the two gaps look different:

- **`leftovers()`** compares the run's IDs against what is still **visible**
  after deletion: bin, object list, `list-properties`, `list-types-compact`.
  That catches tracked objects which did not disappear — `purge()` silently
  skips what it cannot read and reports batch errors without raising.
- **`bin_residue()`** then searches the bin for the name prefix, catching the
  other case: objects a suite creates and **never tracks**, which an ID check
  cannot see by definition. This check is read-only and counts only what **this**
  run added.

Visibility, not readability: a purged property still answers a direct
`get-property` even though it has vanished from every listing. A test written
against IDs once reported 24 successful deletions as a failure.

Anyone extending a suite has to track everything it creates — including
properties, tags, types, templates, and the collection an import creates as a
side effect. Exactly those five classes were missing and filled the bin over
weeks.

## One rule for new checks

**Assert on the observable end state, never on "the call returned without an
error."** In this project three out of four apparent failures were test
artifacts, and several real bugs would have passed an "ok": a tool that reported
the ID of a block that no longer existed; an import that reported success and
imported nothing. So: read the object back, compare the text, look for the file
on disk.

`Suite.check(label, condition, detail)` collects the results; `detail` is printed
only on failure, which is where the observed value belongs so that a failure is
understandable without another run.

## Suites

| File | Covers |
|---|---|
| `01_blocks.py` | Creating and editing blocks, marks, paste, split/merge, move and duplicate IDs |
| `02_tables.py` | Grids, cells, rows and columns, headers, sorting, error messages |
| `03_queries.py` | Source, views, filters, sorts, column visibility, manual order, collections |
| `04_objects.py` | Archive roundtrip, undo/redo, duplicate, templates, bulk properties, schema |
| `05_transfer.py` | Export, import, version history, file roundtrip |
| `06_extras.py` | Relation blocks, apply template, extract blocks, bookmarks, link appearance, layout, LaTeX, graph, Unsplash |
| `07_spaceops.py` | Homepage, flags, dates, permanent deletion, version diff, file usage, type layout |
| `08_cover_batch.py` | Cover from file, image icon, batch processing of the schema tools |
| `09_usage.py` | Usage analysis and tag cleanup, including both dangerous cases |
| `10_attachment.py` | The attachment route: staging, upload, cover, and its error paths |
