"""Export, import, version history and files.

Import needs files staged in the shared input root, so this suite writes them
itself and removes them afterwards. Exports land in the shared output root as
files owned by the container user, which is why cleanup goes through the
Anytype-visible path rather than the host's.
"""

import os
import pathlib
import shutil

from harness import IN_ROOT


def run(s):
    obj = s.page("transfer")
    s.c.call(
        "block-paste", space_id=s.space_id, object_id=obj,
        markdown="# Kapitel\n\n- eins\n- zwei",
    )

    # --- export ------------------------------------------------------------
    exported = s.c.call("object-export", space_id=s.space_id, object_id=obj, format="markdown")
    s.check(
        "object-export returns the rendered markdown inline",
        "Kapitel" in exported["content"] and exported["length"] > 0,
        exported["content"][:120],
    )

    to_files = s.c.call(
        "object-export-files", space_id=s.space_id, object_ids=[obj],
        target_dir="livetest-export", format="markdown",
    )
    s.check(
        "object-export-files writes into the output root",
        (to_files.get("exported_count") or 0) >= 1 and to_files.get("entries"),
        to_files,
    )
    status, msg = s.c.try_call(
        "object-export-files", space_id=s.space_id, target_dir="../ausbruch"
    )
    s.check("an export path outside the output root is refused", status == "err", msg)

    # --- version history ---------------------------------------------------
    bid = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj, kind="text", text="FASSUNG-EINS"
    )["block_id"]
    s.settle(1.2)
    s.c.call(
        "block-set-text", space_id=s.space_id, object_id=obj, block_id=bid, text="FASSUNG-ZWEI"
    )
    s.settle()

    versions = s.c.call("object-versions", space_id=s.space_id, object_id=obj, limit=20)
    s.check("object-versions lists history", versions["count"] >= 2, versions["count"])

    # The oldest version predates the first block, so pick one that actually
    # contains the old text rather than assuming a position.
    target = None
    for version in versions["versions"]:
        blocks = s.c.call(
            "object-version-show", space_id=s.space_id, object_id=obj, version_id=version["id"]
        )["blocks"]
        if any(b.get("text") == "FASSUNG-EINS" for b in blocks):
            target = version["id"]
            break
    s.check("a version containing the earlier text can be read back", target is not None)

    if target:
        s.c.call(
            "object-version-restore", space_id=s.space_id, object_id=obj, version_id=target
        )
        s.settle()
        s.check(
            "restoring a version really reverts the content",
            s.block(obj, bid) and s.block(obj, bid)["text"] == "FASSUNG-EINS",
            s.block(obj, bid),
        )

    # --- import ------------------------------------------------------------
    staging = pathlib.Path(IN_ROOT) / "livetest-import"
    try:
        staging.mkdir(parents=True, exist_ok=True)
        (staging / "seite-a.md").write_text(
            "# LIVETEST Importseite A\n\nEin Absatz.\n\n- Punkt eins\n", encoding="utf-8"
        )
        (staging / "seite-b.md").write_text(
            "# LIVETEST Importseite B\n\n- [x] erledigt\n", encoding="utf-8"
        )

        # An import also builds a collection to hold what it read, and the tool
        # cannot report its id because anytype-heart discards the import result.
        # It is found afterwards by taking the ids that exist before and after
        # and keeping the difference — an object that did not exist a moment ago
        # AND carries heart's own container name. Both conditions, because a
        # name alone could belong to something the owner made.
        def import_containers():
            hit = s.c.call(
                "search-space-compact", space_id=s.space_id, query="Import", limit=200
            )
            return {
                o["id"] for o in (hit.get("objects") or [])
                if str(o.get("name", "")).startswith(("Markdown Import", "Protobuf Import"))
            }

        containers_before = import_containers()
        result = s.c.call(
            "object-import", space_id=s.space_id, type="markdown",
            paths=["livetest-import"], collection_title=s.probe("import"),
        )
        # Anytype throws the import result away, so a count would be invented.
        s.check(
            "import reports only that it started, never an invented count",
            result.get("started") is True and "objects_count" not in result,
            result,
        )
        s.settle(2.5)
        found = s.c.call(
            "search-space-compact", space_id=s.space_id, query="LIVETEST Importseite"
        )
        names = [o.get("name") for o in (found.get("objects") or [])]
        imported_ids = [
            o["id"] for o in (found.get("objects") or [])
            if "LIVETEST Importseite" in (o.get("name") or "")
        ]
        s.track(*imported_ids)
        new_containers = import_containers() - containers_before
        s.track(*new_containers)
        s.check(
            "the import leaves exactly one new collection behind, and it is tracked",
            len(new_containers) == 1,
            new_containers,
        )
        s.check("the imported pages exist afterwards", len(imported_ids) >= 2, names)
        if imported_ids:
            body = s.c.call(
                "object-export", space_id=s.space_id, object_id=imported_ids[0]
            )["content"]
            s.check(
                "imported markdown keeps its structure",
                "Punkt" in body or "erledigt" in body,
                body[:150],
            )

        status, msg = s.c.try_call(
            "object-import", space_id=s.space_id, type="markdown", paths=["gibtsnicht.md"]
        )
        s.check("a missing import path is refused", status == "err", msg)
        status, _ = s.c.try_call("object-import", space_id=s.space_id, type="markdown")
        s.check("an import without paths is refused", status == "err")
        status, _ = s.c.try_call("object-import", space_id=s.space_id, type="notion")
        s.check("a Notion import without a token is refused", status == "err")

        # --- file upload / download round trip -----------------------------
        probe_file = pathlib.Path(IN_ROOT) / "livetest-roundtrip.txt"
        probe_file.write_text("Roundtrip\n", encoding="utf-8")
        uploaded = s.c.call(
            "file-upload", space_id=s.space_id, staged_path="livetest-roundtrip.txt"
        )
        s.track(uploaded["object_id"])
        s.c.call(
            "file-download", object_id=uploaded["object_id"],
            target_name="livetest-roundtrip-out.txt",
        )
        listing = s.c.call("file-list-output")
        names = [e.get("relative_path") or e.get("name") for e in (listing.get("entries") or [])]
        s.check(
            "a file survives the upload/download round trip",
            any("livetest-roundtrip-out" in n for n in names),
            names,
        )
        probe_file.unlink(missing_ok=True)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
        # The export directory and the downloaded file belong to the container
        # user, so they need removing through a container too. Without this the
        # shared output directory grows with every run.
        s.remove_shared("out/livetest-export", "out/livetest-roundtrip-out.txt")
        leftovers = s.c.call("file-list-output").get("entries") or []
        names = [e.get("relative_path") or e.get("name") for e in leftovers]
        s.check(
            "the suite leaves nothing behind in the output directory",
            not [n for n in names if "livetest" in (n or "")],
            names,
        )
