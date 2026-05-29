#!/usr/bin/env python3
"""Trace analyzer / efficiency oracle for the adversarial eval suite.

Reads a crush-dev --trace-file JSONL (holographic, one Sync'd event per line)
and emits structured metrics that the per-scenario oracle asserts against:

  - tool coverage: which tools fired, counts
  - turns: assistant request count (LLM round-trips)
  - concurrency: max simultaneously-running tools (overlapping started/finished),
    and per-parent fan-out width — the explore-parallelism signal
  - context growth: context_bytes / total context tokens per request over time
  - timing: wall-clock span, per-tool wall time, first-event latency
  - tokens/cost totals

Usage: analyze_trace.py <trace.jsonl> [--json]
It does NOT judge pass/fail (that's scenario-specific ground truth); it produces
the measurements a verdict is built from.
"""
import json
import sys
from collections import defaultdict


def load(path):
    events = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                events.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return events


def parse_ts(s):
    # RFC3339 nanos -> float seconds; tolerate empty.
    if not s:
        return None
    s = s.replace("Z", "+00:00")
    try:
        from datetime import datetime
        return datetime.fromisoformat(s).timestamp()
    except ValueError:
        return None


def analyze(events):
    tool_started = [e for e in events if e.get("kind") == "tool_started"]
    tool_done = [e for e in events if e.get("kind") in ("tool_finished", "tool_failed")]

    coverage = defaultdict(int)
    for e in tool_started:
        coverage[e.get("tool_name", "?")] += 1

    # Concurrency: build [start,end] intervals per tool call, count max overlap.
    def sane_ts(t):
        return t is not None and 1.5e9 < t < 2.05e9

    intervals = []
    for e in tool_done:
        st = parse_ts(e.get("started_at"))
        fn = parse_ts(e.get("finished_at"))
        if sane_ts(st) and sane_ts(fn) and fn >= st:
            intervals.append((st, fn, e.get("tool_name", "?"), e.get("parent_id", "")))
    max_overlap = 0
    points = []
    for st, fn, _, _ in intervals:
        points.append((st, 1))
        points.append((fn, -1))
    points.sort()
    cur = 0
    for _, d in points:
        cur += d
        max_overlap = max(max_overlap, cur)

    # Fan-out width per parent (how many child tool calls share a parent_id) —
    # the explore-subagent parallelism signal.
    by_parent = defaultdict(list)
    for st, fn, name, parent in intervals:
        if parent:
            by_parent[parent].append((st, fn, name))
    max_fanout = max((len(v) for v in by_parent.values()), default=0)

    # agent (subagent) spawns specifically.
    agent_spawns = coverage.get("agent", 0) + coverage.get("agent_tool", 0)

    # Context growth over LLM requests (request_started/llm events carry these).
    ctx_curve = []
    for e in events:
        cb = e.get("context_bytes")
        if cb or e.get("context_message_count"):
            ctx_curve.append({
                "seq": e.get("sequence"),
                "context_bytes": cb or 0,
                "context_messages": e.get("context_message_count", 0),
                "input_tokens": e.get("input_tokens", 0),
                "cache_read_tokens": e.get("cache_read_tokens", 0),
            })

    # Turns ~= distinct assistant LLM round-trips. Approximate via max step_number
    # and count of events that carry finish_reason.
    turns = max((e.get("step_number", 0) for e in events), default=0)
    finishes = sum(1 for e in events if e.get("finish_reason"))

    # Timing span. Clamp to a plausible epoch window [2017, 2035] so one
    # malformed/zero/far-future timestamp can't blow the span to centuries
    # (observed: a stray event yielded a year-4000 value).
    def sane(t):
        return t is not None and 1.5e9 < t < 2.05e9

    all_ts = [t for e in events for t in (parse_ts(e.get("recorded_at")),) if sane(t)]
    wall = (max(all_ts) - min(all_ts)) if len(all_ts) >= 2 else 0.0

    # Per-tool wall time (sum of durations) — where time goes.
    tool_wall = defaultdict(float)
    for e in tool_done:
        tool_wall[e.get("tool_name", "?")] += (e.get("duration_ms", 0) or 0) / 1000.0

    totals = {
        "input_tokens": sum(e.get("input_tokens", 0) or 0 for e in events),
        "output_tokens": sum(e.get("output_tokens", 0) or 0 for e in events),
        "reasoning_tokens": sum(e.get("reasoning_tokens", 0) or 0 for e in events),
        "cache_read_tokens": sum(e.get("cache_read_tokens", 0) or 0 for e in events),
        "est_cost_usd": round(sum(e.get("estimated_cost_usd", 0) or 0 for e in events), 4),
    }

    return {
        "tool_coverage": dict(sorted(coverage.items(), key=lambda x: -x[1])),
        "tool_call_total": sum(coverage.values()),
        "agent_subagent_spawns": agent_spawns,
        "max_concurrent_tools": max_overlap,
        "max_parent_fanout": max_fanout,
        "turns_step_max": turns,
        "llm_finishes": finishes,
        "wall_clock_sec": round(wall, 1),
        "tool_wall_sec": {k: round(v, 1) for k, v in sorted(tool_wall.items(), key=lambda x: -x[1])},
        "context_growth": ctx_curve[-1] if ctx_curve else {},
        "context_bytes_peak": max((c["context_bytes"] for c in ctx_curve), default=0),
        "tokens": totals,
    }


def main():
    if len(sys.argv) < 2:
        print("usage: analyze_trace.py <trace.jsonl> [--json]", file=sys.stderr)
        sys.exit(2)
    events = load(sys.argv[1])
    metrics = analyze(events)
    print(json.dumps(metrics, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
