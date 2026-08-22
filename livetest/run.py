#!/usr/bin/env python3
"""Run the live MCP suites against a running Anytype instance.

    python3 run.py              # every suite
    python3 run.py blocks query # only suites whose name contains one of these

Exits non-zero if any check fails, so it can gate a rebuild.

These tests mutate a real space. Everything they create is named with the
LIVETEST prefix and archived afterwards; the final pass also sweeps up anything
an earlier crashed run left behind.
"""

import importlib.util
import os
import pathlib
import sys
import time
import traceback

sys.path.insert(0, str(pathlib.Path(__file__).parent))

from harness import PROBE_PREFIX, Client, MCPError, Suite  # noqa: E402

SUITE_DIR = pathlib.Path(__file__).parent / "suites"


def load_suites(filters):
    for path in sorted(SUITE_DIR.glob("*.py")):
        if path.name.startswith("_"):
            continue
        name = path.stem.split("_", 1)[-1]
        if filters and not any(f.lower() in name.lower() for f in filters):
            continue
        spec = importlib.util.spec_from_file_location(f"suite_{name}", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        yield name, module


# Object types a suite must never erase even if one somehow ends up tracked.
# Date objects are the realistic hazard: object-date hands back a shared space
# fixture that merely looks like a normal object.
PROTECTED_TYPES = {"date", "participant", "spaceview", "profile"}

# Set ANYTYPE_LIVETEST_KEEP=1 to archive but not erase, e.g. to inspect what a
# failing suite produced.
KEEP = os.environ.get("ANYTYPE_LIVETEST_KEEP", "") not in ("", "0", "false")


def archive(client, space_id, object_ids):
    ids = [i for i in dict.fromkeys(object_ids) if i]
    for i in range(0, len(ids), 50):
        try:
            client.call(
                "object-set-archived",
                space_id=space_id,
                object_ids=ids[i : i + 50],
                archived=True,
            )
        except MCPError as exc:
            print(f"  ! cleanup failed for {len(ids[i:i+50])} objects: {exc}")


def purge(client, space_id, object_ids):
    """Erase, for good, the objects this run created.

    Permanent deletion is used here at the explicit request of the repository
    owner, so that test runs stop filling the bin. The safety rule is narrow and
    deliberate: ONLY ids collected during this run through Suite.page() and
    Suite.track() reach this function. Nothing discovered by searching, and
    nothing matched by name, is ever erased — a name could belong to something
    real. Objects are archived first (the tool only empties the bin), each one
    is read back to confirm it is not a protected system object, and whatever
    cannot be confirmed is left alone.
    """
    if KEEP:
        print("  (ANYTYPE_LIVETEST_KEEP set: objects archived, not erased)")
        return
    ids = [i for i in dict.fromkeys(object_ids) if i]
    if not ids:
        return

    erasable, protected = [], []
    for object_id in ids:
        try:
            obj = client.call(
                "get-object-compact", space_id=space_id,
                object_id=object_id, fields=["id", "name", "type"],
            )["object"]
        except MCPError:
            continue  # already gone; nothing to erase
        type_key = str((obj.get("type") or {}).get("key", "")).lower()
        if type_key in PROTECTED_TYPES:
            protected.append(f"{obj.get('name')} ({type_key})")
        else:
            erasable.append(object_id)

    if protected:
        print(f"  ! left alone, protected type: {', '.join(protected)}")
    for i in range(0, len(erasable), 50):
        batch = erasable[i : i + 50]
        try:
            client.call(
                "object-delete-permanently", space_id=space_id,
                object_ids=batch, confirm=True,
            )
        except MCPError as exc:
            print(f"  ! could not erase {len(batch)} object(s): {exc}")


def leftovers(client, space_id, object_ids):
    """Ids this run created that are still VISIBLE afterwards.

    Cleanup has to be checked the same way everything else here is checked: on
    the observed end state. purge() skips anything it cannot read back and
    reports batch failures without raising, so a run whose tracking is
    incomplete — or whose delete-* tool only archives — otherwise finishes green
    while the bin keeps filling up. Matching is by id, never by name, so this
    only ever looks at what the run itself made.

    Visibility, not readability. An erased property, tag, type or template
    vanishes from every listing but keeps answering get-object-compact with a
    tombstone, so probing by id called 24 successful deletions failures. What
    counts is whether the owner can still find the thing: the bin, the object
    list, and the two schema listings the object list does not cover.
    """
    wanted = {i for i in object_ids if i}
    if not wanted:
        return []
    visible = {
        o["object_id"]
        for o in client.call("list-archived", space_id=space_id, limit=5000)["objects"]
    }
    for tool, key in (
        ("list-objects-compact", "objects"),
        ("list-properties", "properties"),
        ("list-types-compact", "types"),
    ):
        result = client.call(tool, space_id=space_id, limit=1000)
        entries = result.get(key) or []
        total = (result.get("pagination") or {}).get("total")
        if total is not None and total > len(entries):
            print(f"  ! {tool} returned {len(entries)} of {total}; leftovers may be missed")
        visible |= {o["id"] for o in entries}
    return sorted(wanted & visible)


def sweep(client, space_id):
    """Archive leftovers from runs that died before their cleanup.

    Archive only. These are found by name, and a name match is not proof that
    an object came from a test, so they are never erased.
    """
    limit = 500
    hit = client.call(
        "search-space-compact", space_id=space_id, query=PROBE_PREFIX, limit=limit
    )
    found = hit.get("objects") or []
    stale = [
        o["id"] for o in found if str(o.get("name", "")).startswith(PROBE_PREFIX)
    ]
    if len(found) >= limit:
        print(f"  ! sweep hit its limit of {limit}; there may be more leftovers")
    if stale:
        archive(client, space_id, stale)
    return len(stale)


def bin_residue(client, space_id):
    """Test-named objects still sitting in the bin after a full run.

    This is the check that matches what the owner actually sees: the desktop
    Bin. It is deliberately name-based and deliberately read-only — a name match
    is good enough to report a leak, and nowhere near good enough to erase
    something. Its job is to catch objects a suite creates but never tracks,
    which the id-based guard cannot see by definition.
    """
    binned = client.call("list-archived", space_id=space_id, limit=5000)
    return [
        o for o in (binned.get("objects") or [])
        if str(o.get("name", "")).startswith(PROBE_PREFIX)
    ]


def main():
    filters = sys.argv[1:]
    client = Client()
    print(f"server  : {client.server_info['name']} {client.server_info['version']}")
    print(f"tools   : {len(client.tools)}")

    total_failures = []
    space_id = None
    bin_before = set()
    try:
        for name, module in load_suites(filters):
            suite = Suite(client, name)
            if space_id is None:
                bin_before = {o["object_id"] for o in bin_residue(client, suite.space_id)}
            space_id = suite.space_id
            print(f"\n── {name} " + "─" * (58 - len(name)))
            try:
                module.run(suite)
            except Exception:
                suite.check(f"{name}: suite raised", False, traceback.format_exc(limit=3))
            for label, ok, detail in suite.checks:
                mark = "  pass  " if ok else "  FAIL  "
                print(mark + label + (f"\n          {detail}" if not ok and detail else ""))
            total_failures += [(name, l, d) for l, d in suite.failures()]
            archive(client, suite.space_id, suite.created)
            # The erase tool checks each object is really in the bin, and that
            # check reads the REST index, which trails writes by about a second.
            # Without this pause it sees the objects as still live and refuses —
            # the guard doing its job, just too early.
            time.sleep(2.0)
            purge(client, suite.space_id, suite.created)
            if not KEEP:
                time.sleep(2.0)
                left = leftovers(client, suite.space_id, suite.created)
                if left:
                    detail = ", ".join(left[:8]) + ("…" if len(left) > 8 else "")
                    print(f"  FAIL  cleanup left {len(left)} object(s) behind\n"
                          f"          {detail}")
                    total_failures.append(
                        (name, f"cleanup left {len(left)} object(s) behind", detail)
                    )
                else:
                    print(f"  pass  cleanup erased all {len(set(suite.created))} created objects")

        if space_id:
            left = sweep(client, space_id)
            if left:
                print(f"\nswept {left} leftover objects from earlier runs")
            if not KEEP:
                time.sleep(2.0)
                # Only what THIS run added: the bin also holds leftovers from
                # older runs, and failing on those would blame every future run
                # for a mess it did not make.
                residue = [
                    o for o in bin_residue(client, space_id)
                    if o["object_id"] not in bin_before
                ]
                if residue:
                    names = sorted({str(o.get("name")) for o in residue})
                    detail = ", ".join(names[:8]) + ("…" if len(names) > 8 else "")
                    print(f"\nFAIL  this run left {len(residue)} test object(s) in the bin\n"
                          f"      {detail}\n"
                          f"      They are not erased automatically: a name match is not "
                          f"proof of origin. Track them in the suite that makes them.")
                    total_failures.append(
                        ("cleanup", f"{len(residue)} test object(s) left in the bin", detail)
                    )
                else:
                    scope = "this run" if not filters else "the suites that ran"
                    print(f"\n{scope} left nothing in the bin")
                if bin_before:
                    print(f"({len(bin_before)} test-named object(s) from earlier runs are "
                          f"still in the bin, untouched)")
    finally:
        client.close()

    print("\n" + "=" * 66)
    if total_failures:
        print(f"FAILED: {len(total_failures)} check(s)")
        for suite_name, label, detail in total_failures:
            print(f"  - [{suite_name}] {label}")
            if detail:
                print(f"      {detail}")
        return 1
    print("all checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
