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

    dark = s.c.call("object-render", space_id=s.space_id, object_id=obj, theme="dark", width=800, format="png")
    s.check("theme, width and format are applied",
            dark.get("theme") == "dark" and dark.get("width_px") == 800 and str(dark.get("uri", "")).endswith(".png"), dark)

    status, msg = s.c.try_call("object-render", space_id=s.space_id, object_id="kein-solches-objekt")
    s.check("an object that does not exist is an error, not an empty picture", status == "err", msg)
