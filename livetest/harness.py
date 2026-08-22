"""Minimal MCP-over-stdio client and assertion helpers for the live suites.

The suites talk to a REAL Anytype instance through the same launcher the tunnel
uses, so everything they create is named with a PROBE_PREFIX and archived again
by the runner. Nothing here mocks anything: these tests exist precisely because
the interesting failures are ones anytype-heart only reveals when it runs.
"""

import json
import os
import pathlib
import subprocess
import threading
import time

PROBE_PREFIX = "LIVETEST"


def _require_env(name, what):
    """Read a path from the environment, or explain what to set and why.

    These used to carry the defaults of the machine they were written on, which
    is worse than having none: a wrong path fails deep inside a suite instead of
    at startup, and it points at a directory that only existed there.
    """
    value = os.environ.get(name, "").strip()
    if not value:
        raise SystemExit(
            f"{name} is not set.\n"
            f"  It must point to {what}.\n"
            f"  Example: export {name}=/srv/anytype/shared"
        )
    return value


# Host path of the directory shared with the Anytype server, used for staging
# imports and receiving exports.
SHARED_ROOT = _require_env(
    "ANYTYPE_LIVETEST_SHARED_ROOT",
    "the directory shared with the Anytype server (mounted there as /data)",
)

# The launcher the suites drive the server through — the same one the client
# uses, so the tests exercise the real startup path.
LAUNCHER = _require_env(
    "ANYTYPE_MCP_LAUNCHER",
    "the script that starts anytype-extension-mcp on stdio",
)

# Where file-upload expects staged files. Defaults to SHARED_ROOT/in, which is
# the layout the server itself assumes.
IN_ROOT = os.environ.get("ANYTYPE_LIVETEST_IN_ROOT", "").strip() or os.path.join(
    SHARED_ROOT, "in"
)


class MCPError(RuntimeError):
    """A tool returned isError, or the transport failed."""


class Client:
    """Speaks JSON-RPC to the MCP server over its stdin/stdout."""

    def __init__(self, launcher=LAUNCHER):
        self.proc = subprocess.Popen(
            [launcher],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        )
        self._stderr = []
        threading.Thread(
            target=lambda: [self._stderr.append(line) for line in self.proc.stderr],
            daemon=True,
        ).start()
        self._id = 0
        init = self.rpc(
            "initialize",
            {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "livetest", "version": "1"},
            },
        )
        self.server_info = init["result"]["serverInfo"]
        self.tools = [t["name"] for t in self.rpc("tools/list")["result"]["tools"]]

    def rpc(self, method, params=None):
        self._id += 1
        self.proc.stdin.write(
            json.dumps(
                {"jsonrpc": "2.0", "id": self._id, "method": method, "params": params or {}}
            )
            + "\n"
        )
        self.proc.stdin.flush()
        while True:
            line = self.proc.stdout.readline()
            if not line:
                tail = "".join(self._stderr[-20:])
                raise MCPError(f"server closed stdout; stderr:\n{tail}")
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue  # the server may log non-JSON lines
            if msg.get("id") == self._id:
                return msg

    def call(self, tool, **args):
        """Call a tool and return its decoded payload; raises on tool errors."""
        resp = self.rpc("tools/call", {"name": tool, "arguments": args})
        if "error" in resp:
            raise MCPError(f"{tool}: transport error {resp['error']}")
        result = resp["result"]
        payload = json.loads(result["content"][0]["text"])
        if result.get("isError"):
            raise MCPError(f"{tool}: {payload}")
        return payload

    def try_call(self, tool, **args):
        """Like call, but returns ("ok", payload) or ("err", message)."""
        try:
            return "ok", self.call(tool, **args)
        except MCPError as exc:
            return "err", str(exc)

    def close(self):
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        self.proc.terminate()


class Suite:
    """Collects checks and the objects a suite created, for reporting and cleanup."""

    def __init__(self, client, name):
        self.c = client
        self.name = name
        self.checks = []
        self.created = []
        self.space_id = client.call("list-spaces")["spaces"][0]["id"]

    # --- assertions --------------------------------------------------------

    def check(self, label, condition, detail=""):
        """Assert on an OBSERVED END STATE, never on "the call returned ok".

        Three of four early failures in this project were assertions that only
        proved a call succeeded, so prefer reading the thing back.
        """
        self.checks.append((label, bool(condition), str(detail)[:200]))
        return bool(condition)

    def failures(self):
        return [(label, detail) for label, ok, detail in self.checks if not ok]

    # --- fixtures ----------------------------------------------------------

    def probe(self, label):
        """Name for a throwaway object, recognisable to the cleanup pass."""
        return f"{PROBE_PREFIX} {self.name} {label}"

    def page(self, label, type_key="page"):
        """Create a throwaway object and remember it for cleanup."""
        res = self.c.call(
            "create-objects-compact-many",
            space_id=self.space_id,
            items=[{"type_key": type_key, "name": self.probe(label)}],
        )
        oid = res["objects"][0]["object_id"]
        self.created.append(oid)
        return oid

    def track(self, *object_ids):
        """Remember objects created by something other than page()."""
        self.created.extend(i for i in object_ids if i)

    # --- helpers -----------------------------------------------------------

    def blocks(self, object_id):
        return self.c.call("block-list", space_id=self.space_id, object_id=object_id)["blocks"]

    def block(self, object_id, block_id):
        for b in self.blocks(object_id):
            if b["id"] == block_id:
                return b
        return None

    def body_text(self, object_id):
        """Text blocks of an object without the title, in document order."""
        return [
            (b.get("style"), b.get("text", ""))
            for b in self.blocks(object_id)
            if b["kind"] == "text" and b["id"] != "title"
        ]

    def type_by_key(self, key):
        types = self.c.call("list-types-compact", space_id=self.space_id, limit=100)
        return next((t for t in (types.get("types") or []) if t.get("key") == key), None)

    def property_by_key(self, key):
        props = self.c.call("list-properties", space_id=self.space_id, limit=100)["properties"]
        return next((p for p in props if p.get("key") == key), None)

    def settle(self, seconds=1.5):
        """Wait for the REST search index, which lags writes by about a second."""
        time.sleep(seconds)

    def remove_shared(self, *relative_paths):
        """Delete paths from the shared files directory.

        Anything the Anytype server wrote there belongs to the container user,
        so the test process cannot unlink it directly. A throwaway container
        with the same mount can. Docker is already a hard requirement — the
        launcher itself is a docker run — so this adds no new dependency.
        """
        targets = []
        for raw in relative_paths:
            rel = (raw or "").strip("/")
            if not rel:
                continue
            # This builds an "rm -rf" that runs as root against a host mount.
            # Today every caller passes a literal, but a computed path is one
            # edit away, so refuse anything that could climb out of /data.
            if os.path.isabs(raw) or ".." in pathlib.PurePosixPath(rel).parts:
                raise ValueError(f"refusing to delete outside the shared root: {raw!r}")
            targets.append(rel)
        if not targets:
            return
        script = " ".join(f"rm -rf '/data/{t}';" for t in targets)
        subprocess.run(
            ["docker", "run", "--rm", "-v", f"{SHARED_ROOT}:/data",
             "alpine:latest", "sh", "-c", script],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False,
        )
