"""Property usage analysis and the cleanup built on it.

Everything here runs on fixtures this suite creates: its own property, its own
options, its own objects. It must never touch real data, because the operation
under test deletes things.

The two cases that decide whether the cleanup is safe both get their own
option:

  * an option used ONLY by an object in the bin — ObjectSearch hides archived
    objects unless asked, so a careless scan would call this unused
  * an option referenced ONLY by a query's filter — no object holds it at all
"""


def run(s):
    prop = s.c.call(
        "create-property", space_id=s.space_id,
        name=s.probe("usageprop"), format="multi_select",
    )
    prop_id = (prop.get("property") or prop)["id"]
    prop_key = (prop.get("property") or prop)["key"]
    s.track(prop_id)

    labels = ["live-a", "live-b", "nur-archiv", "nur-view", "ungenutzt-1", "ungenutzt-2"]
    s.c.call(
        "create-tag", space_id=s.space_id, property_id=prop_id,
        tags=[{"name": s.probe(l), "color": "grey"} for l in labels],
    )
    listed = s.c.call("list-tags", space_id=s.space_id, property_id=prop_id)
    tags = {t["name"].rsplit(" ", 1)[-1]: t["id"]
            for t in (listed.get("tags") or listed.get("data") or [])}
    s.track(*tags.values())
    s.check("six fixture options exist", len(tags) == 6, sorted(tags))

    def tag_objects(label, count):
        made = []
        for i in range(count):
            obj = s.page(f"{label}-obj{i}")
            s.c.call(
                "update-object-compact", space_id=s.space_id, object_id=obj,
                properties=[{"key": prop_key, "multi_select": [tags[label]]}],
            )
            made.append(obj)
        return made

    tag_objects("live-a", 2)
    tag_objects("live-b", 1)

    # The archived trap: this object goes to the bin, and its option must still
    # count as used.
    archived_obj = tag_objects("nur-archiv", 1)[0]
    s.c.call("delete-object", space_id=s.space_id, object_id=archived_obj)

    # The view trap: no object holds this option, only a query's filter does.
    page_type = s.type_by_key("page")
    query = s.c.call(
        "query-create", space_id=s.space_id,
        name=s.probe("usagequery"), source=[page_type["id"]],
    )
    s.track(query["object_id"])
    s.c.call(
        "query-filter-add", space_id=s.space_id, object_id=query["object_id"],
        view_id=query["views"][0]["id"], property_key=prop_key,
        condition="in", value=[tags["nur-view"]], format="multi_select",
    )
    s.settle(2.0)

    # --- the analysis ------------------------------------------------------
    analysis = s.c.call(
        "analyze-property-usage", space_id=s.space_id, property_id=prop_id
    )
    counts = {e["name"].rsplit(" ", 1)[-1]: e for e in analysis["options"]}
    s.check(
        "the analysis reports every option",
        analysis["total_options"] == 6 and len(counts) == 6,
        analysis["total_options"],
    )
    s.check("a live option is counted", counts["live-a"]["usage_count"] == 2, counts.get("live-a"))
    s.check("a second live option is counted", counts["live-b"]["usage_count"] == 1, counts.get("live-b"))
    s.check(
        "an option used only by an object in the bin counts as used",
        counts["nur-archiv"]["usage_count"] == 1
        and counts["nur-archiv"].get("archived_objects") == 1,
        counts.get("nur-archiv"),
    )
    s.check(
        "an option referenced only by a view filter counts as used",
        counts["nur-view"].get("view_filter_references", 0) >= 1,
        counts.get("nur-view"),
    )
    s.check(
        "the genuinely unused options are the only ones with no references",
        analysis["unused_options"] == 2 and analysis["used_options"] == 4,
        {k: v for k, v in analysis.items() if k.endswith("_options")},
    )
    only_unused = s.c.call(
        "analyze-property-usage", space_id=s.space_id,
        property_id=prop_id, only_unused=True,
    )
    s.check(
        "only_unused returns exactly those two",
        {e["name"].rsplit(" ", 1)[-1] for e in only_unused["options"]}
        == {"ungenutzt-1", "ungenutzt-2"},
        [e["name"] for e in only_unused["options"]],
    )
    status, msg = s.c.try_call(
        "analyze-property-usage", space_id=s.space_id,
        property_id=s.property_by_key("description")["id"],
    )
    s.check(
        "a property without options is refused with a reason",
        status == "err" and "select" in msg,
        msg,
    )

    # --- the cleanup, first as a rehearsal ---------------------------------
    plan = s.c.call("clean-unused-tags", space_id=s.space_id, property_id=prop_id)
    proposed = {o["name"].rsplit(" ", 1)[-1] for o in plan["options_to_remove"]}
    s.check("the cleanup is a dry run by default", plan["dry_run"] is True and plan["removed"] == 0, plan)
    s.check(
        "it proposes only the unused options, sparing the archived and view cases",
        proposed == {"ungenutzt-1", "ungenutzt-2"},
        proposed,
    )
    still_there = s.c.call("list-tags", space_id=s.space_id, property_id=prop_id)
    s.check(
        "a dry run changes nothing",
        len(still_there.get("tags") or still_there.get("data") or []) == 6,
    )

    status, msg = s.c.try_call(
        "clean-unused-tags", space_id=s.space_id, property_id=prop_id, dry_run=False
    )
    s.check("executing without confirm is refused", status == "err" and "confirm" in msg, msg)
    status, msg = s.c.try_call(
        "clean-unused-tags", space_id=s.space_id, property_id=prop_id,
        dry_run=False, confirm=True, limit=1,
    )
    s.check("the limit stops an unexpectedly large removal", status == "err" and "limit" in msg, msg)

    kept = s.c.call(
        "clean-unused-tags", space_id=s.space_id, property_id=prop_id,
        keep_tag_ids=[tags["ungenutzt-1"]],
    )
    s.check(
        "keep_tag_ids spares an option that is otherwise unused",
        {o["name"].rsplit(" ", 1)[-1] for o in kept["options_to_remove"]} == {"ungenutzt-2"}
        and kept["kept_by_request"] == 1,
        kept,
    )

    # --- and now for real --------------------------------------------------
    done = s.c.call(
        "clean-unused-tags", space_id=s.space_id, property_id=prop_id,
        dry_run=False, confirm=True,
    )
    s.check("the removal reports two options gone", done["removed"] == 2, done)
    s.settle(2.0)
    remaining = s.c.call("list-tags", space_id=s.space_id, property_id=prop_id)
    names = {t["name"].rsplit(" ", 1)[-1]
             for t in (remaining.get("tags") or remaining.get("data") or [])}
    s.check(
        "exactly the four referenced options survive",
        names == {"live-a", "live-b", "nur-archiv", "nur-view"},
        names,
    )
    s.check(
        "the option used only from the bin was NOT removed",
        "nur-archiv" in names,
    )
    s.check(
        "the option used only by a view filter was NOT removed",
        "nur-view" in names,
    )

    after = s.c.call("clean-unused-tags", space_id=s.space_id, property_id=prop_id)
    s.check(
        "a second run finds nothing left to do",
        after["unused_options"] == 0 and after["removed"] == 0,
        after,
    )

    s.c.call("object-set-archived", space_id=s.space_id, object_ids=[archived_obj], archived=False)
    s.c.call("delete-property", space_id=s.space_id, property_id=prop_id)

    # --- ignore_bin --------------------------------------------------------
    # A fresh property whose only user is in the bin, so the flag has something
    # unambiguous to act on.
    prop2 = s.c.call(
        "create-property", space_id=s.space_id,
        name=s.probe("binprop"), format="multi_select",
    )
    prop2_id = (prop2.get("property") or prop2)["id"]
    prop2_key = (prop2.get("property") or prop2)["key"]
    s.track(prop2_id)
    s.c.call(
        "create-tag", space_id=s.space_id, property_id=prop2_id,
        tags=[{"name": s.probe("binonly"), "color": "grey"}],
    )
    listed2 = s.c.call("list-tags", space_id=s.space_id, property_id=prop2_id)
    bin_tag = (listed2.get("tags") or listed2.get("data") or [])[0]["id"]
    s.track(bin_tag)
    holder = s.page("binhalter")
    s.c.call(
        "update-object-compact", space_id=s.space_id, object_id=holder,
        properties=[{"key": prop2_key, "multi_select": [bin_tag]}],
    )
    s.c.call("delete-object", space_id=s.space_id, object_id=holder)
    s.settle(2.0)

    strict = s.c.call("clean-unused-tags", space_id=s.space_id, property_id=prop2_id)
    s.check(
        "by default an option used only from the bin is kept",
        strict["unused_options"] == 0 and strict.get("only_referenced_from_bin"),
        strict,
    )
    s.check(
        "and the report says the flag would remove it",
        "ignore_bin" in str(strict.get("note_bin", "")),
        strict.get("note_bin"),
    )
    relaxed = s.c.call(
        "clean-unused-tags", space_id=s.space_id, property_id=prop2_id, ignore_bin=True
    )
    s.check(
        "ignore_bin proposes exactly that option",
        [o["name"].rsplit(" ", 1)[-1] for o in relaxed["options_to_remove"]] == ["binonly"],
        relaxed,
    )
    s.check("ignore_bin is still a dry run by default", relaxed["dry_run"] is True)
    s.c.call("object-set-archived", space_id=s.space_id, object_ids=[holder], archived=False)
    s.c.call("delete-property", space_id=s.space_id, property_id=prop2_id)

    # --- the bin is visible at all ----------------------------------------
    doomed = s.page("imkorb")
    s.c.call("delete-object", space_id=s.space_id, object_id=doomed)
    s.settle(2.0)
    binned = s.c.call("list-archived", space_id=s.space_id, limit=5000)
    s.check(
        "list-archived finds an object that was just archived",
        any(o["object_id"] == doomed for o in binned["objects"]),
        f"total={binned['total']} shown={binned['shown']}",
    )
    s.check(
        "the bin reports a total, which the REST tools cannot see at all",
        binned["total"] > 0,
        binned["total"],
    )
    # Which member made an object matters in a space several accounts write to:
    # the MCP has its own identity, so its objects must not be attributed to
    # whoever happens to be looking at the bin.
    listed_members = s.c.call("list-members", space_id=s.space_id)
    member_names = {
        m.get("name") for m in (listed_members.get("members") or listed_members.get("data") or [])
    }
    entry = next((o for o in binned["objects"] if o["object_id"] == doomed), None)
    s.check(
        "the bin names the member who created an object",
        entry and entry.get("created_by") in member_names and entry.get("created_by"),
        {"entry": entry, "members": sorted(n for n in member_names if n)},
    )
    s.c.call("object-set-archived", space_id=s.space_id, object_ids=[doomed], archived=False)

    # --- schema usage ------------------------------------------------------
    lonely = s.c.call(
        "create-property", space_id=s.space_id,
        name=s.probe("niebenutzt"), format="text",
    )
    lonely_id = (lonely.get("property") or lonely)["id"]
    s.track(lonely_id)
    s.settle(2.0)
    schema = s.c.call(
        "analyze-schema-usage", space_id=s.space_id, kind="properties", only_unused=True
    )
    s.check(
        "a property nothing fills in is reported as unused",
        any(p["id"] == lonely_id for p in schema["properties"]),
        schema.get("unused_properties"),
    )
    used = s.c.call("analyze-schema-usage", space_id=s.space_id, kind="both")
    busiest = max(used["properties"], key=lambda p: p["usage_count"], default=None)
    s.check(
        "widely used properties are reported as used",
        busiest and busiest["usage_count"] > 0,
        busiest,
    )
    page_type = next((t for t in used["types"] if t["key"] == "page"), None)
    s.check(
        "a type with objects is reported as used",
        page_type and page_type["usage_count"] > 0,
        page_type,
    )
    s.c.call("delete-property", space_id=s.space_id, property_id=lonely_id)
