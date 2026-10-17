"""Native resource export against heart, including a fresh MCP connection.

Checks the actual bytes and wire shapes. ChatGPT's own materialization and UI
need a separate test in ChatGPT; a stdio client cannot verify those behaviors.
"""

import base64
import hashlib
import json
import os
import pathlib
import urllib.parse

from harness import Client, IN_ROOT


def page_pdf(pages):
    """A valid PDF whose page N shows "(Page N)"."""
    out = bytearray(b"%PDF-1.7\n")
    offsets = []

    def obj(body):
        offsets.append(len(out))
        out.extend(f"{len(offsets)} 0 obj\n{body}\nendobj\n".encode())

    kids = " ".join(f"{4 + 2 * i} 0 R" for i in range(pages))
    obj("<< /Type /Catalog /Pages 2 0 R >>")
    obj(f"<< /Type /Pages /Kids [{kids}] /Count {pages} >>")
    obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
    for n in range(1, pages + 1):
        obj(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents {5 + 2 * (n - 1)} 0 R >>")
        content = f"BT /F1 24 Tf 72 700 Td (Page {n}) Tj ET\n"
        obj(f"<< /Length {len(content)} >>\nstream\n{content}endstream")
    xref = len(out)
    out.extend(f"xref\n0 {len(offsets) + 1}\n0000000000 65535 f \n".encode())
    for offset in offsets:
        out.extend(f"{offset:010d} 00000 n \n".encode())
    out.extend(f"trailer\n<< /Size {len(offsets) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    return bytes(out)


def run(s):
    limit = 6 * 1024 * 1024
    staging = pathlib.Path(IN_ROOT) / "livetest-file-export"
    staging.mkdir(parents=True, exist_ok=True)
    handles = []
    downloads = []
    s.check("MCP 2025-11-25 was negotiated", s.c.protocol_version == "2025-11-25")
    s.check("resources are advertised without unsupported subscriptions", s.c.capabilities.get("resources") == {})
    s.check("file-export is discoverable", "file-export" in s.c.tools)
    s.check("file-export-many is discoverable", "file-export-many" in s.c.tools)
    objects = {}
    fixtures = {
        "LIVETEST export.png": base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a+1cAAAAASUVORK5CYII="),
        "LIVETEST export.pdf": b"%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n",
        "LIVETEST limit.bin": os.urandom(limit),
        "LIVETEST oversized.bin": os.urandom(limit + 1),
        "LIVETEST pages.pdf": page_pdf(5),
    }
    try:
        for name, expected in fixtures.items():
            (staging / name).write_bytes(expected)
            uploaded = s.c.call("file-upload", space_id=s.space_id, staged_path=f"livetest-file-export/{name}")
            object_id = uploaded["object_id"]
            s.track(object_id)
            objects[name] = object_id
            response = s.c.rpc("tools/call", {"name": "file-export", "arguments": {"object_id": object_id, "filename": name}})
            result = response.get("result", {})
            if len(expected) > limit:
                s.check("6 MiB plus one byte is refused", result.get("isError") is True and "6291456" in json.dumps(result), result)
                s.check("oversized export returns no resource link", not any(c.get("type") == "resource_link" for c in result.get("content", [])), result)
                continue
            s.check(f"{name}: export succeeded", not response.get("error") and result.get("isError") is False, result)
            links = [c for c in result.get("content", []) if c.get("type") == "resource_link"]
            s.check(f"{name}: native resource link, no encoded file in tool history", len(links) == 1 and len(json.dumps(result).encode()) < 4096, result)
            if not links:
                continue
            uri = links[0]["uri"]
            handle = urllib.parse.urlparse(uri).netloc
            handles.append(handle)
            metadata = result["structuredContent"]
            s.check(f"{name}: metadata describes original bytes", metadata["size_bytes"] == len(expected) and metadata["sha256"] == hashlib.sha256(expected).hexdigest(), metadata)
            # The second launcher starts another MCP process/container, proving
            # the export registry is not held only by the exporting connection.
            reader = Client()
            try:
                fetched = reader.rpc("resources/read", {"uri": uri})
                content = fetched.get("result", {}).get("contents", [])
                s.check(f"{name}: resource readable across connections", len(content) == 1, fetched.get("error"))
                if content:
                    actual = base64.b64decode(content[0]["blob"], validate=True)
                    s.check(f"{name}: bytes and SHA-256 match", actual == expected and hashlib.sha256(actual).hexdigest() == metadata["sha256"])
                    s.check(f"{name}: binary resource shape and MIME type", "text" not in content[0] and content[0]["mimeType"] == links[0]["mimeType"])
                    s.check(f"{name}: serialized response fits tunnel budget", len(json.dumps(fetched).encode()) < 10 * 1024 * 1024)
            finally:
                reader.close()

            if name.endswith(".png"):
                downloaded = s.c.call("file-download", object_id=object_id, target_name="livetest-file-export-download.png")
                downloads.append("out/livetest-file-export-download.png")
                s.check("existing file-download still returns a local path", downloaded.get("local_path", "").endswith("livetest-file-export-download.png"), downloaded)

        # PDF pages: pdfcpu runs inside the scratch image, which has no config
        # directory, so only a live run shows that extraction works there.
        pages_args = {"object_id": objects["LIVETEST pages.pdf"], "pages": "4, 1-2"}
        result = s.c.rpc("tools/call", {"name": "file-export", "arguments": pages_args}).get("result", {})
        meta = result.get("structuredContent") or {}
        s.check("pdf pages: export succeeded with normalised selection", result.get("isError") is False and meta.get("pages") == "1-2,4" and meta.get("source_page_count") == 5, result)
        if meta.get("uri"):
            handles.append(urllib.parse.urlparse(meta["uri"]).netloc)
            reader = Client()
            try:
                content = reader.rpc("resources/read", {"uri": meta["uri"]}).get("result", {}).get("contents", [])
                data = base64.b64decode(content[0]["blob"], validate=True) if content else b""
                s.check("pdf pages: extract holds the selected pages only", data.startswith(b"%PDF") and b"(Page 4)" in data and b"(Page 1)" in data and b"(Page 3)" not in data and hashlib.sha256(data).hexdigest() == meta["sha256"])
            finally:
                reader.close()
            again = s.c.rpc("tools/call", {"name": "file-export", "arguments": pages_args}).get("result", {}).get("structuredContent") or {}
            s.check("pdf pages: repeated page export returns the same link", again.get("uri") == meta["uri"], again)
        beyond = s.c.rpc("tools/call", {"name": "file-export", "arguments": {"object_id": objects["LIVETEST pages.pdf"], "pages": "5-6"}}).get("result", {})
        s.check("pdf pages: pages beyond the end are refused with the page count", beyond.get("isError") is True and "has 5 pages" in json.dumps(beyond), beyond)

        # One call, several files: an oversized item fails alone and the links
        # come back in item order, each readable from another connection.
        many_names = ["LIVETEST export.png", "LIVETEST oversized.bin", "LIVETEST export.pdf"]
        response = s.c.rpc("tools/call", {"name": "file-export-many", "arguments": {"items": [{"object_id": objects[n], "filename": "many " + n} for n in many_names]}})
        result = response.get("result", {})
        links = [c for c in result.get("content", []) if c.get("type") == "resource_link"]
        meta = result.get("structuredContent") or {}
        s.check("file-export-many: partial failure does not fail the call", result.get("isError") is False and meta.get("exported") == 2 and meta.get("failed") == 1, result)
        s.check("file-export-many: oversized item reports its error in place", "6291456" in json.dumps((meta.get("results") or [{}, {}])[1]), meta)
        s.check("file-export-many: one link per exported file, in item order", [l.get("name") for l in links] == ["many LIVETEST export.png", "many LIVETEST export.pdf"], links)
        handles.extend(urllib.parse.urlparse(l["uri"]).netloc for l in links)
        reader = Client()
        try:
            for link, name in zip(links, ["LIVETEST export.png", "LIVETEST export.pdf"]):
                content = reader.rpc("resources/read", {"uri": link["uri"]}).get("result", {}).get("contents", [])
                s.check(f"file-export-many: {name} bytes match", len(content) == 1 and base64.b64decode(content[0]["blob"], validate=True) == fixtures[name])
        finally:
            reader.close()

        for method, key in (("resources/list", "resources"), ("resources/templates/list", "resourceTemplates")):
            response = s.c.rpc(method)
            s.check(f"{method}: exports are not enumerated", response.get("result", {}).get(key) == [], response)
        listed = s.c.call("file-list-output", recursive=True, include_dirs=True)
        s.check("private export cache is hidden from output listings", not any(".mcp-exports" in e["relative_path"] or ".mcp-export-download-" in e["relative_path"] for e in listed["entries"]))
        missing = s.c.rpc("resources/read", {"uri": "anytype-export://" + "0" * 48 + "/missing.bin"})
        s.check("missing resource has MCP resource-not-found error", missing.get("error", {}).get("code") == -32002, missing)
        invalid = s.c.rpc("resources/read", {"uri": "file:///etc/passwd"})
        s.check("arbitrary filesystem URIs are refused", invalid.get("error", {}).get("code") == -32602, invalid)
    finally:
        # Remove only paths and opaque export handles created by this suite.
        s.remove_shared("in/livetest-file-export", *downloads, *(f"out/.mcp-exports/{h}" for h in handles))
