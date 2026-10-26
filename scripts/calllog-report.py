#!/usr/bin/env python3
"""Summarise the call log (ANYTYPE_CALL_LOG_DIR, one calls-YYYY-MM-DD.jsonl per day).

    scripts/calllog-report.py DIR [--days N] [--client TEXT] [--tool NAME] [--chat HASH]

Sections: who connected; ChatGPT conversations (one tunnel process carries
them all; the "chat" field tells them apart); per tool calls, failures, time
and size; calls repeated with the same arguments (repeated writes are the
ones that matter); export links handed out but never read; the most frequent
errors; the slowest calls. Reads only the log, never Anytype.
"""

import argparse
import collections
import datetime
import json
import pathlib
import statistics
import sys

REPEAT_WINDOW_S = 120


def load(directory, days):
    cutoff = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=days)).date().isoformat() if days else ""
    entries = []
    for path in sorted(pathlib.Path(directory).glob("calls-*.jsonl")):
        if path.stem.removeprefix("calls-") < cutoff:
            continue
        with path.open(encoding="utf-8") as fh:
            for line in fh:
                try:
                    e = json.loads(line)
                except ValueError:
                    continue
                if "method" in e:
                    e["_t"] = datetime.datetime.fromisoformat(e["ts"].replace("Z", "+00:00"))
                    entries.append(e)
    entries.sort(key=lambda e: e["_t"])
    # A process learns its client at initialize; lines written before that
    # (server/discover) belong to the same client.
    client_of = {}
    for e in entries:
        if e.get("client"):
            client_of.setdefault(e["proc"], e["client"])
    for e in entries:
        if not e.get("client") and e["proc"] in client_of:
            e["client"] = client_of[e["proc"]]
    return entries


def pct(values, q):
    if not values:
        return 0
    values = sorted(values)
    return values[min(len(values) - 1, int(round(q * (len(values) - 1))))]


def table(rows, header):
    widths = [max(len(str(x)) for x in col) for col in zip(header, *rows)] if rows else [len(h) for h in header]
    fmt = "  ".join("{:<%d}" % w if i == 0 else "{:>%d}" % w for i, w in enumerate(widths))
    print(fmt.format(*header))
    for r in rows:
        print(fmt.format(*r))


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("dir")
    ap.add_argument("--days", type=int, default=0, help="only the last N days")
    ap.add_argument("--client", default="", help="only clients whose name contains this")
    ap.add_argument("--tool", default="", help="only this tool")
    ap.add_argument("--chat", default="", help="only the conversation whose hash starts with this")
    args = ap.parse_args()

    entries = load(args.dir, args.days)
    if args.client:
        entries = [e for e in entries if args.client in e.get("client", "")]
    if args.chat:
        entries = [e for e in entries if e.get("chat", "").startswith(args.chat)]
    if not entries:
        sys.exit("no log lines")
    calls = [e for e in entries if e["method"] == "tools/call" and (not args.tool or e.get("tool") == args.tool)]

    print(f"{len(entries)} messages, {entries[0]['ts'][:19]} .. {entries[-1]['ts'][:19]} UTC\n")

    print("== Sessions")
    sessions = collections.defaultdict(lambda: {"procs": set(), "calls": 0})
    for e in entries:
        key = (e.get("consumer", "?"), e.get("client", "(no initialize)"))
        sessions[key]["procs"].add(e["proc"])
        sessions[key]["calls"] += e["method"] == "tools/call"
    table([(f"{c} / {cl}", len(v["procs"]), v["calls"]) for (c, cl), v in sorted(sessions.items())],
          ("consumer / client", "sessions", "tool calls"))

    print("\n== Chats")
    chats = collections.defaultdict(list)
    for e in entries:
        if e.get("chat"):
            chats[e["chat"]].append(e)
    if chats:
        rows = []
        for chat, es in sorted(chats.items(), key=lambda kv: kv[1][0]["_t"]):
            tools = collections.Counter(e.get("tool") for e in es if e["method"] == "tools/call")
            writes = sum(1 for e in es if e.get("writes"))
            rows.append((chat, es[0]["ts"][:19], es[-1]["ts"][11:19], sum(tools.values()), writes,
                         ", ".join(f"{t} {n}" for t, n in tools.most_common(3))))
        table(rows, ("chat", "first", "last", "tool calls", "writes", "most used"))
    else:
        print("none (only ChatGPT names its conversations, since v0.39)")

    print("\n== Tools")
    by_tool = collections.defaultdict(list)
    for e in calls:
        by_tool[e.get("tool", "?")].append(e)
    rows = []
    for tool, es in sorted(by_tool.items(), key=lambda kv: -len(kv[1])):
        ms = [e.get("ms", 0) for e in es]
        failed = sum(1 for e in es if e.get("ok") is False)
        size = [e.get("bytes", 0) for e in es]
        reused = sum(1 for e in es if e.get("reused"))
        cut = sum(1 for e in es if e.get("cut"))  # answers shortened to the size budget, since v0.47
        rows.append((tool, len(es), f"{failed} ({100 * failed // len(es)}%)", pct(ms, 0.5), pct(ms, 0.95),
                     f"{statistics.mean(size) / 1024:.1f}", reused, cut))
    table(rows, ("tool", "calls", "failed", "p50 ms", "p95 ms", "avg KB", "reused", "cut"))

    print(f"\n== Repeats (same tool and arguments within {REPEAT_WINDOW_S} s)")
    last, repeats = {}, collections.Counter()
    gaps = collections.defaultdict(list)
    for e in calls:
        # The same call from two conversations is no repeat.
        key = (e.get("chat") or e["proc"], e.get("tool"), e.get("args_hash"))
        prev = last.get(key)
        if prev and (e["_t"] - prev).total_seconds() <= REPEAT_WINDOW_S:
            repeats[(e.get("tool"), bool(e.get("writes")))] += 1
            gaps[e.get("tool")].append((e["_t"] - prev).total_seconds())
        last[key] = e["_t"]
    if repeats:
        table([(tool, "write" if writes else "read", n, f"{statistics.median(gaps[tool]):.0f}")
               for (tool, writes), n in repeats.most_common()], ("tool", "kind", "repeats", "median gap s"))
    else:
        print("none")

    print("\n== Export links")
    issued = {}
    for e in calls:
        for h in e.get("links", []):
            issued.setdefault(h, e.get("tool"))
    read = {e.get("uri") for e in entries if e["method"] == "resources/read" and e.get("ok")}
    per_tool = collections.defaultdict(lambda: [0, 0])
    for h, tool in issued.items():
        per_tool[tool][0] += 1
        per_tool[tool][1] += h in read
    if per_tool:
        table([(tool, n, r, n - r) for tool, (n, r) in sorted(per_tool.items())], ("tool", "links", "read", "never read"))
    else:
        print("none")

    print("\n== Errors")
    errors = collections.Counter((e.get("tool") or e["method"], e.get("error", "")) for e in entries if e.get("ok") is False)
    for (where, msg), n in errors.most_common(15):
        print(f"{n:>4}  {where}: {msg[:160]}")
    if not errors:
        print("none")

    print("\n== Slowest calls")
    for e in sorted(calls, key=lambda e: -e.get("ms", 0))[:10]:
        print(f"{e.get('ms', 0):>7} ms  {e['ts'][:19]}  {e.get('tool')}")


if __name__ == "__main__":
    main()
