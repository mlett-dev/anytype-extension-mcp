"""object-render: the object as the GUI shows it, through the anytype-render daemon.

Needs the daemon running and ANYTYPE_RENDER_URL set in the launcher. Without
it the tool only says so, and the suite checks exactly that and stops.
"""


def listed_view(s, obj):
    """The id of the view the object's dataview shows first."""
    return s.c.call("query-inspect", space_id=s.space_id, object_id=obj)["views"][0]["id"]


def run(s):
    obj = s.page("render")
    status, msg = s.c.try_call("object-render", space_id=s.space_id, object_id=obj)
    if status == "err" and "no renderer configured" in msg:
        s.check("without a renderer the tool says how to set one up", "ANYTYPE_RENDER_URL" in msg, msg)
        return

    s.c.call("block-create", space_id=s.space_id, object_id=obj, style="toggle", text="Abschnitt")
    toggle = [b for b in s.blocks(obj) if b.get("style") == "toggle"][0]["id"]
    for i in range(6):
        s.c.call("block-create", space_id=s.space_id, object_id=obj, target_id=toggle,
                 position="inner", text=f"Zeile {i} im Toggle")
    nested = s.c.call("block-create", space_id=s.space_id, object_id=obj, target_id=toggle,
                      position="inner", style="toggle", text="Unterabschnitt")["block_id"]
    s.c.call("block-create", space_id=s.space_id, object_id=obj, target_id=nested,
             position="inner", text="im inneren Toggle")
    s.c.call("block-create", space_id=s.space_id, object_id=obj, kind="table", rows=2, columns=2)
    s.c.call("table-set-cells", space_id=s.space_id, object_id=obj, cells=[
        {"row": 0, "column": 0, "text": "kurz"},
        {"row": 0, "column": 1, "text": "ein Zelltext, der in die schmale Spalte nicht passt"},
        {"row": 1, "column": 0, "text": "x"}, {"row": 1, "column": 1, "text": "y"}])
    s.c.call("table-column-width", space_id=s.space_id, object_id=obj, columns=[
        {"column": 0, "width_px": 200}, {"column": 1, "width_px": 120}])
    s.settle(2)

    res = s.c.call("object-render", space_id=s.space_id, object_id=obj)
    tables = (res.get("layout") or {}).get("tables") or []
    s.check("the render reports the table at the widths that were set",
            len(tables) == 1 and tables[0].get("column_widths_px") == [200, 120], tables)
    s.check("the long cell in the narrow column is reported as wrapped",
            tables and tables[0].get("cells_wrapped") == 1, tables)
    s.check("the image comes as an export link", str(res.get("uri", "")).startswith("anytype-export://"), res.get("uri"))
    s.check("nothing the page needed was refused", "warnings" not in res, res.get("warnings"))
    s.check("the layout is named", (res.get("layout") or {}).get("layout") == "edit", res.get("layout"))
    s.check("closed toggles with content make the render incomplete, and say how to open them",
            res.get("complete") is False
            and any("closed toggles with content: 1 (expand_toggles)" in r for r in res.get("incomplete_reasons", [])),
            res.get("incomplete_reasons"))

    again = s.c.call("object-render", space_id=s.space_id, object_id=obj)
    s.check("a repeated call returns the same link without rendering again", again.get("uri") == res.get("uri"),
            (again.get("uri"), res.get("uri")))

    opened = s.c.call("object-render", space_id=s.space_id, object_id=obj, expand_toggles=True)
    s.check("open toggles make the render taller",
            opened.get("height_px", 0) > res.get("height_px", 0), (opened.get("height_px"), res.get("height_px")))
    s.check("every toggle opened, the nested one too", "warnings" not in opened, opened.get("warnings"))
    s.check("a full-height render holds the whole object",
            opened.get("complete") is True and "incomplete_reasons" not in opened
            and opened["layout"]["content_height_px"] <= opened["height_px"], opened)

    long_page = s.page("render lang")
    s.c.call("block-paste", space_id=s.space_id, object_id=long_page,
             markdown="\n\n".join(f"Absatz {i}, damit die Seite länger als ein Bildschirm wird." for i in range(40)))
    s.settle(2)
    first = s.c.call("object-render", space_id=s.space_id, object_id=long_page, full_height=False, width=480)
    s.check("the first screen of a long object says it is not all of it, and why",
            first.get("complete") is False and any("first screen" in r for r in first.get("incomplete_reasons", []))
            and first["layout"]["content_height_px"] > first["height_px"], first)

    # Embedded, a collection of 12 shows its first 10 rows above Load more.
    items = [s.page(f"render eintrag {i}") for i in range(12)]
    coll = s.c.call("object-to-collection", space_id=s.space_id, object_id=s.page("render sammlung"))["object_id"]
    s.c.call("add-list-objects", space_id=s.space_id, list_id=coll, objects=items)
    host = s.page("render eingebettet")
    s.c.call("block-embed-query", space_id=s.space_id, object_id=host, query_object_id=coll)
    s.settle(2)
    some = s.c.call("object-render", space_id=s.space_id, object_id=host, image=False)
    views = (some.get("layout") or {}).get("dataviews") or [{}]
    s.check("an embedded collection names what it shows: source and view",
            views[0].get("source_id") == coll and views[0].get("view_name") and isinstance(views[0].get("filters"), int), views)
    s.check("it shows the first rows of 12 and says all_rows loads the rest",
            views[0].get("rows_total") == 12 and views[0].get("rows_shown", 12) < 12
            and any("rows behind Load more: 2 (all_rows)" in r for r in some.get("incomplete_reasons", [])),
            (views, some.get("incomplete_reasons")))
    s.check("image false returns the report without an image", "uri" not in some, some.get("uri"))
    every = s.c.call("object-render", space_id=s.space_id, object_id=host, all_rows=True)
    rows = ((every.get("layout") or {}).get("dataviews") or [{}])[0]
    s.check("all_rows shows every row, and then the render is complete",
            rows.get("rows_shown") == 12 and every.get("complete") is True and "warnings" not in every,
            (rows, every.get("incomplete_reasons"), every.get("warnings")))

    # Another view, as a click on its tab shows it, without saving the switch.
    s.c.call("query-view-create", space_id=s.space_id, object_id=host, name="Liste", layout="list")
    before = s.c.call("query-inspect", space_id=s.space_id, object_id=host).get("active_view")
    listed = s.c.call("object-render", space_id=s.space_id, object_id=host, image=False, views=[{"view": "Liste"}])
    dv = ((listed.get("layout") or {}).get("dataviews") or [{}])[0]
    s.check("views shows another view, and the report lists the views there are",
            dv.get("view") == "list" and dv.get("view_name") == "Liste" and "Liste" in (dv.get("views") or []), dv)
    s.check("showing another view saves nothing",
            s.c.call("query-inspect", space_id=s.space_id, object_id=host).get("active_view") == before, before)
    status, msg = s.c.try_call("object-render", space_id=s.space_id, object_id=host, views=[{"view": "gibt es nicht"}])
    s.check("an unknown view is an error that names the views there are", status == "err" and "Liste" in msg, msg)

    # A filter with a value is applied, one without is stored and does
    # nothing; show_filters opens the bar that shows both.
    s.c.call("query-filter-add", space_id=s.space_id, object_id=host, view_id=listed_view(s, host),
             property_key="name", condition="like", value="eintrag")
    # A new filter without a value is refused, since it would filter nothing.
    # The app leaves such filters behind when a stored one loses its value, and
    # that is how this one is made: added with a value, then emptied by id.
    emptied = s.c.call("query-filter-add", space_id=s.space_id, object_id=host, view_id=listed_view(s, host),
                       property_key="description", condition="equal", value="x")["filter_id"]
    stored = s.c.call("query-inspect", space_id=s.space_id, object_id=host)["views"][0]["filters"]
    s.c.call("query-view-update", space_id=s.space_id, object_id=host, view_id=listed_view(s, host),
             filters=[dict(f, value="") if f["id"] == emptied else f for f in stored])
    s.settle(2)
    bar = s.c.call("object-render", space_id=s.space_id, object_id=host, image=False, show_filters=True)
    dv = ((bar.get("layout") or {}).get("dataviews") or [{}])[0]
    s.check("show_filters opens the filter bar, and the report counts only the applied filter",
            dv.get("filter_bar") is True and dv.get("filters") == 1 and dv.get("filters_inactive") == 1, dv)
    listed = dv.get("filter_list") or []
    s.check("filter_list names both filters as the GUI does and marks the empty one",
            len(listed) == 2 and listed[0] == 'Name: Contains "eintrag"' and "[filters nothing" in listed[1], listed)

    dark = s.c.call("object-render", space_id=s.space_id, object_id=obj, theme="dark", width=800, format="png")
    s.check("theme, width and format are applied",
            dark.get("theme") == "dark" and dark.get("width_px") == 800 and str(dark.get("uri", "")).endswith(".png"), dark)

    status, msg = s.c.try_call("object-render", space_id=s.space_id, object_id="kein-solches-objekt")
    s.check("an object that does not exist is an error, not an empty picture", status == "err", msg)
    s.check("the error says plainly that the object is not there", "object not found in this space" in msg, msg)

    # The properties panel: Local lists every key the object holds that its
    # type does not, empty ones too. done is no property of a page.
    host_page = s.page("panel")
    s.c.call("objects-modify-property", space_id=s.space_id, object_ids=[host_page],
             operations=[{"property_key": "done", "set": False}])
    panel = s.c.call("object-render", space_id=s.space_id, object_id=host_page, show_properties=True)
    sections = ((panel.get("layout") or {}).get("properties_panel") or {}).get("sections") or []
    local = [p for sec in sections if sec.get("section") == "local" for p in sec.get("properties", [])]
    s.check("show_properties lists done under Local while the page holds it",
            any(p.get("key") == "done" and p.get("value") == "unchecked" for p in local), sections)
    s.check("the panel needed nothing the renderer refused", "warnings" not in panel, panel.get("warnings"))
    s.c.call("objects-modify-property", space_id=s.space_id, object_ids=[host_page],
             operations=[{"property_key": "done", "set": None}])
    panel = s.c.call("object-render", space_id=s.space_id, object_id=host_page, show_properties=True, image=False)
    sections = ((panel.get("layout") or {}).get("properties_panel") or {}).get("sections") or []
    s.check("after set null done is gone from the panel",
            not any(p.get("key") == "done" for sec in sections for p in sec.get("properties", [])), sections)
