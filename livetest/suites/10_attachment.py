"""Getting content in from a caller that has no filesystem on the server.

The path-based tools can only see files that are already in the input root of
the machine the server runs on. A caller at the other end of the tunnel has
none of those paths, and its files are held by its runtime rather than by it.
The -attachment tools take such a file as a real file argument: the runtime
resolves it to a temporary download URL before the call, and the server fetches
the bytes itself. What matters here is that the content arrives intact, that a
reference which cannot be resolved fails loudly instead of producing an empty
object, and that nothing is left lying in the shared directory afterwards.

This suite needs outbound internet, because a download URL is the whole point.
It uses a small stable public image in place of the URL a runtime would supply;
every other aspect of the path is exactly what production takes. Without a
network the suite skips rather than failing the run.
"""

import os
import pathlib
import urllib.error
import urllib.request

from harness import IN_ROOT as _IN_ROOT

IN_ROOT = pathlib.Path(_IN_ROOT)

# Stands in for the download_url a runtime would hand over. Small, public, and
# stable enough that a failure here means the route is broken, not the fixture.
SOURCE_URL = "https://httpbin.org/image/png"


def staging_leftovers():
    """Private staging directories the upload tools must not leave behind."""
    return [p.name for p in IN_ROOT.glob(".bytes-*")]


def file_arg(name=None, mime_type="image/png", url=SOURCE_URL, file_id="file_livetest"):
    """The object a runtime substitutes for a file argument."""
    arg = {"download_url": url, "file_id": file_id}
    if mime_type:
        arg["mime_type"] = mime_type
    if name:
        arg["file_name"] = name
    return arg


def source_bytes():
    with urllib.request.urlopen(SOURCE_URL, timeout=20) as response:
        return response.read()


def run(s):
    import hashlib

    try:
        expected = source_bytes()
    except (urllib.error.URLError, OSError) as exc:
        s.check(f"skipped: {SOURCE_URL} is not reachable from the test host ({exc})", True)
        return
    expected_sha = hashlib.sha256(expected).hexdigest()

    obj = s.page("attachment")
    s.c.call("block-paste", space_id=s.space_id, object_id=obj, markdown="# Inhalt\n\n- bleibt")

    staged_name = "livetest attachment bild.png"
    try:
        # --- upload straight from the runtime's file --------------------------
        uploaded = s.c.call(
            "file-upload-attachment", space_id=s.space_id, type="image",
            file=file_arg("livetest-attachment.png"),
        )
        s.track(uploaded.get("object_id"))
        s.check(
            "a runtime-held file becomes an Anytype file object in one call",
            bool(uploaded.get("object_id")) and uploaded["size_bytes"] == len(expected),
            uploaded,
        )
        s.check(
            "the bytes that arrive are the bytes at the source",
            uploaded["sha256"] == expected_sha,
            uploaded["sha256"],
        )
        s.check(
            "the result records that no base64 was involved",
            uploaded["source"]["base64_used"] is False,
            uploaded["source"],
        )
        # The download URL is a short-lived bearer credential; echoing it back
        # would put a live secret into the caller's context for no gain.
        s.check(
            "the download URL is never handed back",
            "download_url" not in str(uploaded),
        )
        s.check(
            "the one-step upload leaves nothing staged behind",
            not staging_leftovers(),
            staging_leftovers(),
        )

        # --- naming ----------------------------------------------------------
        # file_name may be absent by contract, and a name without an extension
        # would leave Anytype guessing at the type.
        unnamed = s.c.call(
            "file-upload-attachment", space_id=s.space_id, file=file_arg(None),
        )
        s.track(unnamed.get("object_id"))
        s.check(
            "a missing file_name still yields a name with the right extension",
            str(unnamed.get("filename", "")).endswith(".png"),
            unnamed.get("filename"),
        )
        renamed = s.c.call(
            "file-upload-attachment", space_id=s.space_id,
            filename="livetest-umbenannt.png", file=file_arg("ignoriert.png"),
        )
        s.track(renamed.get("object_id"))
        s.check(
            "an explicit filename wins over the runtime's",
            renamed.get("filename") == "livetest-umbenannt.png",
            renamed.get("filename"),
        )

        # --- staging, then the ordinary staged_path tools ---------------------
        staged = s.c.call(
            "file-stage-attachment", filename=staged_name, overwrite=True,
            file=file_arg("livetest-staged.png"),
        )
        s.check(
            "the file lands in the input root with its size and checksum",
            staged["size_bytes"] == len(expected) and staged["sha256"] == expected_sha,
            staged,
        )
        s.check(
            "the bytes on disk are the bytes at the source",
            (IN_ROOT / staged_name).read_bytes() == expected,
        )
        s.check(
            "the staged file is what it claims to be",
            staged["mime_type"] == "image/png",
            staged["mime_type"],
        )
        listed = s.c.call("file-list-input")["entries"]
        s.check(
            "file-list-input now sees it, which it could not before",
            any(e["relative_path"] == staged_name for e in listed),
            [e["relative_path"] for e in listed][:10],
        )
        # The point of staging: every existing staged_path tool now works, which
        # is how a generated image becomes a block in a page.
        from_staged = s.c.call(
            "file-upload", space_id=s.space_id, staged_path=staged_name, type="image"
        )
        s.track(from_staged["object_id"])
        s.check(
            "a staged attachment can be used by the ordinary path-based tools",
            bool(from_staged.get("object_id")),
            from_staged,
        )
        status, msg = s.c.try_call(
            "file-stage-attachment", filename=staged_name, file=file_arg("x.png")
        )
        s.check(
            "an existing name is refused unless overwrite is asked for",
            status == "err" and "overwrite" in msg,
            msg,
        )

        # --- cover -----------------------------------------------------------
        result = s.c.call(
            "object-set-cover-from-attachment", space_id=s.space_id, object_id=obj,
            file=file_arg("livetest-cover.png"),
        )
        image_id = result.get("image_object_id")
        s.track(image_id)
        s.check(
            "a runtime-held image becomes the cover without ever touching the caller's context",
            result.get("cover_set") is True and bool(image_id),
            result,
        )
        s.check(
            "setting a cover leaves nothing staged behind",
            not staging_leftovers(),
            staging_leftovers(),
        )
        status, msg = s.c.try_call(
            "object-set-cover-from-attachment", space_id=s.space_id, object_id=obj,
            file=file_arg("livetest-cover.txt", mime_type="text/plain"),
        )
        s.check(
            "a name Anytype cannot render as a cover is refused",
            status == "err",
            msg,
        )

        # --- references that cannot be resolved ------------------------------
        # Each of these would otherwise produce an empty or wrong object, which
        # is the failure mode worth catching: silence, not an error.
        for args, label in (
            ({"file": {"file_id": "file_ohne_url"}},
             "a file argument without download_url"),
            ({"file": file_arg("x.png", url="https://httpbin.org/status/404")},
             "a download that returns 404"),
            ({"file": file_arg("x.png", url="http://127.0.0.1:31012/v1/spaces")},
             "a URL pointing at a loopback service"),
            ({}, "no file argument at all"),
        ):
            status, msg = s.c.try_call(
                "file-upload-attachment", space_id=s.space_id, **args
            )
            s.check(f"{label} is refused", status == "err", msg)
    finally:
        (IN_ROOT / staged_name).unlink(missing_ok=True)
