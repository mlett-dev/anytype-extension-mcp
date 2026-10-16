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


def run(s):
    limit = 6 * 1024 * 1024
    staging = pathlib.Path(IN_ROOT) / "livetest-file-export"
    staging.mkdir(parents=True, exist_ok=True)
    handles = []
    downloads = []
    s.check("MCP 2025-11-25 was negotiated", s.c.protocol_version == "2025-11-25")
    s.check("resources are advertised without unsupported subscriptions", s.c.capabilities.get("resources") == {})
    s.check("file-export is discoverable", "file-export" in s.c.tools)
    fixtures = {
        "LIVETEST export.png": base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a+1cAAAAASUVORK5CYII="),
        "LIVETEST export.pdf": b"%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n",
        "LIVETEST limit.bin": os.urandom(limit),
        "LIVETEST oversized.bin": os.urandom(limit + 1),
    }
    try:
        for name, expected in fixtures.items():
            (staging / name).write_bytes(expected)
            uploaded = s.c.call("file-upload", space_id=s.space_id, staged_path=f"livetest-file-export/{name}")
            object_id = uploaded["object_id"]
            s.track(object_id)
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
