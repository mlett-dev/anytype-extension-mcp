"""Block editing: creation, text, marks, structure, paste, split, merge.

Most checks here read the block back rather than trusting the call, because
anytype-heart defers text writes by three seconds unless the object is closed —
a bug this suite exists to catch if the flush ever regresses.
"""


def run(s):
    obj = s.page("body")

    # --- writes must be visible to the very next read ---------------------
    bid = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj, kind="text", text="SOFORT"
    )["block_id"]
    s.check("block-create text visible immediately", s.block(obj, bid)["text"] == "SOFORT")

    s.c.call(
        "block-set-text", space_id=s.space_id, object_id=obj, block_id=bid, text="GEAENDERT"
    )
    s.check(
        "block-set-text visible immediately (deferred-write flush)",
        s.block(obj, bid)["text"] == "GEAENDERT",
        s.block(obj, bid)["text"],
    )

    s.c.call(
        "block-turn-into", space_id=s.space_id, object_id=obj, block_ids=[bid], style="header2"
    )
    s.check("block-turn-into applies", s.block(obj, bid)["style"] == "header2")

    s.c.call(
        "block-mark", space_id=s.space_id, object_id=obj, block_ids=[bid],
        **{"from": 0, "to": 3, "type": "bold"},
    )
    s.check(
        "block-mark applies",
        "bold" in [m["type"] for m in s.block(obj, bid).get("marks", [])],
    )

    s.c.call(
        "block-style", space_id=s.space_id, object_id=obj, block_ids=[bid],
        text_color="red", align="center",
    )
    styled = s.block(obj, bid)
    s.check(
        "block-style applies colour and alignment",
        (styled.get("color"), styled.get("align")) == ("red", "center"),
        (styled.get("color"), styled.get("align")),
    )

    cb = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj, kind="text",
        style="checkbox", text="Aufgabe",
    )["block_id"]
    s.c.call("block-set-checked", space_id=s.space_id, object_id=obj, block_id=cb, checked=True)
    s.check("block-set-checked applies", s.block(obj, cb)["checked"] is True)

    # --- paste parses markdown into real blocks ---------------------------
    target = s.page("paste")
    s.c.call("block-create", space_id=s.space_id, object_id=target, kind="text", text="BESTAND")
    res = s.c.call(
        "block-paste", space_id=s.space_id, object_id=target,
        markdown="# H1\n\n- a\n- b\n\n1. eins\n\n- [x] fertig\n\n> zitat",
    )
    body = s.body_text(target)
    styles = {style for style, _ in body}
    s.check("paste creates several blocks in one call", res["created_count"] >= 6, res["created_count"])
    s.check("paste appends after existing content", body[0] == ("paragraph", "BESTAND"), body[:1])
    s.check("paste parses headings", "header1" in styles, styles)
    s.check("paste parses bullet lists", "marked" in styles, styles)
    s.check("paste parses numbered lists", "numbered" in styles, styles)
    s.check("paste parses quotes", "quote" in styles, styles)
    checked = [b for b in s.blocks(target) if b.get("style") == "checkbox"]
    s.check(
        "paste preserves checkbox state",
        bool(checked) and checked[0].get("checked") is True,
        checked[:1],
    )

    # Pasting onto the title renames the object instead of inserting blocks,
    # so the server refuses it. See the README.
    status, _ = s.c.try_call(
        "block-paste", space_id=s.space_id, object_id=target,
        markdown="X", target_block_id="title",
    )
    s.check("paste into the title block is refused", status == "err")
    status, _ = s.c.try_call("block-paste", space_id=s.space_id, object_id=target)
    s.check("paste without content is refused", status == "err")

    # --- split and merge ---------------------------------------------------
    sb = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj, kind="text",
        text="LinkeHaelfte RechteHaelfte",
    )["block_id"]
    split = s.c.call(
        "block-split", space_id=s.space_id, object_id=obj, block_id=sb, at=13
    )
    new_id = split["new_block_id"]
    s.check(
        "split divides the text and both halves are readable at once",
        (s.block(obj, sb)["text"], s.block(obj, new_id)["text"])
        == ("LinkeHaelfte ", "RechteHaelfte"),
        (s.block(obj, sb)["text"], s.block(obj, new_id)["text"]),
    )
    s.c.call(
        "block-merge", space_id=s.space_id, object_id=obj,
        first_block_id=sb, second_block_id=new_id,
    )
    s.check(
        "merge rejoins the text and drops the second block",
        s.block(obj, sb)["text"] == "LinkeHaelfte RechteHaelfte" and s.block(obj, new_id) is None,
    )
    status, _ = s.c.try_call(
        "block-split", space_id=s.space_id, object_id=obj, block_id=sb, at=9999
    )
    s.check("split beyond the end of the text is refused", status == "err")

    # --- move and duplicate report usable ids ------------------------------
    # Within one object ids survive; moving into another object re-creates the
    # blocks under fresh ids, which the tool has to report or the caller is
    # left holding dead ids.
    other = s.page("moveziel")
    root = s.c.call("block-list", space_id=s.space_id, object_id=other)["root_block_id"]
    moved = s.c.call(
        "block-move", space_id=s.space_id, object_id=obj, block_ids=[cb],
        target_object_id=other, drop_target_id=root, position="inner",
    )
    new_ids = moved.get("moved_block_ids") or []
    s.check("cross-object move reports new ids", len(new_ids) == 1 and new_ids[0] != cb, moved)
    if new_ids:
        s.c.call(
            "block-set-text", space_id=s.space_id, object_id=other,
            block_id=new_ids[0], text="NACH UMZUG",
        )
        s.check(
            "the reported id is the block that actually moved",
            s.block(other, new_ids[0])["text"] == "NACH UMZUG",
        )

    keep = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj, kind="text", text="ORIGINAL"
    )["block_id"]
    same = s.c.call(
        "block-move", space_id=s.space_id, object_id=obj, block_ids=[keep],
        drop_target_id=bid, position="top",
    )
    s.check(
        "same-object move keeps the ids and says so",
        same.get("block_ids") == [keep] and "moved_block_ids" not in same,
        same,
    )

    # --- page columns ------------------------------------------------------
    # Columns are not a block a caller creates: they appear when something is
    # moved beside another block. The enum has to offer left/right, or no
    # schema-validating client can ask for it at all.
    col_page = s.page("spalten")
    links = s.c.call(
        "block-create", space_id=s.space_id, object_id=col_page, kind="text", text="LINKS"
    )["block_id"]
    rechts = s.c.call(
        "block-create", space_id=s.space_id, object_id=col_page, kind="text", text="RECHTS"
    )["block_id"]
    s.c.call(
        "block-move", space_id=s.space_id, object_id=col_page, block_ids=[rechts],
        drop_target_id=links, position="right",
    )
    blocks = s.c.call("block-list", space_id=s.space_id, object_id=col_page)["blocks"]
    by_id = {b["id"]: b for b in blocks}
    rows = [b for b in blocks if b.get("kind") == "layout" and b.get("style") == "row"]
    s.check("position=right builds a row of columns", len(rows) == 1, [b.get("style") for b in blocks])
    columns = rows[0]["children_ids"] if rows else []
    s.check(
        "the two blocks end up in one column each",
        [by_id[c]["children_ids"][0] for c in columns] == [links, rechts],
        columns,
    )

    widened = s.c.call(
        "block-column-width", space_id=s.space_id, object_id=col_page,
        block_id=links, widths=[2, 1],
    )
    s.check(
        "shares are normalised to fractions of the row",
        [round(c["width"], 3) for c in widened["columns"]] == [0.667, 0.333],
        widened["columns"],
    )
    s.check(
        "the row is found from a block sitting inside a column",
        widened["row_id"] == rows[0]["id"],
        widened["row_id"],
    )
    s.check(
        "the widths that were replaced are reported back",
        [c["previous_width"] for c in widened["columns"]] == [0, 0],
        widened["columns"],
    )
    s.check(
        "block-list reads the widths back",
        [round(s.block(col_page, c)["width"], 3) for c in columns] == [0.667, 0.333],
        [s.block(col_page, c).get("width") for c in columns],
    )
    reset = s.c.call(
        "block-column-width", space_id=s.space_id, object_id=col_page,
        block_id=rows[0]["id"], widths=[0, 0],
    )
    s.check(
        "zeroes reset the columns to equal widths",
        [c["width"] for c in reset["columns"]] == [0, 0]
        and [round(c["previous_width"], 3) for c in reset["columns"]] == [0.667, 0.333],
        reset["columns"],
    )
    s.check(
        "a reset width is absent from block-list rather than reported as zero",
        all("width" not in s.block(col_page, c) for c in columns),
        [s.block(col_page, c) for c in columns],
    )
    status, msg = s.c.try_call(
        "block-column-width", space_id=s.space_id, object_id=col_page,
        block_id=rows[0]["id"], widths=[1],
    )
    s.check("a width per column is required", status == "err" and "columns" in msg, msg)
    status, msg = s.c.try_call(
        "block-column-width", space_id=s.space_id, object_id=obj,
        block_id=bid, widths=[1, 1],
    )
    s.check("a block outside any row is refused with the way to make one", status == "err" and "block-move" in msg, msg)

    dup = s.c.call("block-duplicate", space_id=s.space_id, object_id=obj, block_ids=[keep])
    copies = dup.get("new_block_ids") or []
    s.check("duplicate reports the ids of the copies", len(copies) == 1 and copies[0] != keep, dup)
    if copies:
        s.c.call(
            "block-set-text", space_id=s.space_id, object_id=obj,
            block_id=copies[0], text="KOPIE",
        )
        s.check("the copy's reported id is writable", s.block(obj, copies[0])["text"] == "KOPIE")

    s.c.call("block-delete", space_id=s.space_id, object_id=obj, block_ids=[keep])
    s.check("block-delete removes the block", s.block(obj, keep) is None)
