"""object-render: the object as the GUI shows it, through the anytype-render daemon.

Needs the daemon running and ANYTYPE_RENDER_URL set in the launcher. Without
it the tool only says so, and the suite checks exactly that and stops.
"""


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

    again = s.c.call("object-render", space_id=s.space_id, object_id=obj)
    s.check("a repeated call returns the same link without rendering again", again.get("uri") == res.get("uri"),
            (again.get("uri"), res.get("uri")))

    opened = s.c.call("object-render", space_id=s.space_id, object_id=obj, expand_toggles=True)
    s.check("open toggles make the render taller",
            opened.get("height_px", 0) > res.get("height_px", 0), (opened.get("height_px"), res.get("height_px")))
    s.check("every toggle opened, the nested one too", "warnings" not in opened, opened.get("warnings"))
    s.check("a full-height render holds the whole object",
            opened.get("complete") is True and "incomplete_reason" not in opened
            and opened["layout"]["content_height_px"] <= opened["height_px"], opened)

    long_page = s.page("render lang")
    s.c.call("block-paste", space_id=s.space_id, object_id=long_page,
             markdown="\n\n".join(f"Absatz {i}, damit die Seite länger als ein Bildschirm wird." for i in range(40)))
    s.settle(2)
    first = s.c.call("object-render", space_id=s.space_id, object_id=long_page, full_height=False, width=480)
    s.check("the first screen of a long object says it is not all of it, and why",
            first.get("complete") is False and "first screen" in first.get("incomplete_reason", "")
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
            and any("all_rows" in w for w in some.get("warnings", [])), (views, some.get("warnings")))
    s.check("image false returns the report without an image", "uri" not in some, some.get("uri"))
    every = s.c.call("object-render", space_id=s.space_id, object_id=host, all_rows=True)
    rows = ((every.get("layout") or {}).get("dataviews") or [{}])[0]
    s.check("all_rows shows every row", rows.get("rows_shown") == 12 and "warnings" not in every,
            (rows, every.get("warnings")))

    dark = s.c.call("object-render", space_id=s.space_id, object_id=obj, theme="dark", width=800, format="png")
    s.check("theme, width and format are applied",
            dark.get("theme") == "dark" and dark.get("width_px") == 800 and str(dark.get("uri", "")).endswith(".png"), dark)

    status, msg = s.c.try_call("object-render", space_id=s.space_id, object_id="kein-solches-objekt")
    s.check("an object that does not exist is an error, not an empty picture", status == "err", msg)
    s.check("the error says plainly that the object is not there", "object not found in this space" in msg, msg)
