"""Queries and collections: sources, views, filters, sorts, columns, ordering.

The subtle one is columns. A view keeps an entry for every property the
dataview knows about; "visible" is what makes an entry a column. Removing an
entry does not stick, because heart re-adds it hidden on the next change — so
query-view-update shows and hides rather than adds and removes.
"""

import json


def view_by_id(s, obj, view_id):
    info = s.c.call("query-inspect", space_id=s.space_id, object_id=obj)
    return next(v for v in info["views"] if v["id"] == view_id)


def visible_columns(view):
    return [r["property_key"] for r in view.get("relations", []) if r.get("visible")]


def column_width(view, key):
    return next((r.get("width") for r in view.get("relations", []) if r["property_key"] == key), None)


def run(s):
    task = s.type_by_key("task") or s.type_by_key("page")
    page_type = s.type_by_key("page")

    created = s.c.call(
        "query-create", space_id=s.space_id, name=s.probe("query"), source=[task["id"]]
    )
    obj, view = created["object_id"], created["views"][0]["id"]
    s.track(obj)
    s.check("query-create returns a dataview with a view", bool(obj and view), created.get("views"))

    info = s.c.call("query-inspect", space_id=s.space_id, object_id=obj)
    s.check("the source is reported from the setOf detail", info["source"] == [task["id"]], info["source"])
    s.c.call("query-set-source", space_id=s.space_id, object_id=obj, source=[page_type["id"]])
    s.check(
        "query-set-source replaces the source",
        s.c.call("query-inspect", space_id=s.space_id, object_id=obj)["source"] == [page_type["id"]],
    )
    s.c.call("query-set-source", space_id=s.space_id, object_id=obj, source=[task["id"]])

    # --- columns are visibility, not membership ---------------------------
    default_columns = len(view_by_id(s, obj, view)["relations"])
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=obj, view_id=view,
        name="Schmal", relations=[{"property_key": "name"}, {"property_key": "tag"}],
    )
    v = view_by_id(s, obj, view)
    s.check("query-view-update applies the name", v["name"] == "Schmal", v["name"])
    s.check(
        "query-view-update sets exactly the requested visible columns",
        visible_columns(v) == ["name", "tag"],
        visible_columns(v),
    )
    s.check(
        "hidden properties stay listed rather than vanishing",
        len(v["relations"]) >= default_columns,
        len(v["relations"]),
    )

    s.c.call("query-view-update", space_id=s.space_id, object_id=obj, view_id=view, name="Schmal2")
    s.check(
        "omitting relations leaves the columns untouched",
        visible_columns(view_by_id(s, obj, view)) == ["name", "tag"],
        visible_columns(view_by_id(s, obj, view)),
    )

    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=obj, view_id=view,
        relations=[{"property_key": "tag"}, {"property_key": "name"}, {"property_key": "done"}],
    )
    s.check(
        "the requested column order is applied",
        visible_columns(view_by_id(s, obj, view)) == ["tag", "name", "done"],
        visible_columns(view_by_id(s, obj, view)),
    )

    # --- filters and sorts -------------------------------------------------
    s.c.call(
        "query-filter-add", space_id=s.space_id, object_id=obj, view_id=view,
        property_key="done", condition="equal", value=False, format="checkbox",
    )
    filters = view_by_id(s, obj, view)["filters"]
    s.check("query-filter-add adds a filter", len(filters) == 1, filters)
    if filters:
        s.c.call(
            "query-filter-remove", space_id=s.space_id, object_id=obj,
            view_id=view, filter_ids=[filters[0]["id"]],
        )
        s.check("query-filter-remove removes it", view_by_id(s, obj, view)["filters"] == [])

    sorts_before = len(view_by_id(s, obj, view)["sorts"])
    s.c.call(
        "query-sort-add", space_id=s.space_id, object_id=obj, view_id=view,
        property_key="name", direction="desc",
    )
    sorts = view_by_id(s, obj, view)["sorts"]
    s.check("query-sort-add adds a sort", len(sorts) == sorts_before + 1, sorts)
    s.c.call(
        "query-sort-remove", space_id=s.space_id, object_id=obj, view_id=view,
        sort_ids=[sorts[-1]["id"]],
    )
    s.check(
        "query-sort-remove removes it",
        len(view_by_id(s, obj, view)["sorts"]) == sorts_before,
    )

    # --- extra views -------------------------------------------------------
    s.c.call(
        "query-view-create", space_id=s.space_id, object_id=obj,
        name="Board", layout="kanban", group_property_key="done",
    )
    info = s.c.call("query-inspect", space_id=s.space_id, object_id=obj)
    board = next((v for v in info["views"] if v["name"] == "Board"), None)
    s.check("a kanban view is created with its layout", board and board["layout"] == "kanban", board)
    s.check(
        "the kanban grouping property is applied",
        board and board.get("group_property_key") == "done",
        board.get("group_property_key") if board else None,
    )

    s.c.call("query-view-arrange", space_id=s.space_id, object_id=obj, view_id=board["id"], position=0)
    order = [v["id"] for v in s.c.call("query-inspect", space_id=s.space_id, object_id=obj)["views"]]
    s.check("query-view-arrange moves a view to the front", order[0] == board["id"], order)

    s.c.call("query-view-delete", space_id=s.space_id, object_id=obj, view_id=board["id"])
    order = [v["id"] for v in s.c.call("query-inspect", space_id=s.space_id, object_id=obj)["views"]]
    s.check("query-view-delete removes the view", board["id"] not in order, order)

    # --- an update writes what it was given and nothing else ---------------
    # BlockDataviewViewUpdate replaces the whole view: heart's SetViewFields
    # copies every simple field of the struct it is handed. A spec built from
    # optional arguments therefore used to write empty strings and zeroes over
    # the stored values — the view lost its name (the GUI showed Untitled) and,
    # with no visible complaint at all, a kanban became a table because layout 0
    # is Table, losing its grouping in the same call.
    keep = s.c.call(
        "query-view-create", space_id=s.space_id, object_id=obj,
        name="Behalten", layout="kanban", group_property_key="done", page_limit=17,
        relations=[{"property_key": "name", "width": 240}],
    )["view_id"]
    s.check(
        "the view under test starts out fully configured",
        [view_by_id(s, obj, keep).get(k) for k in ("name", "layout", "group_property_key", "page_limit")]
        == ["Behalten", "kanban", "done", 17],
        view_by_id(s, obj, keep),
    )

    for label, kwargs in (
        ("columns", {"relations": [{"property_key": "name"}, {"property_key": "done"}]}),
        ("filters", {"filters": [{"property_key": "done", "condition": "equal",
                                  "value": False, "format": "checkbox"}]}),
        ("sorts", {"sorts": [{"property_key": "name", "direction": "asc"}]}),
    ):
        s.c.call("query-view-update", space_id=s.space_id, object_id=obj, view_id=keep, **kwargs)
        v = view_by_id(s, obj, keep)
        s.check(
            f"a {label}-only update keeps name, layout, grouping and page limit",
            [v.get(k) for k in ("name", "layout", "group_property_key", "page_limit")]
            == ["Behalten", "kanban", "done", 17],
            {k: v.get(k) for k in ("name", "layout", "group_property_key", "page_limit")},
        )

    s.c.call("query-view-update", space_id=s.space_id, object_id=obj, view_id=keep, name="Umbenannt")
    v = view_by_id(s, obj, keep)
    s.check(
        "renaming a view does not flatten its layout or drop its grouping",
        [v.get(k) for k in ("name", "layout", "group_property_key")] == ["Umbenannt", "kanban", "done"],
        {k: v.get(k) for k in ("name", "layout", "group_property_key")},
    )

    # The other half of the contract: what IS passed still gets written.
    s.c.call("query-view-update", space_id=s.space_id, object_id=obj, view_id=keep,
             layout="table", page_limit=5)
    v = view_by_id(s, obj, keep)
    s.check(
        "an explicitly passed layout and page limit are applied",
        [v.get(k) for k in ("layout", "page_limit", "name")] == ["table", 5, "Umbenannt"],
        {k: v.get(k) for k in ("layout", "page_limit", "name")},
    )

    # Widths go the same way: the RPC replaces the whole relation entry and a
    # zero is stored verbatim, so an unstated width has to be carried over.
    s.check(
        "a stated column width is stored",
        column_width(view_by_id(s, obj, keep), "name") == 240,
        column_width(view_by_id(s, obj, keep), "name"),
    )
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=obj, view_id=keep,
        relations=[{"property_key": "name"}, {"property_key": "tag"}],
    )
    s.check(
        "a column listed without a width keeps the width it had",
        column_width(view_by_id(s, obj, keep), "name") == 240,
        column_width(view_by_id(s, obj, keep), "name"),
    )
    s.c.call("query-view-delete", space_id=s.space_id, object_id=obj, view_id=keep)

    # --- manual order is stored, though only the clients render it --------
    targets = [s.page(f"order{i}") for i in range(3)]
    wanted = [targets[2], targets[0], targets[1]]
    s.c.call(
        "query-order-set", space_id=s.space_id, object_id=obj, view_id=view, object_ids=wanted
    )
    orders = s.c.call("query-inspect", space_id=s.space_id, object_id=obj).get("object_orders", [])
    s.check("the manual order is stored and readable", orders and orders[0]["object_ids"] == wanted, orders)
    s.c.call(
        "query-order-set", space_id=s.space_id, object_id=obj, view_id=view, object_ids=[targets[0]]
    )
    orders = s.c.call("query-inspect", space_id=s.space_id, object_id=obj)["object_orders"]
    s.check(
        "setting the order again replaces it instead of appending",
        len(orders) == 1 and orders[0]["object_ids"] == [targets[0]],
        orders,
    )

    # --- collections -------------------------------------------------------
    coll = s.c.call("object-to-collection", space_id=s.space_id, object_id=s.page("sammlung"))
    coll_id = coll["object_id"]
    items = [s.page(f"item{i}") for i in range(2)]
    s.c.call("add-list-objects", space_id=s.space_id, list_id=coll_id, objects=items)
    s.settle()
    views = s.c.call("get-list-views-compact", space_id=s.space_id, list_id=coll_id)
    vid = (views.get("views") or [{}])[0].get("id")
    rows = s.c.call(
        "get-list-objects-compact", space_id=s.space_id, list_id=coll_id,
        view_id=vid, fields=["id", "name"],
    )
    s.check(
        "add-list-objects puts both objects in the collection",
        len(rows.get("objects") or []) == 2,
        rows.get("objects"),
    )
    s.c.call("remove-list-object", space_id=s.space_id, list_id=coll_id, object_id=items[0])
    s.settle()
    rows = s.c.call(
        "get-list-objects-compact", space_id=s.space_id, list_id=coll_id,
        view_id=vid, fields=["id", "name"],
    )
    s.check(
        "remove-list-object removes exactly one",
        len(rows.get("objects") or []) == 1,
        rows.get("objects"),
    )

    conv = s.c.call(
        "object-to-query", space_id=s.space_id, object_id=s.page("wirdquery"),
        source=[task["id"]],
    )
    s.track(conv["object_id"])
    s.check(
        "object-to-query converts and sets the source",
        s.c.call("query-inspect", space_id=s.space_id, object_id=conv["object_id"])["source"]
        == [task["id"]],
    )

    # --- property keys must reach the index in the spelling it uses --------
    # Anytype names a property twice: due_date / linked_projects through REST,
    # dueDate / linkedProjects in the index and in every dataview. A filter
    # written the REST way is stored and echoed back unchanged and then matches
    # nothing, which is why this is checked on results rather than on the
    # stored filter.
    area = s.page("filterziel")
    linked = s.page("verlinkt", type_key="task")
    unlinked = s.page("unverlinkt", type_key="task")
    s.c.call(
        "update-object-compact", space_id=s.space_id, object_id=linked,
        properties=[{"key": "linked_projects", "objects": [area]}],
    )
    s.settle(2.0)

    task_type = s.type_by_key("task")
    relq = s.c.call(
        "query-create", space_id=s.space_id, name=s.probe("relq"), source=[task_type["id"]]
    )
    relq_id, relq_view = relq["object_id"], relq["views"][0]["id"]
    s.track(relq_id)
    s.settle(2.0)

    def rows():
        result = s.c.call(
            "get-list-objects-compact", space_id=s.space_id, list_id=relq_id,
            view_id=relq_view, fields=["id", "name"], limit=100,
        )
        return [o["id"] for o in (result.get("objects") or [])]

    for spelling in ("linked_projects", "linkedProjects"):
        s.c.call(
            "query-view-update", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
            filters=[{"property_key": spelling, "condition": "in",
                      "value": [area], "format": "objects"}],
        )
        s.settle(2.0)
        found = rows()
        s.check(
            f"an object-relation filter written as {spelling} actually matches",
            linked in found and unlinked not in found,
            found,
        )

    # A sort on the REST spelling has no visible symptom at all, so the check is
    # that the stored sort carries the key the index understands.
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
        filters=[], sorts=[{"property_key": "due_date", "direction": "asc", "format": "date"}],
    )
    stored = view_by_id(s, relq_id, relq_view)
    s.check(
        "a sort given the REST spelling is stored under the index spelling",
        [x["property_key"] for x in stored["sorts"]] == ["dueDate"],
        stored["sorts"],
    )
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
        relations=[{"property_key": "name"}, {"property_key": "due_date"}, {"property_key": "done"}],
    )
    stored = view_by_id(s, relq_id, relq_view)
    s.check(
        "columns given the REST spelling are stored under the index spelling",
        visible_columns(stored) == ["name", "dueDate", "done"],
        visible_columns(stored),
    )
    status, msg = s.c.try_call(
        "query-filter-add", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
        property_key="gibt_es_nicht", condition="equal", value=1,
    )
    s.check(
        "an unknown property key is refused instead of silently matching nothing",
        status == "err" and "no property" in msg,
        msg,
    )

    # --- configuration that saves but cannot work is refused ---------------
    # An operator on a filter is not a connective: heart reads it as "this
    # filter is a GROUP", drops property, condition and value, and leaves an
    # empty group behind that constrains nothing while the write reports
    # success. Writing one has to fail loudly.
    status, msg = s.c.try_call(
        "query-filter-add", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
        property_key="done", condition="equal", value=False, operator="and",
    )
    s.check(
        "a filter operator is refused instead of quietly disabling the filter",
        status == "err" and "operator" in msg,
        msg,
    )
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
        filters=[{"property_key": "done", "condition": "equal", "value": False,
                  "format": "checkbox"}],
    )
    s.check(
        "a filter written without an operator is stored as operator=no",
        [f.get("operator") for f in view_by_id(s, relq_id, relq_view)["filters"]] == ["no"],
        view_by_id(s, relq_id, relq_view)["filters"],
    )

    # Anytype groups a board by select, multi-select or checkbox only. Any
    # other format saves and then renders an empty board, so it is refused.
    status, msg = s.c.try_call(
        "query-view-create", space_id=s.space_id, object_id=relq_id,
        name="Brett", layout="kanban", group_property_key="linked_projects",
    )
    s.check(
        "a kanban grouped by an object property is refused",
        status == "err" and "kanban" in msg,
        msg,
    )
    status, msg = s.c.try_call(
        "query-view-create", space_id=s.space_id, object_id=relq_id,
        name="Brett", layout="kanban",
    )
    s.check("a kanban without a grouping property is refused", status == "err", msg)

    # A column the dataview block does not know about has no schema behind it
    # and the client cannot render it. heart never derives the block's relation
    # links from a view, so the tools have to register them.
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
        relations=[{"property_key": "name"}, {"property_key": "linked_projects"}],
    )
    exported = json.loads(
        s.c.call("object-export", space_id=s.space_id, object_id=relq_id, format="json")["content"]
    )
    links = [
        link["key"]
        for block in exported["snapshot"]["data"]["blocks"]
        if block.get("dataview")
        for link in (block["dataview"].get("relationLinks") or [])
    ]
    s.check(
        "a visible column is registered on the dataview block itself",
        "linkedProjects" in links,
        links,
    )

    # --- the defaults a view hands to newly created objects ---------------
    tpl = s.c.call("template-create", space_id=s.space_id, object_id=linked)["template_id"]
    s.track(tpl)
    s.settle(1.5)
    s.c.call(
        "query-view-update", space_id=s.space_id, object_id=relq_id, view_id=relq_view,
        default_object_type_id=task_type["id"], default_template_id=tpl,
    )
    stored = view_by_id(s, relq_id, relq_view)
    s.check(
        "a view's default type and template are stored and readable again",
        stored.get("default_object_type_id") == task_type["id"]
        and stored.get("default_template_id") == tpl,
        {k: stored.get(k) for k in ("default_object_type_id", "default_template_id")},
    )

    # --- the server can say what it is ------------------------------------
    info = s.c.call("server-info")
    s.check(
        "server-info reports this server's version",
        info.get("server") == "anytype-extension-mcp" and info.get("version"),
        info.get("version"),
    )
    s.check(
        "its tool count matches what tools/list actually offers",
        info.get("tool_count") == len(s.c.tools),
        f"{info.get('tool_count')} vs {len(s.c.tools)}",
    )
    s.check(
        "it also reports the Anytype server it is connected to",
        bool(info.get("anytype_version")),
        info.get("anytype_version"),
    )

    # The spelling rule has to be findable by a caller, not only in the repo.
    definitions = {t["name"]: t for t in s.c.rpc("tools/list")["result"]["tools"]}
    for tool in ("query-filter-add", "query-sort-add", "query-inspect"):
        text = definitions[tool]["description"] + str(definitions[tool]["inputSchema"])
        s.check(
            f"{tool} explains the two property-key spellings",
            "dueDate" in text,
            definitions[tool]["description"][-80:],
        )
    for tool in ("query-filter-add", "query-view-create", "query-view-update"):
        s.check(
            f"{tool} no longer offers a filter operator",
            "operator" not in str(definitions[tool]["inputSchema"]),
            tool,
        )
