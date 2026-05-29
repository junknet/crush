#!/usr/bin/env python3
"""Corpus aggregator over the accumulated crush-dev --trace-file JSONL history.

crush-dev writes a holographic, Sync'd trace for every run; ~/.local/state/
crush-dev has hundreds of them from crush's own iteration. This streams them
(newest first), single pass, and aggregates the real behavioral signals that
single-scenario eval cannot — answering questions like "across N real sessions,
did the explore subagent EVER fan out?" and "which tools actually fail in
practice?".

Usage: aggregate_corpus.py <dir> [max_files]
"""
import glob
import json
import os
import sys
from collections import defaultdict


def main():
    d = sys.argv[1] if len(sys.argv) > 1 else os.path.expanduser("~/.local/state/crush-dev")
    cap = int(sys.argv[2]) if len(sys.argv) > 2 else 0
    files = sorted(glob.glob(os.path.join(d, "trace-*.jsonl")), reverse=True)  # newest first
    if cap:
        files = files[:cap]

    sessions = 0
    empty = 0
    tool_calls = defaultdict(int)
    tool_fail = defaultdict(int)
    tool_dur_ms = defaultdict(float)
    agent_spawn_total = 0
    sessions_with_agent = 0
    sessions_with_fail = 0
    global_max_concurrent = 0
    sessions_concurrent_gt1 = 0
    autosummarize_triggered = 0
    max_context_bytes = 0
    fail_msgs = defaultdict(int)
    finish_reasons = defaultdict(int)

    def sane(t):
        return t is not None and 1.5e9 < t < 2.05e9

    def pts(s):
        if not s:
            return None
        try:
            from datetime import datetime
            return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()
        except ValueError:
            return None

    for fp in files:
        sessions += 1
        starts = []  # (ts, +1/-1) for concurrency
        this_agent = 0
        this_fail = 0
        n = 0
        try:
            with open(fp) as fh:
                for line in fh:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        e = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    n += 1
                    kind = e.get("kind")
                    if kind == "tool_started":
                        name = e.get("tool_name", "?")
                        tool_calls[name] += 1
                        if name in ("agent", "agent_tool"):
                            this_agent += 1
                        st = pts(e.get("started_at"))
                        if sane(st):
                            starts.append((st, 1))
                    elif kind in ("tool_finished", "tool_failed"):
                        name = e.get("tool_name", "?")
                        tool_dur_ms[name] += e.get("duration_ms", 0) or 0
                        fn = pts(e.get("finished_at"))
                        if sane(fn):
                            starts.append((fn, -1))
                        if kind == "tool_failed":
                            tool_fail[name] += 1
                            this_fail += 1
                            msg = (e.get("error") or e.get("tool_output") or "")[:80]
                            if msg:
                                fail_msgs[msg] += 1
                    if e.get("finish_reason"):
                        finish_reasons[e["finish_reason"]] += 1
                    if e.get("auto_summarize_triggered"):
                        autosummarize_triggered += 1
                    cb = e.get("context_bytes") or 0
                    if cb > max_context_bytes:
                        max_context_bytes = cb
        except OSError:
            continue
        if n == 0:
            empty += 1
        agent_spawn_total += this_agent
        if this_agent:
            sessions_with_agent += 1
        if this_fail:
            sessions_with_fail += 1
        # concurrency for this session
        starts.sort()
        cur = mx = 0
        for _, delta in starts:
            cur += delta
            mx = max(mx, cur)
        global_max_concurrent = max(global_max_concurrent, mx)
        if mx > 1:
            sessions_concurrent_gt1 += 1

    total_calls = sum(tool_calls.values())
    out = {
        "trace_files": len(files),
        "sessions_nonempty": sessions - empty,
        "sessions_empty": empty,
        "total_tool_calls": total_calls,
        "AGENT_SUBAGENT_SPAWNS_TOTAL": agent_spawn_total,
        "sessions_with_agent_spawn": sessions_with_agent,
        "global_max_concurrent_tools": global_max_concurrent,
        "sessions_with_concurrency_gt1": sessions_concurrent_gt1,
        "sessions_with_a_tool_failure": sessions_with_fail,
        "autosummarize_triggered_events": autosummarize_triggered,
        "max_context_bytes_seen": max_context_bytes,
        "tool_usage_top": dict(sorted(tool_calls.items(), key=lambda x: -x[1])[:25]),
        "tool_failure_rate": {
            k: f"{tool_fail[k]}/{tool_calls[k]} ({100*tool_fail[k]//max(tool_calls[k],1)}%)"
            for k in sorted(tool_fail, key=lambda x: -tool_fail[x])[:15]
        },
        "top_failure_messages": dict(sorted(fail_msgs.items(), key=lambda x: -x[1])[:12]),
        "finish_reasons": dict(sorted(finish_reasons.items(), key=lambda x: -x[1])),
    }
    print(json.dumps(out, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
