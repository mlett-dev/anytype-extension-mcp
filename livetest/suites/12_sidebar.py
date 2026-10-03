"""The sidebar: a space's derived system objects, and the widget object.

The widget object is SHARED — every member of the space sees it — so this suite
only ever adds a widget it created itself, through an import, and removes it in
the same run. It never touches a widget it did not make.
"""

import pathlib

from harness import IN_ROOT


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
        "every widget reports both block ids and a status",
        all(w.get("block_id") and w.get("wrapper_id") and w.get("status")
            for w in every.get("widgets", [])),
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
        dry.get("applied") is False and dry.get("selected") == dry.get("deleted"),
        {k: dry.get(k) for k in ("applied", "selected", "deleted")},
    )
    after_dry = s.c.call("sidebar-inspect", space_id=s.space_id, filter="all")
    s.check(
        "a dry run changes nothing",
        after_dry.get("total") == every.get("total"),
        [every.get("total"), after_dry.get("total")],
    )
    status, msg = s.c.try_call(
        "sidebar-prune", space_id=s.space_id, block_ids=["nichtvorhanden"], apply=True
    )
    s.check("a block id that is not a widget is refused", status == "err", msg)

    # --- a widget this run made, removed again -----------------------------
    # object-import is the only way to add a widget until the write tools land,
    # and keep_widget=True is what makes the import leave one behind.
    staging = pathlib.Path(IN_ROOT) / "livetest-sidebar"
    try:
        staging.mkdir(parents=True, exist_ok=True)
        (staging / "widget-probe.md").write_text(
            "# LIVETEST Widgetsonde\n\nEin Satz.\n", encoding="utf-8"
        )
        before = {w["block_id"] for w in every.get("widgets", [])}
        s.c.call(
            "object-import", space_id=s.space_id, type="markdown",
            paths=["livetest-sidebar"], collection_title=s.probe("widget"),
            keep_widget=True,
        )
        s.settle(3.0)
        with_widget = s.c.call("sidebar-inspect", space_id=s.space_id, filter="all")
        fresh = [w for w in with_widget.get("widgets", []) if w["block_id"] not in before]
        s.check(
            "keep_widget leaves the import's widget in the sidebar",
            len(fresh) == 1,
            [w["target_name"] for w in fresh],
        )
        if fresh:
            s.track(fresh[0]["target_id"])
            removed = s.c.call(
                "sidebar-prune", space_id=s.space_id,
                block_ids=[fresh[0]["block_id"]], apply=True,
            )
            s.check(
                "sidebar-prune removes a named widget and nothing else",
                removed.get("applied") is True
                and removed.get("total_after") == with_widget.get("total") - 1,
                {k: removed.get(k) for k in ("applied", "removed", "total_after")},
            )
            # Removing the inner link has to take the wrapper with it. A left
            # over wrapper is a block with no children, which inspect reports
            # as an orphan — the failure mode of deleting the wrong block id.
            s.check(
                "removing a widget leaves no empty wrapper behind",
                not removed.get("orphan_block_ids"),
                removed.get("orphan_block_ids"),
            )
    finally:
        for path in (staging / "widget-probe.md", staging):
            try:
                path.rmdir() if path.is_dir() else path.unlink()
            except OSError:
                pass

    # Also catch the imported page itself, which the runner would otherwise
    # leave behind: it is named by heart, not by probe().
    hit = s.c.call("search-space-compact", space_id=s.space_id, query="LIVETEST Widgetsonde")
    s.track(*[o["id"] for o in (hit.get("objects") or [])])
