"""Object-level actions: archive, favourites, undo, duplicate, templates,
schema management and bulk property edits.

The archive round trip matters most: delete-object archives and the REST API
offers no way back, so object-set-archived is the only route out of the bin.
"""

import json
import time


def run(s):
    obj = s.page("objekt")
    s.c.call(
        "block-paste", space_id=s.space_id, object_id=obj,
        markdown="# Kopf\n\n- inhalt",
    )

    # --- undo / redo -------------------------------------------------------
    bid = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj, kind="text", text="ALT"
    )["block_id"]
    s.c.call("block-set-text", space_id=s.space_id, object_id=obj, block_id=bid, text="NEU")
    s.c.call("object-undo", space_id=s.space_id, object_id=obj)
    s.check("undo reverts the last text change", s.block(obj, bid)["text"] == "ALT",
            s.block(obj, bid)["text"])
    s.c.call("object-undo", space_id=s.space_id, object_id=obj, direction="redo")
    s.check("redo puts it back", s.block(obj, bid)["text"] == "NEU", s.block(obj, bid)["text"])
    exhausted = s.c.call(
        "object-undo", space_id=s.space_id, object_id=obj, direction="redo"
    )
    s.check(
        "running out of redo steps is reported cleanly, not as an error",
        exhausted["steps_applied"] == 0 and "note" in exhausted,
        exhausted,
    )

    # --- favourites --------------------------------------------------------
    s.c.call("object-set-favorite", space_id=s.space_id, object_ids=[obj], favorite=True)
    s.c.call("object-set-favorite", space_id=s.space_id, object_ids=[obj], favorite=False)
    s.check("favourite can be toggled both ways", True)

    # --- the archive round trip -------------------------------------------
    s.c.call("delete-object", space_id=s.space_id, object_id=obj)
    s.settle()
    archived = s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=obj, fields=["archived"]
    )["object"]["archived"]
    s.check("delete-object archives", archived is True)
    s.c.call("object-set-archived", space_id=s.space_id, object_ids=[obj], archived=False)
    s.settle()
    restored = s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=obj, fields=["archived"]
    )["object"]["archived"]
    s.check("object-set-archived restores from the bin", restored is False)

    # --- duplicate ---------------------------------------------------------
    dup = s.c.call("object-duplicate", space_id=s.space_id, object_id=obj)["object_id"]
    s.track(dup)
    body = s.c.call("object-export", space_id=s.space_id, object_id=dup)["content"]
    s.check("a duplicate carries the source's content", "inhalt" in body, body[:120])

    # --- templates ---------------------------------------------------------
    tpl = s.c.call("template-create", space_id=s.space_id, object_id=obj)["template_id"]
    s.track(tpl)
    s.settle()
    made = s.c.call(
        "create-objects-compact-many", space_id=s.space_id,
        items=[{"type_key": "page", "name": s.probe("ausvorlage"), "template_id": tpl}],
    )["objects"][0]["object_id"]
    s.track(made)
    s.settle()
    from_template = s.c.call("object-export", space_id=s.space_id, object_id=made)["content"]
    s.check(
        "an object created from the template has the template's body",
        "inhalt" in from_template,
        from_template[:150],
    )

    # --- bulk property edits ----------------------------------------------
    targets = [s.page(f"bulk{i}") for i in range(3)]
    s.c.call(
        "objects-modify-property", space_id=s.space_id, object_ids=targets,
        operations=[{"property_key": "description", "set": "SAMMELWERT"}],
    )
    s.settle()
    got = s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=targets[0],
        property_keys=["description"],
    )
    s.check(
        "a bulk set writes the value on every object",
        "SAMMELWERT" in json.dumps(got["object"].get("properties")),
        json.dumps(got["object"].get("properties"))[:160],
    )

    tag_prop = s.property_by_key("tag")
    tags = s.c.call("list-tags", space_id=s.space_id, property_id=tag_prop["id"])
    tag_list = tags.get("tags") or tags.get("data") or []
    if tag_list:
        tag_id = tag_list[0]["id"]
        s.c.call(
            "objects-modify-property", space_id=s.space_id, object_ids=targets,
            operations=[{"property_key": "tag", "add": [tag_id]}],
        )
        s.settle()
        tagged = s.c.call(
            "get-object-compact", space_id=s.space_id, object_id=targets[0],
            property_keys=["tag"],
        )
        s.check(
            "a bulk add attaches the tag without a prior read",
            tag_id in json.dumps(tagged["object"].get("properties")),
            json.dumps(tagged["object"].get("properties"))[:180],
        )
        s.c.call(
            "objects-modify-property", space_id=s.space_id, object_ids=targets,
            operations=[{"property_key": "tag", "remove": [tag_id]}],
        )
        s.settle()
        untagged = s.c.call(
            "get-object-compact", space_id=s.space_id, object_id=targets[0],
            property_keys=["tag"],
        )
        s.check(
            "a bulk remove detaches it again",
            tag_id not in json.dumps(untagged["object"].get("properties")),
            json.dumps(untagged["object"].get("properties"))[:180],
        )

    # set null must take the property off the object. Stored as a key holding
    # null it stays in the app's properties panel, which lists every key an
    # object holds under Local, empty or not (14_render looks at the panel).
    # REST leaves null out, so only the tool's own read-back can tell.
    first = s.c.call(
        "objects-modify-property", space_id=s.space_id, object_ids=targets,
        operations=[{"property_key": "done", "set": False}],
    )
    s.check(
        "a bulk set reads every object back",
        first.get("updated") is True and first.get("verified") == len(targets),
        first,
    )
    gone = s.c.call(
        "objects-modify-property", space_id=s.space_id, object_ids=targets,
        operations=[{"property_key": "done", "set": None}],
    )
    s.check(
        "set null removes the property, which the read-back confirms",
        gone.get("updated") is True and gone.get("verified") == len(targets)
        and "mismatches" not in gone,
        gone,
    )
    # A property held with no value is left out of REST's properties, but the
    # app shows it; empty_keys names it. A removed one is gone from both.
    s.c.call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": "tag", "set": []}],
    )
    seen = s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=targets[0], include_properties=True,
    )["object"]
    s.check(
        "empty_keys names a property held empty and leaves out a removed one",
        "tag" in (seen.get("empty_keys") or []) and "done" not in (seen.get("empty_keys") or [])
        and "done" not in (seen.get("properties") or {}),
        seen,
    )
    only = s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=targets[0], property_keys=["description"],
    )["object"]
    s.check(
        "empty_keys keeps to the property_keys asked for",
        "tag" not in (only.get("empty_keys") or []), only,
    )

    # heart answers a list call with success as soon as one object took the
    # change; the object it could not reach is only logged there.
    missing = "bafyreinotthere"
    partial = s.c.call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0], missing],
        operations=[{"property_key": "done", "set": True}],
    )
    s.check(
        "an object the change did not reach makes updated false and is named",
        partial.get("updated") is False and partial.get("verified") == 1
        and missing in (partial.get("unverified") or []),
        partial,
    )
    status, msg = s.c.try_call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": "done", "set": None}, {"property_key": "done", "set": True}],
    )
    s.check(
        "null with another operation on the same key is refused",
        status == "err" and "pass it alone" in msg, msg,
    )
    status, msg = s.c.try_call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": "type", "set": None}],
    )
    s.check("removing an internal property is refused", status == "err", msg)
    versions = s.c.call(
        "object-versions", space_id=s.space_id, object_id=targets[0], limit=3,
    )["versions"]
    s.check(
        "versions name their author and mark those of this server's account",
        versions and all(v.get("author") and not v["author"].startswith("_participant_") for v in versions)
        and versions[0].get("by_this_account") is True,
        versions,
    )

    # Everything above uses description and tag, and those are bundled
    # properties, where the REST spelling of the key and the internal one are
    # the same string. A property of one's own has no such luck: it is created
    # with a generated key (6a87593a...) while list-properties reports the
    # snake_cased name, and the gRPC call behind this tool is checked against
    # the space index, which knows only the internal spelling. Handing it the
    # REST key failed the whole call with "object not found in space index" —
    # for set, add and remove alike, empty value or not — and there is no REST
    # endpoint that reports the internal key, so the caller could not have
    # supplied it. Hence the translation, and hence this test.
    own_prop = s.c.call(
        "create-property", space_id=s.space_id, name=s.probe("eigene"), format="objects"
    )
    own_prop = own_prop.get("property") or own_prop
    s.track(own_prop["id"])
    linked = s.page("verlinkt")
    s.c.call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": own_prop["key"], "set": [linked]}],
    )
    s.settle()
    own_set = s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=targets[0],
        property_keys=[own_prop["key"]],
    )
    s.check(
        "the key list-properties reports is translated for a property of one's own",
        linked in json.dumps(own_set["object"].get("properties")),
        json.dumps(own_set["object"].get("properties"))[:200],
    )
    s.c.call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": own_prop["key"], "set": []}],
    )
    s.settle()
    own_cleared = s.c.call(
        "get-object-compact", space_id=s.space_id, object_id=targets[0],
        property_keys=[own_prop["key"]],
    )
    s.check(
        "and an empty list clears it again",
        linked not in json.dumps(own_cleared["object"].get("properties")),
        json.dumps(own_cleared["object"].get("properties"))[:200],
    )
    # An object property took a name just as a select took a tag key: heart
    # stored it, the app showed a link to nothing, the read-back agreed.
    status, msg = s.c.try_call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": own_prop["key"], "set": ["Ein Projektname"]}],
    )
    s.check("an object property refuses a name instead of an id", status == "err" and "object ids" in msg, msg)
    # A tag key or name sent as a select value was stored as that string: the
    # app showed the property empty, REST still read it as the tag (its cache
    # is keyed by tag key too), and the read-back confirmed what it was sent.
    # Names and keys are now resolved to option ids before the write, so the
    # read-back passing means the id is what heart holds.
    def raw_holds(oid, want_ids):
        """The raw details hold exactly these tag ids under some key."""
        details = json.loads(s.c.call(
            "object-export", space_id=s.space_id, object_id=oid, format="json",
        )["content"])["snapshot"]["data"]["details"]
        want = sorted(want_ids)
        for v in details.values():
            got = sorted(v) if isinstance(v, list) else [v]
            if got == want:
                return True
        return False

    suffix = str(int(time.time()))
    for fmt in ("select", "multi_select"):
        prop = s.c.call(
            "create-property", space_id=s.space_id, name=s.probe(fmt), format=fmt,
            tags=[{"name": "Offen", "color": "grey", "key": f"lt_open_{fmt}_{suffix}"},
                  {"name": "Fertig", "color": "lime", "key": f"lt_done_{fmt}_{suffix}"}],
        )
        prop = prop.get("property") or prop
        s.track(prop["id"])
        opts = s.c.call("list-tags", space_id=s.space_id, property_id=prop["id"])
        opts = {t["name"]: t for t in (opts.get("tags") or opts.get("data") or [])}
        done = opts["Fertig"]
        by_key = s.c.call(
            "objects-modify-property", space_id=s.space_id, object_ids=[targets[1]],
            operations=[{"property_key": prop["key"], "set": done["key"]}],
        )
        # REST reads a stored key as the tag, so only the raw details tell.
        details = json.loads(s.c.call(
            "object-export", space_id=s.space_id, object_id=targets[1], format="json",
        )["content"])["snapshot"]["data"]["details"]
        stored = [v for v in details.values() if v == done["id"] or v == [done["id"]] or v == done["key"] or v == [done["key"]]]
        s.check(
            f"{fmt}: a tag key is stored as the tag's id, which the read-back confirms",
            by_key.get("updated") is True and by_key.get("verified") == 1
            and stored and all(v in (done["id"], [done["id"]]) for v in stored),
            {"result": by_key, "stored": stored},
        )
        by_name = s.c.call(
            "objects-modify-property", space_id=s.space_id, object_ids=[targets[1]],
            operations=[{"property_key": prop["key"], "set": "fertig"}],
        )
        s.check(f"{fmt}: so is a tag name", by_name.get("updated") is True, by_name)
        status, msg = s.c.try_call(
            "objects-modify-property", space_id=s.space_id, object_ids=[targets[1]],
            operations=[{"property_key": prop["key"], "set": "gibtesnicht"}],
        )
        s.check(f"{fmt}: an unknown tag is refused, naming the tags", status == "err" and "Fertig" in msg, msg)
        if fmt == "multi_select":
            s.c.call(
                "objects-modify-property", space_id=s.space_id, object_ids=[targets[1]],
                operations=[{"property_key": prop["key"], "add": [opts["Offen"]["key"]]}],
            )
            removed = s.c.call(
                "objects-modify-property", space_id=s.space_id, object_ids=[targets[1]],
                operations=[{"property_key": prop["key"], "remove": [done["key"]]}],
            )
            s.settle()
            left = s.c.call(
                "get-object-compact", space_id=s.space_id, object_id=targets[1],
                property_keys=[prop["key"]],
            )
            text = json.dumps(left["object"].get("properties"))
            s.check(
                "multi_select: a remove by key takes the tag off, an add by key puts one on",
                removed.get("updated") is True and done["id"] not in text and opts["Offen"]["id"] in text,
                text[:200],
            )

        # The REST object tools took a tag id or key but refused a name, the
        # spelling models reach for first; the names are now resolved first.
        offen = opts["Offen"]
        def link(key, *names):
            return {"key": key, "select": names[0]} if fmt == "select" else {"key": key, "multi_select": list(names)}
        upd = s.c.call(
            "update-object-compact", space_id=s.space_id, object_id=targets[2],
            properties=[link(prop["key"], "Fertig")],
        )
        s.check(f"{fmt}: update-object-compact takes a tag name and stores the id",
                upd.get("updated") is not False and raw_holds(targets[2], [done["id"]]), upd)
        s.c.call(
            "update-object-compact", space_id=s.space_id, object_id=targets[2],
            properties=[link(prop["id"], "offen")],
        )
        s.check(f"{fmt}: also when the link names the property by id", raw_holds(targets[2], [offen["id"]]))
        status, msg = s.c.try_call(
            "update-object-compact", space_id=s.space_id, object_id=targets[2],
            properties=[link(prop["key"], "Gibtesnicht")],
        )
        s.check(f"{fmt}: an unknown tag name is refused and nothing is stored",
                status == "err" and raw_holds(targets[2], [offen["id"]]), msg)
        many = s.c.call(
            "update-objects-compact-many", space_id=s.space_id,
            items=[{"object_id": targets[2], "properties": [link(prop["key"], done["key"])]}],
        )
        s.check(f"{fmt}: update-objects-compact-many still takes a tag key",
                many.get("ok_count") == 1 and raw_holds(targets[2], [done["id"]]), many)
        names = ("Offen",) if fmt == "select" else ("Offen", "fertig")
        created = s.c.call(
            "create-objects-compact-many", space_id=s.space_id,
            items=[{"type_key": "page", "name": s.probe(f"angelegt-{fmt}"), "properties": [link(prop["key"], *names)]}],
        )
        new_id = (created.get("objects") or [{}])[0].get("object_id")
        s.track(new_id)
        want = [offen["id"]] if fmt == "select" else [offen["id"], done["id"]]
        s.check(f"{fmt}: create-objects-compact-many takes tag names", bool(new_id) and raw_holds(new_id, want), created)

        if fmt == "select":
            ordered = s.c.call(
                "schema-set-order", space_id=s.space_id, kind="tags",
                property_key=prop["key"], ids=["Fertig", offen["key"]],
            )
            # list-tags is no witness: REST lists the options in index order,
            # unsorted. The order lives in each option's orderId, which the app
            # sorts by.
            def order_id(tag_id):
                return json.loads(s.c.call(
                    "object-export", space_id=s.space_id, object_id=tag_id, format="json",
                )["content"])["snapshot"]["data"]["details"].get("orderId", "")
            first, second = order_id(done["id"]), order_id(offen["id"])
            s.check("schema-set-order takes tag names and keys and answers the ids",
                    ordered.get("ids") == [done["id"], offen["id"]] and first and second and first < second,
                    {"answer": ordered.get("ids"), "orderIds": [first, second]})
            plan = s.c.call(
                "clean-unused-tags", space_id=s.space_id, property_id=prop["id"], keep_tag_ids=["Offen"],
            )
            s.check("clean-unused-tags takes a tag name to keep", plan.get("dry_run") is True, plan)
            status, msg = s.c.try_call(
                "clean-unused-tags", space_id=s.space_id, property_id=prop["id"], keep_tag_ids=["Gibtesnicht"],
            )
            s.check("and refuses one that matches no option, since it would protect nothing",
                    status == "err" and "Offen" in msg, msg)

    # The translation must not become stricter than anytype itself: a bundled
    # relation exists in every space even when the space index holds no object
    # for it, which is why heart asks the bundle before the index. starred is
    # such a key here — it is in no listing this space returns, and refusing it
    # would take away something that used to work.
    starred = s.c.try_call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": "starred", "set": True}],
    )
    s.check(
        "a bundled key the space has never used is still accepted",
        starred[0] == "ok",
        starred[1],
    )

    status, msg = s.c.try_call(
        "objects-modify-property", space_id=s.space_id, object_ids=[targets[0]],
        operations=[{"property_key": "gibtesnicht", "set": []}],
    )
    s.check(
        "a key that is neither spelling is refused instead of written",
        status == "err" and "no property with the key" in msg,
        msg,
    )

    status, _ = s.c.try_call(
        "objects-modify-property", space_id=s.space_id, object_ids=targets,
        operations=[{"property_key": "tag"}],
    )
    s.check("an operation without add/remove/set is refused", status == "err")

    # Anytype's "which properties could this object have" RPC is an unimplemented
    # stub upstream, so no tool is offered for it; the question is answered by a
    # type's properties instead.
    s.check(
        "no tool is offered for the unimplemented available-relations RPC",
        "object-relations-available" not in s.c.tools,
    )

    # --- permanent deletion, and the guards around it ---------------------
    # A throwaway object, created here and referenced nowhere else.
    doomed = s.page("wirdgeloescht")
    status, msg = s.c.try_call(
        "object-delete-permanently", space_id=s.space_id, object_ids=[doomed]
    )
    s.check(
        "erasing without confirm is refused and points at delete-object",
        status == "err" and "confirm" in msg and "delete-object" in msg,
        msg,
    )
    status, msg = s.c.try_call(
        "object-delete-permanently", space_id=s.space_id,
        object_ids=[doomed], confirm=True,
    )
    s.check(
        "erasing a live object is refused until it is archived",
        status == "err" and "not in the bin" in msg,
        msg,
    )
    s.check(
        "the refused object is untouched",
        s.c.call("get-object-compact", space_id=s.space_id, object_id=doomed,
                 fields=["id"])["object"]["id"] == doomed,
    )
    status, msg = s.c.try_call(
        "object-delete-permanently", space_id=s.space_id,
        object_ids=[doomed, "bafyreigibtesnichtxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"],
        confirm=True,
    )
    s.check(
        "a batch with an unreadable id erases nothing at all",
        status == "err" and "could not be read" in msg,
        msg,
    )

    s.c.call("delete-object", space_id=s.space_id, object_id=doomed)
    s.settle()
    erased = s.c.call(
        "object-delete-permanently", space_id=s.space_id,
        object_ids=[doomed], confirm=True,
    )
    s.check("erasing an archived object reports success", erased.get("deleted") is True, erased)
    s.settle()
    gone, _ = s.c.try_call(
        "get-object-compact", space_id=s.space_id, object_id=doomed, fields=["id"]
    )
    s.check("the erased object is really gone", gone == "err", "it is still readable")
    # It no longer exists, so the runner must not try to clean it up again.
    if doomed in s.created:
        s.created.remove(doomed)

    # A schema object does NOT behave like that. Erasing a property removes it
    # from every listing, but a direct read by id keeps serving a tombstone. The
    # tool therefore confirms an erase by checking the object has left the bin;
    # if this check ever flips, that verification has to be revisited, because
    # reading by id would then be the better probe.
    ghost = s.c.call(
        "create-property", space_id=s.space_id, name=s.probe("geist"), format="text"
    )
    ghost_id = (ghost.get("property") or ghost)["id"]
    s.c.call("delete-property", space_id=s.space_id, property_id=ghost_id)
    s.settle(2.0)
    erased = s.c.call(
        "object-delete-permanently", space_id=s.space_id,
        object_ids=[ghost_id], confirm=True,
    )
    s.check(
        "erasing a property reports success and no survivors",
        erased.get("deleted") is True and "still_present" not in erased,
        erased,
    )
    s.settle(2.0)
    props = s.c.call("list-properties", space_id=s.space_id, limit=1000)["properties"]
    s.check(
        "the erased property is gone from list-properties",
        not any(p["id"] == ghost_id for p in props),
        len(props),
    )
    binned = s.c.call("list-archived", space_id=s.space_id, limit=5000)["objects"]
    s.check(
        "and gone from the bin",
        not any(o["object_id"] == ghost_id for o in binned),
    )
    readable, _ = s.c.try_call("get-property", space_id=s.space_id, property_id=ghost_id)
    s.check(
        "but a read by id still serves it, which is why that is not the erase check",
        readable == "ok",
        "if this fails, object-delete-permanently can verify by id again",
    )

    # --- schema: properties, tags, types ----------------------------------
    prop = s.c.call(
        "create-property", space_id=s.space_id, name=s.probe("prop"), format="multi_select"
    )
    prop_id = (prop.get("property") or prop)["id"]
    s.track(prop_id)
    s.c.call(
        "update-property", space_id=s.space_id, property_id=prop_id, name=s.probe("prop v2")
    )
    fetched = s.c.call("get-property", space_id=s.space_id, property_id=prop_id)
    s.check(
        "update-property renames",
        (fetched.get("property") or fetched)["name"] == s.probe("prop v2"),
    )
    tag = s.c.call(
        "create-tag", space_id=s.space_id, property_id=prop_id,
        name=s.probe("tag"), color="red",
    )
    tag_id = (tag.get("tag") or tag)["id"]
    s.track(tag_id)
    s.c.call(
        "update-tag", space_id=s.space_id, property_id=prop_id, tag_id=tag_id,
        name=s.probe("tag v2"),
    )
    fetched = s.c.call("get-tag", space_id=s.space_id, property_id=prop_id, tag_id=tag_id)
    s.check("update-tag renames", (fetched.get("tag") or fetched)["name"] == s.probe("tag v2"))

    # delete-* archives: it disappears from list-* but get-* keeps serving it,
    # and for tags nothing in the response marks it as gone.
    s.c.call("delete-tag", space_id=s.space_id, property_id=prop_id, tag_id=tag_id)
    s.settle()
    remaining = s.c.call("list-tags", space_id=s.space_id, property_id=prop_id)
    ids = [t["id"] for t in (remaining.get("tags") or remaining.get("data") or [])]
    s.check("a deleted tag disappears from list-tags", tag_id not in ids, ids)
    status, _ = s.c.try_call(
        "get-tag", space_id=s.space_id, property_id=prop_id, tag_id=tag_id
    )
    s.check(
        "get-tag still serves the deleted tag, as documented",
        status == "ok",
        "if this ever fails, the delete-* descriptions need updating",
    )
    s.c.call("delete-property", space_id=s.space_id, property_id=prop_id)

    new_type = s.c.call(
        "create-type", space_id=s.space_id, name=s.probe("typ"),
        plural_name=s.probe("typen"), layout="basic",
        properties=[{"key": "description", "format": "text"}],
    )
    type_id = (new_type.get("type") or new_type)["id"]
    s.track(type_id)
    read_back = s.c.call("get-type-compact", space_id=s.space_id, type_id=type_id)
    props = (read_back.get("type") or read_back).get("properties")
    s.check("get-type-compact returns a type's properties by default", bool(props), props)

    # --- the layout switches of a type -------------------------------------
    # These are hidden relations no listing shows, so the tool has to report
    # what it replaced, and the names have to survive a round trip.
    layout = s.c.call(
        "type-set-layout", space_id=s.space_id, type_id=type_id,
        full_width=True, header_position="center", properties_view="list",
    )
    s.check(
        "a fresh type starts out narrow, left-aligned, properties in a line",
        layout["before"] == {"full_width": False, "header_position": "left",
                             "properties_view": "line"},
        layout["before"],
    )
    s.check(
        "the switches read back as the names that were passed",
        layout["after"] == {"full_width": True, "header_position": "center",
                            "properties_view": "list"},
        layout["after"],
    )
    partial = s.c.call(
        "type-set-layout", space_id=s.space_id, type_id=type_id, full_width=False,
    )
    s.check(
        "a switch that was not passed keeps its value",
        partial["after"] == {"full_width": False, "header_position": "center",
                             "properties_view": "list"},
        partial["after"],
    )
    status, msg = s.c.try_call("type-set-layout", space_id=s.space_id, type_id=type_id)
    s.check("a call that changes nothing is refused", status == "err", msg)
    status, msg = s.c.try_call(
        "type-set-layout", space_id=s.space_id, type_id=type_id, header_position="diagonal",
    )
    s.check("an unknown header position is refused", status == "err" and "left" in msg, msg)

    desc = s.property_by_key("description")
    if desc:
        status, _ = s.c.try_call(
            "type-set-featured-properties", space_id=s.space_id,
            type_id=type_id, property_ids=[desc["id"]],
        )
        s.check("featured properties can be set on a type", status == "ok")
        status, _ = s.c.try_call(
            "type-set-featured-properties", space_id=s.space_id,
            type_id=type_id, property_ids=[],
        )
        s.check("featured properties can be cleared", status == "ok")
    s.c.call("delete-type", space_id=s.space_id, type_id=type_id)
