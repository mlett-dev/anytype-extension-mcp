"""Filters and sorts as the Anytype app stores and applies them.

The app is the measure: it drops a filter without a value before querying,
takes the format from the property and coerces the value (a number from "5", a
list from a single id). A filter written in any other shape gives one result in
the app and another through REST. Every check here therefore looks at the rows
a view returns, not only at what was stored.

A collection holding exactly four tasks keeps the row counts exact, whatever
else lives in the space.
"""

import secrets
import time

DAY = 86400


def iso(ts):
    return time.strftime("%Y-%m-%d", time.gmtime(ts))


def run(s):
    sfx = secrets.token_hex(2)
    props = s.c.call(
        "create-property", space_id=s.space_id, include_results=True,
        properties=[
            {"name": s.probe("Zahl"), "format": "number", "key": f"lt_zahl_{sfx}"},
            {"name": s.probe("Farben"), "format": "multi_select", "key": f"lt_farben_{sfx}"},
            {"name": s.probe("Termin"), "format": "date", "key": f"lt_termin_{sfx}"},
            {"name": s.probe("Weg"), "format": "text", "key": f"lt_weg_{sfx}"},
        ],
    )
    P = {}
    for r in props["results"]:
        p = r["result"]["property"]
        s.track(p["id"])
        P[p["name"].rsplit(" ", 1)[-1]] = p
    tsfx = P["Zahl"]["key"].split("_", 2)[2]
    T = {}
    for name in ("Rot", "Gruen", "Blau", "Lila", "Alt"):
        tag = s.c.call("create-tag", space_id=s.space_id, property_id=P["Farben"]["id"],
                       name=name, color="red", key=f"lt_{name.lower()}_{tsfx}")
        T[name] = (tag.get("tag") or tag)["id"]
        s.track(T[name])
    zahl, farben, termin = P["Zahl"]["key"], P["Farben"]["key"], P["Termin"]["key"]

    now = int(time.time())
    today = now - now % DAY
    O = {k: s.page(k, type_key="task") for k in ("A", "B", "C", "D")}
    values = {
        "A": [{"key": zahl, "number": 1}, {"key": farben, "multi_select": [T["Rot"]]},
              {"key": termin, "date": iso(today) + "T12:00:00Z"}],
        "B": [{"key": zahl, "number": 5}, {"key": farben, "multi_select": [T["Rot"], T["Gruen"]]},
              {"key": termin, "date": iso(today + DAY) + "T12:00:00Z"}],
        "C": [{"key": zahl, "number": 10}, {"key": farben, "multi_select": [T["Blau"]]},
              {"key": termin, "date": iso(today - 10 * DAY) + "T12:00:00Z"}],
    }
    for k, v in values.items():
        s.c.call("update-object-compact", space_id=s.space_id, object_id=O[k], properties=v)
    label = {v: k for k, v in O.items()}

    coll = s.c.call("object-to-collection", space_id=s.space_id, object_id=s.page("sammlung"))["object_id"]
    s.c.call("add-list-objects", space_id=s.space_id, list_id=coll, objects=list(O.values()))
    s.settle(2.0)
    view = s.c.call("query-inspect", space_id=s.space_id, object_id=coll)["views"][0]["id"]
    target = dict(space_id=s.space_id, object_id=coll, view_id=view)

    def inspect():
        return next(v for v in s.c.call("query-inspect", space_id=s.space_id, object_id=coll)["views"] if v["id"] == view)

    def rows():
        s.settle(1.0)
        got = s.c.call("get-list-objects-compact", space_id=s.space_id, list_id=coll, view_id=view,
                       fields=["id"], limit=50)
        return sorted(label.get(o["id"], "?") for o in (got.get("objects") or []))

    def clear():
        s.c.call("query-view-update", filters=[], sorts=[], **target)

    def one(label_, want_rows, **filter_args):
        """Add a filter, check the rows, remove it again; returns the stored filter."""
        status, res = s.c.try_call("query-filter-add", **target, **filter_args)
        if status != "ok":
            s.check(label_, False, res)
            return None
        got = rows()
        s.check(label_, got == want_rows, f"rows {got}, want {want_rows}; stored {res.get('filter')}")
        s.c.call("query-filter-remove", filter_ids=[res["filter_id"]], **target)
        return res.get("filter")

    clear()
    s.check("the collection holds the four tasks", rows() == ["A", "B", "C", "D"], rows())

    # --- values are stored as the app stores them -------------------------
    f = one("an ISO date matches that day", ["A"], property_key=termin, condition="equal", value=iso(today))
    s.check("the ISO date is stored as Unix seconds of UTC midnight", f and f.get("value") == today, f)
    one("an ISO date-time with offset works too", ["A"], property_key=termin, condition="equal",
        value=iso(today) + "T23:30:00+00:00")
    f = one("a number given as a string matches", ["B"], property_key=zahl, condition="equal", value="5")
    s.check("the number is stored as a number", f and f.get("value") == 5, f)
    f = one("a tag can be named instead of its id", ["A", "B"], property_key=farben, condition="in", value=["rot"])
    s.check("the tag name is stored as the tag id", f and f.get("value") == [T["Rot"]], f)
    one("a tag can be given by its key", ["C"], property_key=farben, condition="in", value=[f"lt_blau_{tsfx}"])
    f = one("a single id needs no list", ["C"], property_key=farben, condition="in", value=T["Blau"])
    s.check("the single id is stored as a list", f and f.get("value") == [T["Blau"]], f)
    f = one("the format comes from the property when omitted", ["A", "B"], property_key=farben,
            condition="in", value=[T["Rot"]])
    s.check("and is stored as the dataview's tag format", f and f.get("format") == "tag", f)

    for what, args, needle in [
        ("an unknown tag name", dict(property_key=farben, condition="in", value=["Pink"]), "Lila"),
        ("an empty list", dict(property_key=farben, condition="in", value=[]), "nothing"),
        ("a missing value", dict(property_key=zahl, condition="equal"), "nothing"),
        ("exists, which the app ignores", dict(property_key=zahl, condition="exists"), "not_empty"),
        ("a checkbox given the string false", dict(property_key="done", condition="equal", value="false"), "true or false"),
        ("text on a number property", dict(property_key=zahl, condition="equal", value="fuenf"), "number"),
        ("a format that does not fit", dict(property_key=zahl, condition="equal", value=5, format="tag"), "format"),
        ("a quick option on a number", dict(property_key=zahl, condition="equal", value=5, quick_option="today"), "date"),
    ]:
        before = inspect().get("filters") or []
        status, msg = s.c.try_call("query-filter-add", **target, **args)
        s.check(f"{what} is refused and nothing is stored",
                status == "err" and needle in msg and (inspect().get("filters") or []) == before, msg)

    # --- relative dates -----------------------------------------------------
    f = one("quick option today matches today", ["A"], property_key=termin, condition="equal", quick_option="today")
    s.check("the quick option is stored and read back", f and f.get("quick_option") == "today", f)
    one("number_of_days_ago: after 3 days ago", ["A", "B"], property_key=termin, condition="greater",
        quick_option="number_of_days_ago", value=3)

    # --- OR groups ----------------------------------------------------------
    status, res = s.c.try_call(
        "query-filter-add", **target, operator="or",
        nested_filters=[{"property_key": farben, "condition": "in", "value": ["Blau"]},
                        {"property_key": zahl, "condition": "greater_or_equal", "value": 5}],
    )
    s.check("an OR group is added", status == "ok", res)
    s.check("the OR group returns what it matches: Blau or Zahl >= 5", rows() == ["B", "C"], rows())
    stored = inspect()["filters"]
    s.check("query-inspect shows the group with its rules",
            len(stored) == 1 and stored[0].get("operator") == "or" and len(stored[0].get("nested_filters") or []) == 2,
            stored)
    status, msg = s.c.try_call("query-filter-add", **target, operator="and",
                               nested_filters=[{"property_key": zahl, "condition": "not_empty"}])
    s.check("a second group is refused, as in the app", status == "err" and "group" in msg, msg)
    s.c.call("query-filter-add", **target, property_key=termin, condition="equal", quick_option="today")
    s.check("a group and a plain filter combine with AND", rows() == [], rows())

    # --- the round trip a caller does to change one thing ------------------
    before = inspect()["filters"]
    s.c.call("query-view-update", filters=before, **target)
    after = inspect()["filters"]
    s.check("writing back what query-inspect read changes nothing (ids, group, quick option)",
            after == before, {"before": before, "after": after})

    # --- a refused entry leaves the view as it was --------------------------
    status, msg = s.c.try_call("query-view-update", **target, filters=[
        {"property_key": zahl, "condition": "equal", "value": 1},
        {"property_key": zahl, "condition": "bogus", "value": 1},
    ])
    s.check("a list with one bad entry is refused", status == "err", msg)
    s.check("and the stored filters are untouched", inspect()["filters"] == before, inspect()["filters"])
    status, msg = s.c.try_call("query-view-update", **target, sorts=[
        {"property_key": "name", "direction": "asc"}, {"property_key": zahl, "direction": "sideways"}])
    s.check("a sort list with one bad entry is refused and changes nothing",
            status == "err" and (inspect().get("sorts") or []) == [], inspect().get("sorts"))

    # --- removing -----------------------------------------------------------
    status, msg = s.c.try_call("query-filter-remove", filter_ids=[before[0]["id"], "gibt-es-nicht"], **target)
    s.check("removing an unknown id is refused and removes nothing",
            status == "err" and "gibt-es-nicht" in msg and inspect()["filters"] == before, msg)
    s.c.call("query-filter-remove", filter_ids=[before[0]["id"]], **target)
    s.check("removing the group by its id leaves the other filter",
            [x["id"] for x in inspect()["filters"]] == [before[1]["id"]], inspect()["filters"])

    # --- tags used only inside a group are still in use ----------------------
    clear()
    s.c.call("query-filter-add", **target, operator="or",
             nested_filters=[{"property_key": farben, "condition": "in", "value": ["Lila"]},
                             {"property_key": zahl, "condition": "equal", "value": 1}])
    plan = s.c.call("clean-unused-tags", space_id=s.space_id, property_id=P["Farben"]["id"])
    proposed = {o["name"] for o in plan.get("options_to_remove") or []}
    s.check("clean-unused-tags spares a tag that only a filter group uses", "Lila" not in proposed, proposed)

    # --- sorts --------------------------------------------------------------
    clear()
    res = s.c.call("query-sort-add", **target, property_key=zahl, direction="desc", empty_placement="end")
    sorts = inspect().get("sorts") or []
    s.check("query-sort-add returns the id query-inspect shows",
            [x["id"] for x in sorts] == [res.get("sort_id")], {"returned": res.get("sort_id"), "stored": sorts})
    s.check("the sort orders the rows", [label.get(o["id"]) for o in s.c.call(
        "get-list-objects-compact", space_id=s.space_id, list_id=coll, view_id=view, fields=["id"])["objects"]]
        == ["C", "B", "A", "D"], "desc, empty last")
    status, msg = s.c.try_call("query-sort-add", **target, property_key=zahl, direction="custom")
    s.check("direction custom is refused and points to query-order-set",
            status == "err" and "query-order-set" in msg, msg)
    status, msg = s.c.try_call("query-sort-remove", sort_ids=["gibt-es-nicht"], **target)
    s.check("removing an unknown sort id is refused", status == "err" and len(inspect().get("sorts") or []) == 1, msg)

    # --- writing filters clears a manual order, as in the app ---------------
    s.c.call("query-order-set", **target, object_ids=[O["D"], O["C"], O["B"], O["A"]])
    had = bool(s.c.call("query-inspect", space_id=s.space_id, object_id=coll).get("object_orders"))
    s.c.call("query-filter-add", **target, property_key=zahl, condition="not_empty")
    left = s.c.call("query-inspect", space_id=s.space_id, object_id=coll).get("object_orders")
    s.check("a filter change clears the view's manual order (said in query-view-update)", had and not left,
            {"before": had, "after": left})
    clear()

    # --- what the app leaves behind still writes back -----------------------
    # A filter on a property that is deleted later, a sort on it, and a filter
    # naming a tag that is deleted later: the app keeps all three (and ignores
    # the first two). A view holding them must still take its own list back.
    weg = P["Weg"]["key"]
    s.c.call("query-filter-add", **target, property_key=weg, condition="like", value="x")
    s.c.call("query-filter-add", **target, property_key=farben, condition="in", value=[T["Alt"], T["Rot"]])
    s.c.call("query-sort-add", **target, property_key=weg, direction="asc")
    s.c.call("delete-property", space_id=s.space_id, property_id=P["Weg"]["id"])
    s.c.call("delete-tag", space_id=s.space_id, property_id=P["Farben"]["id"], tag_id=T["Alt"])
    s.settle(2.0)
    info = s.c.call("query-inspect", space_id=s.space_id, object_id=coll)
    s.check("query-inspect warns about the filter and sort on the deleted property",
            sum("deleted or archived" in w for w in info.get("warnings") or []) == 2, info.get("warnings"))
    before = inspect()
    status, msg = s.c.try_call("query-view-update", **target, filters=before["filters"], sorts=before["sorts"])
    after = inspect()
    s.check("a view with a filter and sort on a deleted property and a deleted tag writes back unchanged",
            status == "ok" and after["filters"] == before["filters"] and after["sorts"] == before["sorts"],
            msg if status != "ok" else {"before": before["filters"], "after": after["filters"]})
    status, msg = s.c.try_call("query-filter-add", **target, property_key=farben, condition="in", value=[T["Alt"]])
    s.check("a NEW filter on the deleted tag is still refused", status == "err", msg)
    clear()
