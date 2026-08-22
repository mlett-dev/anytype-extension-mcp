"""Object-level actions: archive, favourites, undo, duplicate, templates,
schema management and bulk property edits.

The archive round trip matters most: delete-object archives and the REST API
offers no way back, so object-set-archived is the only route out of the bin.
"""

import json


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
