"""Icon writes that anytype-heart accepts, stores, and then hides.

An icon looks like one field but lives in three detail relations at once —
iconEmoji, iconImage, and the iconName/iconOption pair. They are not exclusive:
heart writes only the relation the requested format needs and never clears the
others, then resolves them on read in a fixed order (iconName, iconEmoji,
iconImage). Writing a file icon over an existing emoji is therefore stored and
invisible, and the API reports success either way.

These tests exist because every check here passed against a server that did not
work: the earlier icon check in the cover suite set a file icon on an object
that had no icon yet, which is the one case the bug never touched.

One claim in this area is NOT covered here and is source-derived only: an
object created from a template that carries an emoji, with a file icon passed
alongside. heart merges the request's details onto the template's state without
iconEmoji among the keys the template wins (`templatePreferableRelationKeys` in
core/block/template/templateimpl/impl.go), so the emoji should survive and hide
the icon. Reproducing it needs an emoji-bearing template, and the test space has
none — the create path is covered here without a template instead.
"""

import pathlib
import struct
import zlib

from harness import IN_ROOT


def write_png(path, size=32, rgb=(200, 120, 90)):
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


def icon_of(s, object_id):
    return s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=object_id,
        fields=["id", "icon"], include_icon=True,
    )["object"].get("icon") or {}


def run(s):
    staged = pathlib.Path(IN_ROOT) / "livetest icon bild.png"
    try:
        write_png(staged)
        image_id = s.c.call(
            "file-upload", space_id=s.space_id,
            staged_path="livetest icon bild.png", type="image",
        )["object_id"]
        s.track(image_id)

        # --- the case the bug was about: an emoji is already there ----------
        obj = s.page("icon swap")
        s.c.call("update-object-compact", space_id=s.space_id, object_id=obj,
                 icon={"format": "emoji", "emoji": "🚇"})
        s.check("an emoji icon can be set", icon_of(s, obj).get("format") == "emoji", icon_of(s, obj))

        swapped = s.c.call(
            "update-object-compact", space_id=s.space_id, object_id=obj,
            icon={"format": "file", "file": image_id}, include_icon=True,
        )
        s.check(
            "a file icon replaces an existing emoji and is reported as applied",
            swapped.get("updated") is True and "icon_warning" not in swapped,
            swapped,
        )
        s.check(
            "the response carries the file icon, not the emoji it replaced",
            (swapped["object"].get("icon") or {}).get("format") == "file",
            swapped["object"].get("icon"),
        )
        s.check(
            "and an independent read agrees",
            icon_of(s, obj).get("format") == "file",
            icon_of(s, obj),
        )

        # --- emoji over emoji never broke; it must stay unbroken ------------
        s.c.call("update-object-compact", space_id=s.space_id, object_id=obj,
                 icon={"format": "emoji", "emoji": "🧪"})
        s.check(
            "an emoji still replaces another emoji",
            icon_of(s, obj).get("emoji") == "🧪",
            icon_of(s, obj),
        )

        # The other direction hid nothing, so it looked fine — but it left the
        # file icon behind. Removing the emoji is what exposes a leftover: the
        # object has to end up with no icon rather than the old picture.
        cleared = s.c.call(
            "update-object-compact", space_id=s.space_id, object_id=obj,
            icon={"format": "emoji", "emoji": ""}, include_icon=True,
        )
        s.check(
            "clearing an emoji is not mistaken for a failed icon write",
            cleared.get("updated") is True and "icon_warning" not in cleared,
            cleared,
        )
        s.check(
            "switching to an emoji had cleared the file icon rather than hiding it",
            not icon_of(s, obj),
            icon_of(s, obj),
        )

        # --- creation carries the icon too ---------------------------------
        created_obj = s.c.call(
            "create-objects-compact-many", space_id=s.space_id,
            items=[{"type_key": "page", "name": s.probe("icon create"),
                    "icon": {"format": "file", "file": image_id}}],
            include_icon=True,
        )
        new_id = created_obj["objects"][0]["object_id"]
        s.track(new_id)
        s.check(
            "an object created with a file icon reports it as applied",
            created_obj.get("ok_count") == 1
            and "icon_warning" not in created_obj["objects"][0],
            created_obj,
        )
        s.check("and the created object shows the file icon",
                icon_of(s, new_id).get("format") == "file", icon_of(s, new_id))

        # --- the bulk path counts a repaired icon as ok --------------------
        bulk = s.page("icon bulk")
        s.c.call("update-object-compact", space_id=s.space_id, object_id=bulk,
                 icon={"format": "emoji", "emoji": "🚇"})
        result = s.c.call(
            "update-objects-compact-many", space_id=s.space_id,
            items=[{"object_id": bulk, "icon": {"format": "file", "file": image_id}}],
            include_icon=True,
        )
        s.check(
            "a bulk icon swap is repaired and counted as ok",
            result.get("ok_count") == 1 and result.get("error_count") == 0,
            result,
        )
        s.check("and the bulk entry reports the file icon",
                icon_of(s, bulk).get("format") == "file", icon_of(s, bulk))

        # --- types: same precedence, one unfixable corner ------------------
        created = s.c.call(
            "create-type", space_id=s.space_id,
            name=s.probe("icon type"), plural_name=s.probe("icon types"),
            layout="basic", icon={"format": "icon", "name": "book", "color": "red"},
        )
        type_id = created["type"]["id"]
        s.track(type_id)
        s.check("a type can be created with a built-in icon",
                (created["type"].get("icon") or {}).get("format") == "icon", created["type"].get("icon"))

        swapped_type = s.c.call(
            "update-type", space_id=s.space_id, type_id=type_id,
            icon={"format": "emoji", "emoji": "🧪"},
        )
        s.check(
            "an emoji replaces a type's built-in icon instead of hiding behind it",
            (swapped_type["type"].get("icon") or {}).get("format") == "emoji"
            and "icon_warning" not in swapped_type,
            swapped_type,
        )

        # heart stores a file icon on a type but its reader never returns one,
        # so there is no losing relation to clear. Saying so beats claiming
        # success.
        refused = s.c.call(
            "update-type", space_id=s.space_id, type_id=type_id,
            icon={"format": "file", "file": image_id},
        )
        s.check(
            "a file icon on a type is reported as not applied",
            refused.get("icon_applied") is False and refused.get("icon_warning"),
            refused,
        )
    finally:
        staged.unlink(missing_ok=True)
        s.remove_shared("livetest icon bild.png")
