"""The sidebar: a space's derived system objects, and the widget object.

The widget object is SHARED — every member of the space sees it — so this suite
only pins objects it created itself and unpins them in the same run. It never
removes a widget it did not make, and the one call that reorders existing ones
puts them back.
"""


def run(s):
    # --- system objects ----------------------------------------------------
    sys_objects = s.c.call("space-system-objects", space_id=s.space_id)
    s.check(
        "space-system-objects answers the derived ids a sidebar needs",
        all(sys_objects.get(k) for k in ("widgets_id", "home_id", "archive_id")),
        {k: sys_objects.get(k) for k in ("widgets_id", "home_id", "archive_id")},
    )
    # The personal widgets id is computed, not derived, so it must follow the
    # space id exactly — a wrong encoding would silently read an empty object.
    s.check(
        "personal_widgets_id encodes the space id the way heart does",
        sys_objects.get("personal_widgets_id")
        == "_personalWidgets_" + s.space_id.replace(".", "_", 1),
        sys_objects.get("personal_widgets_id"),
    )

    # --- inspect -----------------------------------------------------------
    live = s.c.call("sidebar-inspect", space_id=s.space_id)
    s.check(
        "sidebar-inspect defaults to the live widgets and counts all of them",
        live.get("filter") == "live"
        and live.get("total") == live.get("ok", 0) + live.get("bin", 0) + live.get("deleted", 0),
        {k: live.get(k) for k in ("filter", "total", "ok", "bin", "deleted", "shown")},
    )
    s.check(
        "a live listing hides nothing but dead widgets",
        all(w["status"] != "deleted" for w in live.get("widgets", [])),
        [w["status"] for w in live.get("widgets", [])][:10],
    )
    every = s.c.call("sidebar-inspect", space_id=s.space_id, filter="all")
    s.check(
        "filter=all lists as many widgets as the counts promise",
        every.get("shown") == every.get("total"),
        {k: every.get(k) for k in ("shown", "total")},
    )
    s.check(
        "every widget reports the id the write tools take, and a status",
        all(w.get("block_id") and w.get("status") for w in every.get("widgets", [])),
        len(every.get("widgets", [])),
    )
    status, msg = s.c.try_call("sidebar-inspect", space_id=s.space_id, filter="lebendig")
    s.check("an unknown filter is refused", status == "err", msg)

    personal = s.c.call("sidebar-inspect", space_id=s.space_id, scope="personal")
    s.check(
        "the personal widget object reads as its own, empty object",
        personal.get("object_id") == sys_objects.get("personal_widgets_id"),
        personal.get("object_id"),
    )

    # --- prune is a dry run by default -------------------------------------
    dry = s.c.call("sidebar-prune", space_id=s.space_id)
    s.check(
        "sidebar-prune reports without applying unless told to",
        dry.get("applied") is False and len(dry.get("widgets", [])) == dry.get("deleted"),
        {k: dry.get(k) for k in ("applied", "deleted", "total")},
    )
    after_dry = s.c.call("sidebar-inspect", space_id=s.space_id, filter="all")
    s.check(
        "a dry run changes nothing",
        after_dry.get("total") == every.get("total"),
        [every.get("total"), after_dry.get("total")],
    )
    for tool in ("sidebar-widget-remove", "sidebar-arrange"):
        status, msg = s.c.try_call(tool, space_id=s.space_id, block_ids=["nichtvorhanden"])
        s.check(f"{tool} refuses a block id that is not a widget", status == "err", msg)

    # --- pin, reconfigure, reorder, unpin ----------------------------------
    # The widget object is shared, so everything here is created by this run
    # and removed again before it returns.
    probe = s.page("pin")
    pinned = s.c.call(
        "sidebar-widget-add", space_id=s.space_id, target_ids=[probe],
        layout="tree", limit=4, position="top",
    )
    s.check(
        "sidebar-widget-add pins an object at the top with the layout asked for",
        pinned.get("added") == 1
        and pinned["widgets"][0]["position"] == 0
        and pinned["widgets"][0]["layout"] == "tree"
        and pinned["widgets"][0]["limit"] == 4,
        pinned.get("widgets"),
    )
    block_id = pinned["widgets"][0]["block_id"]

    again = s.c.call("sidebar-widget-add", space_id=s.space_id, target_ids=[probe])
    s.check(
        "pinning the same object twice is a no-op, not a duplicate",
        again.get("added") == 0 and again.get("already_present") == [probe],
        again,
    )
    status, msg = s.c.try_call(
        "sidebar-widget-add", space_id=s.space_id, target_ids=[probe], layout="view"
    )
    s.check("layout=view without a view_id is refused", status == "err", msg)

    updated = s.c.call(
        "sidebar-widget-update", space_id=s.space_id, block_id=block_id,
        layout="compact_list", limit=10,
    )
    s.check(
        "sidebar-widget-update writes only the fields passed, and reads back",
        updated.get("changed") == ["layout", "limit"]
        and updated["widget"]["layout"] == "compact_list"
        and updated["widget"]["limit"] == 10,
        updated.get("widget"),
    )
    status, msg = s.c.try_call(
        "sidebar-widget-update", space_id=s.space_id, block_id=block_id
    )
    s.check("an update with nothing to change is refused", status == "err", msg)

    # Repointing has to keep the widget, not replace it: the block id is what
    # every other call uses, and a new one would break a caller mid-sequence.
    repointed = s.c.call(
        "sidebar-widget-update", space_id=s.space_id, block_id=block_id, target_id="bin",
    )
    s.check(
        "a repointed widget keeps its block id and is recognised as built-in",
        repointed["widget"]["block_id"] == block_id
        and repointed["widget"]["target_id"] == "bin"
        and repointed["widget"].get("builtin") is True,
        repointed.get("widget"),
    )
    s.c.call(
        "sidebar-widget-update", space_id=s.space_id, block_id=block_id, target_id=probe
    )

    # Arrange names a subset; the rest keeps its relative order below.
    full = s.c.call("sidebar-inspect", space_id=s.space_id, filter="all")
    others = [w["block_id"] for w in full["widgets"] if w["block_id"] != block_id]
    if others:
        arranged = s.c.call(
            "sidebar-arrange", space_id=s.space_id, block_ids=others[:2],
        )
        order = [w["block_id"] for w in arranged["order"]]
        s.check(
            "sidebar-arrange moves the named widgets to the top in order",
            order[: len(others[:2])] == others[:2],
            order[:4],
        )
        s.check(
            "a widget not named keeps its place below the named ones",
            block_id in order[len(others[:2]):],
            order,
        )

    removed = s.c.call(
        "sidebar-widget-remove", space_id=s.space_id, block_ids=[block_id]
    )
    s.check(
        "sidebar-widget-remove unpins and leaves no empty wrapper",
        removed.get("removed") == 1 and not removed.get("orphan_block_ids"),
        removed,
    )
    s.check(
        "the object itself survives being unpinned",
        s.c.call("object-flags", space_id=s.space_id, object_ids=[probe])["objects"][0]["found"],
        probe,
    )
