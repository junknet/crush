#!/usr/bin/env python3
"""Run real Crush role/model E2E cases with the user's configured providers."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path


REPO = Path(__file__).resolve().parents[1]
CASE_DIR = REPO / "bench" / "real_role_matrix"
DEFAULT_CASES = CASE_DIR / "cases.jsonl"
USER_CONFIG = Path.home() / ".config" / "crush" / "crush.yaml"
STATE_ROOT = Path.home() / ".local" / "state" / "crush-real-bench"


def load_cases(path: Path) -> list[dict]:
    cases: list[dict] = []
    with path.open("r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            cases.append(json.loads(line))
    return cases


def state_yaml(case: dict) -> str:
    effort = str(case.get("reasoning_effort") or "").strip()
    max_tokens = int(case.get("max_tokens") or 8192)
    lines = [
        "models:",
        "  brain:",
        f"    provider: {case['provider']}",
        f"    model: {case['model']}",
        f"    max_tokens: {max_tokens}",
    ]
    if effort:
        lines.append(f"    reasoning_effort: {effort}")
    if case.get("think"):
        lines.append("    think: true")
    if case.get("thinking_budget"):
        lines.append(f"    thinking_budget: {int(case['thinking_budget'])}")
    lines.extend(
        [
            "  explore:",
            "    provider: antigravity",
            "    model: gemini-3.5-flash-extra-low",
            "    max_tokens: 8192",
            "  worker:",
            f"    provider: {case['provider']}",
            f"    model: {case['model']}",
            f"    max_tokens: {max_tokens}",
            "  plan:",
            f"    provider: {case['provider']}",
            f"    model: {case['model']}",
            f"    max_tokens: {max_tokens}",
            "  auditor:",
            f"    provider: {case['provider']}",
            f"    model: {case['model']}",
            f"    max_tokens: {max_tokens}",
        ]
    )
    if effort:
        lines.extend(
            [
                f"    reasoning_effort: {effort}",
            ]
        )
    if case.get("think"):
        lines.append("    think: true")
    if case.get("thinking_budget"):
        lines.append(f"    thinking_budget: {int(case['thinking_budget'])}")
    return "\n".join(lines) + "\n"


def parse_trace(trace_path: Path | None) -> dict:
    metrics = {
        "trace": str(trace_path) if trace_path else "",
        "provider": "",
        "provider_type": "",
        "model": "",
        "success": False,
        "duration_ms": 0,
        "first_event_latency_ms": 0,
        "input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "cache_creation_tokens": 0,
        "cache_read_tokens": 0,
        "estimated_cost_usd": 0.0,
        "tool_started": 0,
        "tool_finished": 0,
        "tool_failed": 0,
        "llm_requests": 0,
        "finish_reason": "",
        "error": "",
    }
    if not trace_path or not trace_path.exists():
        return metrics
    for line in trace_path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        kind = ev.get("kind")
        is_root = ev.get("parent_id") == ""
        if kind == "tool_started":
            metrics["tool_started"] += 1
        elif kind == "tool_finished":
            metrics["tool_finished"] += 1
        elif kind == "tool_failed":
            metrics["tool_failed"] += 1
        elif kind == "llm_request_started":
            metrics["llm_requests"] += 1
        elif kind == "llm_first_event" and is_root:
            metrics["first_event_latency_ms"] = max(
                metrics["first_event_latency_ms"],
                int(ev.get("first_event_latency_ms") or 0),
            )
        elif is_root and kind in ("llm_request_finished", "task_finished", "task_failed"):
            for key in (
                "provider_id",
                "provider_type",
                "model_id",
                "input_tokens",
                "output_tokens",
                "reasoning_tokens",
                "cache_creation_tokens",
                "cache_read_tokens",
                "estimated_cost_usd",
                "finish_reason",
            ):
                if key in ev and ev[key] not in ("", 0, None):
                    out_key = {
                        "provider_id": "provider",
                        "model_id": "model",
                    }.get(key, key)
                    metrics[out_key] = ev[key]
            if kind == "task_finished":
                metrics["success"] = bool(ev.get("success"))
                metrics["duration_ms"] = int(ev.get("duration_ms") or 0)
            elif kind == "task_failed":
                metrics["success"] = False
                metrics["duration_ms"] = int(ev.get("duration_ms") or 0)
        if ev.get("error"):
            metrics["error"] = str(ev.get("error"))[:500]
    return metrics


def find_trace(text: str) -> Path | None:
    matches = re.findall(r"trace=([^\s]+)", text)
    if not matches:
        return None
    return Path(matches[-1])


def run_case(case: dict, out_root: Path, timeout_s: int) -> dict:
    case_out = out_root / case["id"]
    case_out.mkdir(parents=True, exist_ok=True)
    prompt = (CASE_DIR / case["prompt"]).read_text(encoding="utf-8")
    stdout_path = case_out / "stdout.txt"
    stderr_path = case_out / "stderr.txt"
    raw_path = case_out / "raw.log"

    with tempfile.TemporaryDirectory(prefix="crush-real-config-") as cfg:
        cfg_dir = Path(cfg)
        shutil.copy2(USER_CONFIG, cfg_dir / "crush.yaml")
        (cfg_dir / "state.yaml").write_text(state_yaml(case), encoding="utf-8")

        env = os.environ.copy()
        env["CRUSH_GLOBAL_CONFIG"] = str(cfg_dir)
        env["CRUSH_GLOBAL_DATA"] = str(case_out / "data")
        env["CRUSH_DISABLE_METRICS"] = "1"
        env["CRUSH_DISABLE_PROVIDER_AUTO_UPDATE"] = "1"
        for key in ("CRUSH_MOCK_API_KEY", "CRUSH_MOCK_KEY", "CRUSH_MOCK_BASE", "CRUSH_MOCK_LLM_BASE"):
            env.pop(key, None)

        started = time.time()
        proc = subprocess.run(
            ["timeout", str(timeout_s), "crush-dev", "run", "--quiet", prompt],
            cwd=case["cwd"],
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        wall_ms = int((time.time() - started) * 1000)

    stdout_path.write_text(proc.stdout, encoding="utf-8", errors="replace")
    stderr_path.write_text(proc.stderr, encoding="utf-8", errors="replace")
    raw = proc.stderr + "\n" + proc.stdout
    raw_path.write_text(raw, encoding="utf-8", errors="replace")

    trace = find_trace(raw)
    metrics = parse_trace(trace)
    output_for_check = proc.stdout + "\n" + raw
    expected = case.get("expect") or []
    hits = [term for term in expected if term.lower() in output_for_check.lower()]
    result = {
        "id": case["id"],
        "role": case["role"],
        "cwd": case["cwd"],
        "reasoning_effort": case.get("reasoning_effort") or "",
        "exit_code": proc.returncode,
        "wall_ms": wall_ms,
        "stdout": str(stdout_path),
        "stderr": str(stderr_path),
        "raw": str(raw_path),
        "expected_hits": hits,
        "expected_total": len(expected),
        "passed_expectations": len(hits) == len(expected),
        **metrics,
        "provider": case["provider"],
        "model": case["model"],
    }
    return result


def write_report(results: list[dict], out_root: Path) -> None:
    lines = [
        "# Crush Real Role Matrix",
        "",
        f"Run directory: `{out_root}`",
        "",
        "| case | provider/model | exit | trace success | tools | first token | duration | expectations | trace |",
        "|---|---|---:|---:|---:|---:|---:|---:|---|",
    ]
    for r in results:
        tools = f"{r['tool_finished']}/{r['tool_started']} failed={r['tool_failed']}"
        first = f"{r['first_event_latency_ms']}ms" if r["first_event_latency_ms"] else "-"
        dur = f"{r['duration_ms']}ms" if r["duration_ms"] else f"{r['wall_ms']}ms"
        exp = f"{len(r['expected_hits'])}/{r['expected_total']}"
        trace = r.get("trace") or ""
        lines.append(
            f"| `{r['id']}` | `{r['provider']}/{r['model']}` | {r['exit_code']} | "
            f"{str(r['success']).lower()} | {tools} | {first} | {dur} | {exp} | `{trace}` |"
        )
    lines.append("")
    lines.append("Raw outputs are stored in each case directory as `stdout.txt`, `stderr.txt`, and `raw.log`.")
    (out_root / "REPORT.md").write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--cases", type=Path, default=DEFAULT_CASES)
    parser.add_argument("--only", action="append", default=[])
    parser.add_argument("--timeout", type=int, default=300)
    args = parser.parse_args()

    if not USER_CONFIG.exists():
        print(f"missing real Crush config: {USER_CONFIG}", file=sys.stderr)
        return 2

    run_id = dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    out_root = STATE_ROOT / run_id
    out_root.mkdir(parents=True, exist_ok=True)
    cases = load_cases(args.cases)
    if args.only:
        allow = set(args.only)
        cases = [case for case in cases if case["id"] in allow or case["role"] in allow]

    results = []
    result_path = out_root / "results.jsonl"
    for case in cases:
        print(f"[bench] {case['id']} -> {case['provider']}/{case['model']}", flush=True)
        result = run_case(case, out_root, args.timeout)
        results.append(result)
        with result_path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(result, ensure_ascii=False) + "\n")
        status = "PASS" if result["exit_code"] == 0 and result["success"] and result["passed_expectations"] else "FAIL"
        print(
            f"[bench] {case['id']} {status} trace={result.get('trace','')} "
            f"tools={result['tool_finished']}/{result['tool_started']} failed={result['tool_failed']}",
            flush=True,
        )

    write_report(results, out_root)
    print(f"[bench] report={out_root / 'REPORT.md'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
