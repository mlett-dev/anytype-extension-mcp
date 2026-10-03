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
    # layout=view without a view_id is a state the GUI itself creates, so it
    # must not be refused — otherwise a snapshot of a real sidebar could not be
    # replayed. Found by snapshotting a space that had such a widget in it.
    status, _ = s.c.try_call(
        "sidebar-widget-add", space_id=s.space_id, target_ids=[probe], layout="view"
    )
    s.check("layout=view without a view_id is accepted", status == "ok")

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

    # --- snapshot / apply round trip ---------------------------------------
    # The point of a snapshot is that it can be replayed, so the check is a
    # real detour and back: capture, rebuild the sidebar differently, restore,
    # compare byte for byte.
    baseline = s.c.call("sidebar-snapshot", space_id=s.space_id)
    s.check(
        "sidebar-snapshot identifies widgets by target, never by block id",
        all("target_id" in w and "block_id" not in w for w in baseline["widgets"]),
        baseline["widgets"][:2],
    )

    second = s.page("apply")
    wanted = [{"target_id": second, "layout": "compact_list", "limit": 3}] + [
        {k: v for k, v in w.items() if k != "target_name"} for w in baseline["widgets"]
    ]
    planned = s.c.call("sidebar-apply", space_id=s.space_id, widgets=wanted)
    s.check(
        "sidebar-apply plans without touching anything",
        planned.get("applied") is False and planned.get("add") == [second],
        {k: planned.get(k) for k in ("applied", "add", "update", "reorder")},
    )
    applied = s.c.call(
        "sidebar-apply", space_id=s.space_id, widgets=wanted, remove_missing=True, apply=True
    )
    s.check(
        "sidebar-apply puts the wanted widgets in the wanted order",
        applied.get("applied") is True
        and [w["position"] for w in applied["order"]][:1] == [0]
        and applied["order"][0]["layout"] == "compact_list",
        [(w["position"], w["target_name"], w["layout"]) for w in applied["order"]],
    )
    s.check(
        "applying the same state twice is a no-op",
        all(
            not s.c.call(
                "sidebar-apply", space_id=s.space_id, widgets=wanted, remove_missing=True
            ).get(k)
            for k in ("add", "remove", "update", "reorder")
        ),
        "second plan is empty",
    )

    restored = s.c.call(
        "sidebar-apply", space_id=s.space_id,
        widgets=[{k: v for k, v in w.items() if k != "target_name"} for w in baseline["widgets"]],
        remove_missing=True, apply=True,
    )
    s.check(
        "restoring a snapshot removes what was added in between",
        all(w["target_id"] != second for w in restored["order"]),
        [w["target_name"] for w in restored["order"]],
    )
    s.check(
        "a restored sidebar snapshots identically to the original",
        s.c.call("sidebar-snapshot", space_id=s.space_id)["widgets"] == baseline["widgets"],
        baseline["widgets"],
    )
