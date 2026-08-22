"""Tables: the grid, filling cells, and reshaping rows and columns.

block-create with kind=table only lays out an empty grid — the cell blocks come
into existence on first write, and anytype-heart removes them again when they
are emptied. table-inspect therefore always reports the full row x column shape
with "exists" marking the holes.
"""


def grid(result):
    return {(c["row"], c["column"]): c.get("text", "") for c in result["cells"]}


def visible_rows(result):
    return [r["index"] for r in result["rows"]]


def run(s):
    obj = s.page("tabelle")
    table = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj, kind="table",
        rows=3, columns=3, with_header_row=True,
    )["block_id"]

    info = s.c.call("table-inspect", space_id=s.space_id, object_id=obj)
    s.check(
        "table-inspect finds the only table without being told which",
        info["table_block_id"] == table and (info["row_count"], info["column_count"]) == (3, 3),
        info["table_block_id"],
    )
    s.check(
        "the header row is reported as such",
        [r["index"] for r in info["rows"] if r.get("is_header")] == [0],
        info["rows"],
    )

    # --- filling ----------------------------------------------------------
    res = s.c.call(
        "table-set-cells", space_id=s.space_id, object_id=obj,
        cells=[{"row": r, "column": c, "text": f"r{r}c{c}"} for r in range(3) for c in range(3)],
    )
    g = grid(res)
    s.check(
        "every written cell is readable straight away",
        all(g.get((r, c)) == f"r{r}c{c}" for r in range(3) for c in range(3)),
        g,
    )

    # --- inserting --------------------------------------------------------
    res = s.c.call("table-insert", space_id=s.space_id, object_id=obj, axis="row", count=2)
    s.check("appending rows without a target", res["row_count"] == 5, res["row_count"])
    # Rows take top/bottom underneath, columns take left/right; the tool speaks
    # before/after and translates per axis.
    res = s.c.call(
        "table-insert", space_id=s.space_id, object_id=obj,
        axis="column", target=0, position="before",
    )
    s.check("inserting a column before another", res["column_count"] == 4, res["column_count"])

    # --- clearing keeps the shape ----------------------------------------
    res = s.c.call("table-row-clear", space_id=s.space_id, object_id=obj, indices=[1])
    row1 = [c for c in res["cells"] if c["row"] == 1]
    s.check(
        "clearing a row empties it but keeps the grid shape",
        len(row1) == res["column_count"] and all(c.get("text", "") == "" for c in row1),
        row1,
    )
    res = s.c.call(
        "table-set-cells", space_id=s.space_id, object_id=obj,
        cells=[{"row": 1, "column": c, "text": f"neu{c}"} for c in range(res["column_count"])],
    )
    g = grid(res)
    s.check(
        "a cleared row can be written again",
        all(g.get((1, c)) == f"neu{c}" for c in range(res["column_count"])),
        [g.get((1, c)) for c in range(res["column_count"])],
    )

    # --- reshaping --------------------------------------------------------
    res = s.c.call(
        "table-duplicate", space_id=s.space_id, object_id=obj, axis="row", target=1
    )
    s.check("duplicating a row", res["row_count"] == 6, res["row_count"])

    before = grid(s.c.call("table-inspect", space_id=s.space_id, object_id=obj))
    res = s.c.call(
        "table-move", space_id=s.space_id, object_id=obj,
        axis="column", target=0, drop_target=2, position="after",
    )
    after = grid(res)
    s.check(
        "moving a column changes the column order",
        [before.get((0, c)) for c in range(res["column_count"])]
        != [after.get((0, c)) for c in range(res["column_count"])],
        (
            [before.get((0, c)) for c in range(res["column_count"])],
            [after.get((0, c)) for c in range(res["column_count"])],
        ),
    )

    res = s.c.call(
        "table-row-header", space_id=s.space_id, object_id=obj, indices=[0], is_header=False
    )
    s.check("header can be turned off", not any(r.get("is_header") for r in res["rows"]))
    res = s.c.call(
        "table-row-header", space_id=s.space_id, object_id=obj, indices=[0], is_header=True
    )
    s.check("header can be turned back on", res["rows"][0].get("is_header") is True)

    res = s.c.call("table-sort", space_id=s.space_id, object_id=obj, column=0, direction="asc")
    s.check("sorting reports success and keeps the shape", res["row_count"] == 6, res["row_count"])

    rows_before = res["row_count"]
    res = s.c.call(
        "table-delete", space_id=s.space_id, object_id=obj, axis="row", indices=[2, 3]
    )
    s.check(
        "deleting two rows by index removes exactly two",
        res["row_count"] == rows_before - 2,
        res["row_count"],
    )
    cols_before = res["column_count"]
    res = s.c.call("table-delete", space_id=s.space_id, object_id=obj, axis="column", indices=[0])
    s.check("deleting a column", res["column_count"] == cols_before - 1, res["column_count"])

    # --- error messages must name parameters the schema accepts -----------
    status, msg = s.c.try_call(
        "table-set-cells", space_id=s.space_id, object_id=obj,
        cells=[{"row": 99, "column": 0, "text": "x"}],
    )
    s.check("out-of-range row is refused with a helpful message",
            status == "err" and "out of range" in msg, msg)
    status, msg = s.c.try_call(
        "table-delete", space_id=s.space_id, object_id=obj, axis="row"
    )
    s.check(
        "delete without targets names ids/indices, the real parameters",
        status == "err" and "ids" in msg and "indices" in msg,
        msg,
    )
    status, msg = s.c.try_call(
        "table-duplicate", space_id=s.space_id, object_id=obj, axis="row"
    )
    s.check(
        "duplicate without a target names target_id/target",
        status == "err" and "target" in msg,
        msg,
    )
    status, msg = s.c.try_call(
        "table-inspect", space_id=s.space_id, object_id=obj, table_block_id="gibtsnicht"
    )
    s.check("an unknown table block id is refused", status == "err", msg)
