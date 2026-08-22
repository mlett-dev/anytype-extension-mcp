"""The remaining GUI actions: relation blocks, templates on existing objects,
extracting blocks, objects from a URL, link appearance, layout, embeds, the
graph and Unsplash.

Two of these depend on the server reaching the internet (objects from a URL,
Unsplash). Those checks report what happened rather than failing the run when
the network is unavailable — a missing network is not a defect in this server.
"""


def run(s):
    obj = s.page("extras")

    # --- relation block ----------------------------------------------------
    # BlockRelationAdd only re-keys an existing relation block, so the tool
    # creates one through BlockCreate. Check a block really appeared.
    rel = s.c.call(
        "block-relation-add", space_id=s.space_id, object_id=obj, property_key="description"
    )
    block = s.block(obj, rel["block_id"])
    s.check(
        "block-relation-add inserts a relation block",
        block is not None and block["kind"] == "relation",
        block,
    )
    status, msg = s.c.try_call(
        "block-relation-add", space_id=s.space_id, object_id=obj, property_key=""
    )
    s.check("a relation block without a property key is refused", status == "err", msg)

    # description is a bundled property, where the REST key and the internal one
    # are the same string — which is why this tool looked fine while handing the
    # REST key straight to gRPC. A property of one's own has a generated internal
    # key, and the block stores that one; the block used to point at a property
    # that does not exist and simply rendered as nothing.
    own = s.c.call(
        "create-property", space_id=s.space_id, name=s.probe("eigenschaft"), format="text"
    )
    own = own.get("property") or own
    s.track(own["id"])
    own_rel = s.c.call(
        "block-relation-add", space_id=s.space_id, object_id=obj, property_key=own["key"]
    )
    stored = s.block(obj, own_rel["block_id"])
    s.check(
        "a relation block stores the internal key, not the one list-properties reports",
        stored is not None and stored.get("property_key") not in ("", None, own["key"]),
        {"rest_key": own["key"], "stored": (stored or {}).get("property_key")},
    )
    status, msg = s.c.try_call(
        "block-relation-add", space_id=s.space_id, object_id=obj, property_key="gibtesnicht"
    )
    s.check(
        "a property key that exists in neither spelling is refused",
        status == "err" and "no property with the key" in msg,
        msg,
    )

    s.check(
        "object-set-layout is not offered",
        "object-set-layout" not in s.c.tools,
        "the RPC reports success without changing anything; see the README",
    )

    # --- embeds ------------------------------------------------------------
    embed = s.c.call(
        "block-embed-create", space_id=s.space_id, object_id=obj,
        kind="latex", text="E = mc^2",
    )
    block = s.block(obj, embed["block_id"])
    s.check(
        "block-embed-create inserts a latex block with its source",
        block is not None and block["kind"] == "latex" and block.get("text") == "E = mc^2",
        block,
    )
    s.c.call(
        "block-embed-set-text", space_id=s.space_id, object_id=obj,
        block_id=embed["block_id"], kind="latex", text="a^2 + b^2 = c^2",
    )
    s.check(
        "block-embed-set-text replaces the source",
        s.block(obj, embed["block_id"]).get("text") == "a^2 + b^2 = c^2",
        s.block(obj, embed["block_id"]).get("text"),
    )
    mermaid = s.c.call(
        "block-embed-create", space_id=s.space_id, object_id=obj,
        kind="mermaid", text="graph TD; A-->B;",
    )
    s.check(
        "a mermaid embed is inserted too",
        s.block(obj, mermaid["block_id"]) is not None,
    )

    # --- link blocks and their appearance ----------------------------------
    linked = s.page("linkziel")
    link = s.c.call(
        "block-create", space_id=s.space_id, object_id=obj,
        kind="link", linked_object_id=linked,
    )
    status, _ = s.c.try_call(
        "block-link-appearance", space_id=s.space_id, object_id=obj,
        block_ids=[link["block_id"]], card_style="card",
        icon_size="medium", description="content",
    )
    s.check("link appearance can be set to a card", status == "ok")
    s.check(
        "the link block survives the appearance change",
        s.block(obj, link["block_id"]) is not None,
    )
    shown = s.block(obj, link["block_id"])
    s.check(
        "block-list reports the appearance that was written",
        (shown.get("card_style"), shown.get("icon_size"), shown.get("description"))
        == ("card", "medium", "content"),
        {k: shown.get(k) for k in ("card_style", "icon_size", "description")},
    )
    # BlockLinkListSetAppearance carries all four settings and heart assigns all
    # four, so a partial call used to reset the rest: only icon_size sent meant
    # card_style fell back to text and the property list was cleared.
    s.c.call(
        "block-link-appearance", space_id=s.space_id, object_id=obj,
        block_ids=[link["block_id"]], property_keys=["type"],
    )
    s.c.call(
        "block-link-appearance", space_id=s.space_id, object_id=obj,
        block_ids=[link["block_id"]], icon_size="small",
    )
    shown = s.block(obj, link["block_id"])
    s.check(
        "changing only the icon size keeps card style, description and properties",
        (shown.get("card_style"), shown.get("icon_size"), shown.get("description"),
         shown.get("property_keys")) == ("card", "small", "content", ["type"]),
        {k: shown.get(k) for k in ("card_style", "icon_size", "description", "property_keys")},
    )
    s.c.call(
        "block-link-appearance", space_id=s.space_id, object_id=obj,
        block_ids=[link["block_id"]], property_keys=[],
    )
    s.check(
        "an empty property list clears it, unlike an omitted one",
        s.block(obj, link["block_id"]).get("property_keys") == [],
        s.block(obj, link["block_id"]).get("property_keys"),
    )
    status, msg = s.c.try_call(
        "block-link-appearance", space_id=s.space_id, object_id=obj,
        block_ids=[link["block_id"]],
    )
    s.check(
        "a call that asks for nothing is refused instead of writing defaults",
        status == "err" and "at least one" in msg,
        msg,
    )

    # --- extracting blocks into their own object ---------------------------
    source = s.page("ausgliedern")
    s.c.call(
        "block-paste", space_id=s.space_id, object_id=source,
        markdown="# Bleibt hier\n\nAbsatz eins.\n\n# Wandert aus\n\nAbsatz zwei.",
    )
    body = [
        b for b in s.blocks(source)
        if b["kind"] == "text" and b["id"] != "title"
    ]
    # One object is created per top-level block in the selection, so two
    # siblings yield two objects — not one object holding both.
    to_extract = [b["id"] for b in body if b.get("text") in ("Wandert aus", "Absatz zwei.")]
    extracted = s.c.call(
        "block-extract-to-object", space_id=s.space_id, object_id=source,
        block_ids=to_extract, type_key="page",
    )
    new_ids = extracted.get("new_object_ids") or []
    s.track(*new_ids)
    s.check(
        "extracting creates one object per selected top-level block",
        len(new_ids) == len(to_extract),
        extracted,
    )
    remaining = [t for _, t in s.body_text(source)]
    s.check(
        "the extracted blocks are gone from the source",
        "Absatz zwei." not in remaining,
        remaining,
    )
    if new_ids:
        bodies = [
            s.c.call("object-export", space_id=s.space_id, object_id=i)["content"]
            for i in new_ids
        ]
        s.check(
            "the extracted content ended up in the new objects",
            any("Absatz zwei." in b for b in bodies),
            bodies,
        )

    # --- applying a template to an existing object -------------------------
    template_source = s.page("vorlagequelle")
    s.c.call(
        "block-paste", space_id=s.space_id, object_id=template_source,
        markdown="# Vorlagenkopf\n\n- vorlagenpunkt",
    )
    template_id = s.c.call(
        "template-create", space_id=s.space_id, object_id=template_source
    )["template_id"]
    s.track(template_id)
    s.settle()

    plain = s.page("bekommtvorlage")
    s.c.call("block-create", space_id=s.space_id, object_id=plain, kind="text", text="VORHER")
    s.c.call(
        "object-apply-template", space_id=s.space_id, object_id=plain, template_id=template_id
    )
    s.settle()
    after = s.c.call("object-export", space_id=s.space_id, object_id=plain)["content"]
    s.check(
        "applying a template puts the template's body on the object",
        "vorlagenpunkt" in after,
        after[:180],
    )

    # --- graph -------------------------------------------------------------
    graph = s.c.call("object-graph", space_id=s.space_id, limit=50)
    s.check(
        "object-graph returns nodes",
        graph["node_count"] > 0,
        f"nodes={graph['node_count']} edges={graph['edge_count']}",
    )
    s.check(
        "graph nodes carry ids",
        all(n.get("id") for n in graph["nodes"]),
        graph["nodes"][:2],
    )

    # --- network-dependent: report, do not fail the run --------------------
    status, payload = s.c.try_call(
        "object-create-from-url", space_id=s.space_id,
        url="https://example.com", type_key="bookmark",
    )
    if status == "ok":
        s.track(payload["object_id"])
        s.settle(2.5)
        fetched = s.c.call(
            "get-object-compact", space_id=s.space_id,
            object_id=payload["object_id"], fields=["id", "name", "type"],
        )
        s.check(
            "a bookmark object is created from a URL",
            fetched["object"]["id"] == payload["object_id"],
            fetched["object"].get("name"),
        )
    else:
        print(f"          (skipped: object-create-from-url unavailable — {payload[:90]})")

    status, payload = s.c.try_call("unsplash-search", query="mountains", limit=3)
    if status == "ok" and payload.get("count"):
        first = payload["pictures"][0]
        s.check(
            "unsplash-search returns ids and attribution data",
            all(p.get("picture_id") for p in payload["pictures"]) and first.get("artist"),
            first,
        )
        got, downloaded = s.c.try_call(
            "unsplash-download", space_id=s.space_id, picture_id=first["picture_id"]
        )
        if got == "ok":
            s.track(downloaded["object_id"])
            s.check(
                "unsplash-download reports the attribution its terms require",
                bool(downloaded.get("attribution")),
                downloaded.get("attribution"),
            )
            # End state, not "the call returned ok": the image object must exist.
            s.settle(2.0)
            fetched = s.c.call(
                "get-object-compact", space_id=s.space_id,
                object_id=downloaded["object_id"], fields=["id", "name", "type"],
            )
            # The type is what marks it as an image; the reported layout stays
            # basic, as it does for uploaded files generally.
            s.check(
                "the downloaded picture exists as an image object",
                fetched["object"]["id"] == downloaded["object_id"]
                and (fetched["object"].get("type") or {}).get("key") == "image",
                fetched["object"],
            )
            # The staging file is a handover, not a deposit.
            staged = s.c.call("file-list-input", recursive=True).get("entries") or []
            leftovers = [
                e.get("relative_path") or e.get("name") for e in staged
                if "unsplash-" in (e.get("relative_path") or e.get("name") or "")
            ]
            s.check(
                "no staged Unsplash file is left in the input directory",
                not leftovers,
                leftovers,
            )
        else:
            s.check("unsplash-download succeeds", False, downloaded)
    elif status == "err" and "no Unsplash key configured" in str(payload):
        print("          (skipped: no UNSPLASH_ACCESS_KEY configured)")
    else:
        s.check("unsplash-search works", False, payload)
