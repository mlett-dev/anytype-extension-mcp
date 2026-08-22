"""Space information, inline dataviews, version diffing, date objects, schema
ordering, block export and the extended block-style.
"""


def run(s):
    # --- storage usage -----------------------------------------------------
    usage = s.c.call("space-file-usage", space_id=s.space_id)
    s.check(
        "space-file-usage reports counts and a readable size",
        "files_count" in usage and "bytes_used" in usage and usage.get("used"),
        {k: usage.get(k) for k in ("files_count", "used", "left", "limit")},
    )

    # --- inline query ------------------------------------------------------
    task = s.type_by_key("task") or s.type_by_key("page")
    query = s.c.call(
        "query-create", space_id=s.space_id, name=s.probe("inline query"), source=[task["id"]]
    )
    s.track(query["object_id"])
    page = s.page("einbettung")
    embedded = s.c.call(
        "block-embed-query", space_id=s.space_id, object_id=page,
        query_object_id=query["object_id"],
    )
    block = s.block(page, embedded["block_id"])
    s.check(
        "block-embed-query inserts a dataview block into the page",
        block is not None and block["kind"] == "dataview",
        block,
    )
    # Which objects the embed shows follows the query; its views are a copy.
    # Both halves have to be readable, or a caller cannot tell an embed from a
    # query and reads the page's missing setOf as "this thing has no source".
    inspected = s.c.call("query-inspect", space_id=s.space_id, object_id=page)
    s.check(
        "the embedded block is reachable as a dataview of the page",
        inspected.get("block_id") == embedded["block_id"],
        inspected.get("block_id"),
    )
    s.check(
        "query-inspect names the query an embed points at",
        inspected.get("target_object_id") == query["object_id"],
        inspected.get("target_object_id"),
    )
    s.check(
        "the source of an embed is read from the query it shows",
        bool(inspected.get("source")) and inspected.get("source_from") == query["object_id"],
        [inspected.get("source"), inspected.get("source_from")],
    )
    s.check(
        "block-list reports which query a dataview block displays",
        s.block(page, embedded["block_id"]).get("target_object_id") == query["object_id"],
        s.block(page, embedded["block_id"]),
    )

    # The copy drifts as soon as either side is edited, so it has to be
    # possible to bring the embed back in line with the query.
    view_id = inspected["views"][0]["id"]
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=page,
        block_id=embedded["block_id"], view_id=view_id, name="NUR-IM-EMBED",
    )
    s.check(
        "the embed can be configured on its own",
        s.c.call("query-inspect", space_id=s.space_id, object_id=page)["views"][0]["name"]
        == "NUR-IM-EMBED",
    )
    s.c.call(
        "block-embed-query", space_id=s.space_id, object_id=page,
        query_object_id=query["object_id"], block_id=embedded["block_id"],
    )
    refreshed = s.c.call("query-inspect", space_id=s.space_id, object_id=page)
    s.check(
        "re-embedding into the same block restores the query's configuration",
        refreshed["views"][0]["name"] != "NUR-IM-EMBED"
        and refreshed["block_id"] == embedded["block_id"],
        refreshed["views"][0]["name"],
    )

    # A page can hold more than one embed, and then the first one is not the
    # only answer: reads have to follow block_id just as writes do.
    second = s.c.call(
        "block-embed-query", space_id=s.space_id, object_id=page,
        query_object_id=query["object_id"],
    )
    both = s.c.call("query-inspect", space_id=s.space_id, object_id=page)
    s.check(
        "query-inspect lists every dataview block of a page",
        {b["block_id"] for b in both.get("dataview_blocks") or []}
        == {embedded["block_id"], second["block_id"]},
        both.get("dataview_blocks"),
    )
    s.check(
        "it warns that more than one is there",
        any("dataview blocks" in w for w in both.get("warnings") or []),
        both.get("warnings"),
    )
    s.check(
        "block_id selects which embed is read",
        s.c.call(
            "query-inspect", space_id=s.space_id, object_id=page,
            block_id=second["block_id"],
        )["block_id"] == second["block_id"],
    )
    status, msg = s.c.try_call(
        "query-inspect", space_id=s.space_id, object_id=page, block_id="gibt-es-nicht"
    )
    s.check("an unknown block id is refused", status == "err", msg)
    status, msg = s.c.try_call(
        "block-embed-query", space_id=s.space_id, object_id=page, query_object_id=""
    )
    s.check("embedding without a query object is refused", status == "err", msg)

    # --- version diff ------------------------------------------------------
    doc = s.page("diff")
    first = s.c.call(
        "block-create", space_id=s.space_id, object_id=doc, kind="text", text="ZEILE-EINS"
    )["block_id"]
    s.settle(1.2)
    s.c.call(
        "block-set-text", space_id=s.space_id, object_id=doc, block_id=first, text="ZEILE-EINS-GEAENDERT"
    )
    second = s.c.call(
        "block-create", space_id=s.space_id, object_id=doc, kind="text", text="ZEILE-ZWEI"
    )["block_id"]
    s.settle(2.0)

    versions = s.c.call("object-versions", space_id=s.space_id, object_id=doc, limit=20)["versions"]
    s.check("the diff test produced several versions", len(versions) >= 2, len(versions))
    if len(versions) >= 2:
        newest, oldest = versions[0]["id"], versions[-1]["id"]
        diff = s.c.call(
            "object-version-diff", space_id=s.space_id, object_id=doc,
            from_version_id=oldest, to_version_id=newest,
        )
        added_texts = [b.get("text") for b in diff["added"]]
        changed_after = [c["after"] for c in diff["changed"]]
        s.check(
            "the diff reports the blocks that appeared",
            any("ZEILE" in (t or "") for t in added_texts) or bool(changed_after),
            {"added": added_texts, "changed": diff["changed"]},
        )
        s.check(
            "the diff distinguishes added, removed and changed",
            all(k in diff for k in ("added_count", "removed_count", "changed_count")),
            {k: diff.get(k) for k in ("added_count", "removed_count", "changed_count")},
        )
        same = s.c.call(
            "object-version-diff", space_id=s.space_id, object_id=doc,
            from_version_id=newest, to_version_id=newest,
        )
        s.check("comparing a version with itself reports no change", same["unchanged"] is True, same)

    # --- date objects ------------------------------------------------------
    today = s.c.call("object-date", space_id=s.space_id)
    s.check("object-date returns an object for today", bool(today.get("object_id")), today)
    fixed = s.c.call("object-date", space_id=s.space_id, date="2024-03-15")
    s.check("object-date accepts a plain date", bool(fixed.get("object_id")), fixed)
    s.check(
        "different days give different objects",
        today.get("object_id") != fixed.get("object_id"),
        (today.get("object_id"), fixed.get("object_id")),
    )
    again = s.c.call("object-date", space_id=s.space_id, date="2024-03-15")
    s.check(
        "the same day gives the same object",
        again.get("object_id") == fixed.get("object_id"),
    )
    status, msg = s.c.try_call("object-date", space_id=s.space_id, date="irgendwann")
    s.check("an unreadable date is refused with a hint", status == "err" and "YYYY" in msg, msg)

    # --- block export ------------------------------------------------------
    src = s.page("export")
    s.c.call(
        "block-paste", space_id=s.space_id, object_id=src,
        markdown="# Ueberschrift\n\n- punkt eins\n- punkt zwei\n\nAbsatz.",
    )
    body = [b for b in s.blocks(src) if b["kind"] == "text" and b["id"] != "title"]
    wanted = [b["id"] for b in body if b.get("text") in ("punkt eins", "punkt zwei")]
    exported = s.c.call(
        "block-export", space_id=s.space_id, object_id=src, block_ids=wanted
    )
    s.check(
        "block-export renders only the selected blocks",
        "punkt eins" in exported["markdown"] and "Absatz." not in exported["markdown"],
        exported["markdown"][:120],
    )
    status, msg = s.c.try_call(
        "block-export", space_id=s.space_id, object_id=src, block_ids=["gibtsnicht"]
    )
    s.check("exporting unknown block ids is refused", status == "err", msg)

    # --- extended block-style ---------------------------------------------
    styled = s.page("stil")
    callout = s.c.call(
        "block-create", space_id=s.space_id, object_id=styled, kind="text",
        style="callout", text="Hinweis",
    )["block_id"]
    s.c.call(
        "block-style", space_id=s.space_id, object_id=styled, block_ids=[callout],
        icon_emoji="💡", background_color="ice",
    )
    block = s.block(styled, callout)
    s.check(
        "block-style sets a callout emoji",
        block.get("icon_emoji") == "💡",
        block.get("icon_emoji"),
    )
    s.check(
        "block-style still sets the background colour",
        block.get("background_color") == "ice",
        block.get("background_color"),
    )
    divider = s.c.call(
        "block-create", space_id=s.space_id, object_id=styled, kind="divider", style="line"
    )["block_id"]
    s.c.call(
        "block-style", space_id=s.space_id, object_id=styled,
        block_ids=[divider], divider_style="dots",
    )
    s.check(
        "block-style switches a divider to dots",
        s.block(styled, divider).get("style") == "dots",
        s.block(styled, divider).get("style"),
    )
    status, _ = s.c.try_call(
        "block-style", space_id=s.space_id, object_id=styled, block_ids=[callout]
    )
    s.check("block-style with nothing to change is refused", status == "err")

    # --- schema ordering ---------------------------------------------------
    tag_prop = s.property_by_key("tag")
    tags = s.c.call("list-tags", space_id=s.space_id, property_id=tag_prop["id"])
    tag_ids = [t["id"] for t in (tags.get("tags") or tags.get("data") or [])]
    if len(tag_ids) >= 2:
        reordered = list(reversed(tag_ids))
        status, _ = s.c.try_call(
            "schema-set-order", space_id=s.space_id, kind="tags",
            property_key="tag", ids=reordered,
        )
        s.check("tag options can be reordered", status == "ok")
    status, msg = s.c.try_call(
        "schema-set-order", space_id=s.space_id, kind="tags", ids=["x"]
    )
    s.check("ordering tags without a property key is refused", status == "err", msg)
    status, msg = s.c.try_call(
        "schema-set-order", space_id=s.space_id, kind="unfug", ids=["x"]
    )
    s.check("an unknown order kind is refused", status == "err", msg)

    # --- homepage ----------------------------------------------------------
    # Set it to a throwaway page and back to nothing, so the space is left as
    # it was found.
    home = s.page("startseite")
    status, _ = s.c.try_call("space-set-homepage", space_id=s.space_id, object_id=home)
    s.check("space-set-homepage is accepted", status == "ok")
    if status == "ok":
        s.c.try_call("space-set-homepage", space_id=s.space_id, object_id="")
