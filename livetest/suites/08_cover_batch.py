"""Covers, image icons and batched schema operations.

Three things that looked impossible from the outside and turned out to be
either undocumented or unreadable rather than missing — see the README.
"""

import os
import pathlib
import struct
import zlib

from harness import IN_ROOT


def write_png(path, size=48, rgb=(90, 140, 200)):
    """A tiny valid PNG, so the suite needs no binary fixture in the repo."""
    raw = b"".join(b"\x00" + bytes(rgb) * size for _ in range(size))

    def chunk(tag, data):
        body = tag + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

    path.write_bytes(
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 2, 0, 0, 0))
        + chunk(b"IDAT", zlib.compress(raw))
        + chunk(b"IEND", b"")
    )


def run(s):
    obj = s.page("cover")
    s.c.call("block-paste", space_id=s.space_id, object_id=obj, markdown="# Inhalt\n\n- bleibt")
    blocks_before = len(s.blocks(obj))

    # A name with a space and an umlaut, because that is what broke for the user.
    staged = pathlib.Path(IN_ROOT) / "livetest cover bild.png"
    try:
        write_png(staged)

        # --- cover from a local file, in one step -------------------------
        result = s.c.call(
            "object-set-cover-from-file", space_id=s.space_id,
            object_id=obj, staged_path="livetest cover bild.png",
        )
        image_id = result.get("image_object_id")
        s.track(image_id)
        s.check(
            "a local image becomes the cover and the image object id is returned",
            result.get("cover_set") is True and image_id,
            result,
        )
        s.check(
            "the cover reads back as the uploaded image",
            (result.get("cover") or {}).get("image_object_id") == image_id,
            result.get("cover"),
        )
        # The whole point: a cover, not a block.
        s.check(
            "setting a cover adds no block to the page",
            len(s.blocks(obj)) == blocks_before,
            f"{blocks_before} -> {len(s.blocks(obj))}",
        )
        s.check(
            "the body is untouched",
            [t for _, t in s.body_text(obj)] == ["Inhalt", "bleibt"],
            s.body_text(obj),
        )

        # --- reading it back separately ------------------------------------
        read = s.c.call("object-get-cover", space_id=s.space_id, object_id=obj)
        s.check(
            "object-get-cover reports the cover",
            read["cover"].get("image_object_id") == image_id and read["cover"]["is_set"] is True,
            read["cover"],
        )

        # --- replacing and removing ----------------------------------------
        second = s.c.call(
            "file-upload", space_id=s.space_id,
            staged_path="livetest cover bild.png", type="image",
        )["object_id"]
        s.track(second)
        replaced = s.c.call(
            "object-set-cover", space_id=s.space_id, object_id=obj, image_object_id=second
        )
        s.check(
            "an existing cover is replaced",
            replaced["cover"].get("image_object_id") == second,
            replaced["cover"],
        )
        s.check(
            "the previous image object still exists after being replaced",
            s.c.call("get-object-compact", space_id=s.space_id,
                     object_id=image_id, fields=["id"])["object"]["id"] == image_id,
        )
        cleared = s.c.call(
            "object-set-cover", space_id=s.space_id, object_id=obj, image_object_id=""
        )
        s.check(
            "an empty id removes the cover",
            cleared["cover"].get("is_set") is False,
            cleared["cover"],
        )

        # --- an image as the object's icon ---------------------------------
        s.c.call(
            "update-object-compact", space_id=s.space_id, object_id=obj,
            icon={"format": "file", "file": image_id},
        )
        s.settle()
        icon = s.c.call(
            "get-object-compact", space_id=s.space_id, object_id=obj,
            fields=["id", "icon"], include_icon=True,
        )["object"].get("icon") or {}
        s.check(
            "an uploaded image can be used as the object icon",
            icon.get("format") == "file",
            icon,
        )

        # --- refusals -------------------------------------------------------
        status, msg = s.c.try_call(
            "object-set-cover-from-file", space_id=s.space_id,
            object_id=obj, staged_path="gibtsnicht.png",
        )
        s.check("a missing cover file is refused", status == "err", msg)
        notes = pathlib.Path(IN_ROOT) / "livetest-kein-bild.txt"
        notes.write_text("kein bild\n", encoding="utf-8")
        status, msg = s.c.try_call(
            "object-set-cover-from-file", space_id=s.space_id,
            object_id=obj, staged_path="livetest-kein-bild.txt",
        )
        s.check(
            "a non-image file is refused with the accepted formats named",
            status == "err" and "PNG" in msg,
            msg,
        )
        notes.unlink(missing_ok=True)
    finally:
        staged.unlink(missing_ok=True)

    # --- batched schema operations ----------------------------------------
    prop = s.c.call(
        "create-property", space_id=s.space_id,
        name=s.probe("batchprop"), format="multi_select",
    )
    prop_id = (prop.get("property") or prop)["id"]
    s.track(prop_id)

    created = s.c.call(
        "create-tag", space_id=s.space_id, property_id=prop_id,
        tags=[{"name": s.probe(f"tag{i}"), "color": "red"} for i in range(5)],
    )
    s.check(
        "five tags are created in one call",
        created["requested"] == 5 and created["succeeded"] == 5 and created["failed"] == 0,
        created,
    )
    s.check(
        "the batch answer stays compact unless details are asked for",
        "results" not in created,
        list(created.keys()),
    )
    listed = s.c.call("list-tags", space_id=s.space_id, property_id=prop_id)
    tag_ids = [t["id"] for t in (listed.get("tags") or listed.get("data") or [])]
    s.track(*tag_ids)
    s.check("all five really exist", len(tag_ids) == 5, len(tag_ids))

    renamed = s.c.call(
        "update-tag", space_id=s.space_id, property_id=prop_id,
        items=[{"tag_id": t, "name": s.probe(f"neu{i}")} for i, t in enumerate(tag_ids[:3])],
        include_results=True,
    )
    s.check(
        "three tags are renamed in one call",
        renamed["succeeded"] == 3 and "results" in renamed,
        {k: renamed.get(k) for k in ("requested", "succeeded", "failed")},
    )
    listed = s.c.call("list-tags", space_id=s.space_id, property_id=prop_id)
    names = [t["name"] for t in (listed.get("tags") or listed.get("data") or [])]
    s.check(
        "the new names are on the tags",
        sum(1 for n in names if "neu" in n) == 3,
        names,
    )

    # A batch keeps going past a bad entry and says which one failed.
    partial = s.c.call(
        "delete-tag", space_id=s.space_id, property_id=prop_id,
        tag_ids=tag_ids[:2] + ["bafyreigibtesnichtxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"],
    )
    s.check(
        "a batch reports partial failure instead of hiding it",
        partial["succeeded"] == 2 and partial["failed"] == 1 and partial.get("failures"),
        partial,
    )
    stopped = s.c.call(
        "delete-tag", space_id=s.space_id, property_id=prop_id,
        tag_ids=["bafyreigibtesnichtxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"] + tag_ids[2:],
        stop_on_error=True,
    )
    s.check(
        "stop_on_error halts at the first failure",
        stopped["failed"] == 1 and stopped["succeeded"] == 0,
        stopped,
    )
    status, msg = s.c.try_call(
        "delete-tag", space_id=s.space_id, property_id=prop_id, tag_ids=[]
    )
    s.check("an empty batch is refused", status == "err", msg)

    # The single-item form must keep working and keep its old shape.
    single = s.c.call(
        "delete-tag", space_id=s.space_id, property_id=prop_id, tag_id=tag_ids[2]
    )
    s.check(
        "the single-item form still works and is not wrapped in batch counters",
        "requested" not in single,
        single,
    )
    s.c.call("delete-property", space_id=s.space_id, property_id=prop_id)
