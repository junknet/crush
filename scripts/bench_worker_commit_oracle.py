#!/usr/bin/env python3
"""Worker-agent closed-loop E2E benchmark against a real historical commit.

This replaces the single-tool apply_patch benchmark with a transcript-mined
Crush engineering task. Each run starts from the parent of a real commit,
plants the target commit's oracle tests, strips git history, then drives
`crush run --role worker` with the user's real provider config.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tarfile
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import yaml


REPO = Path(__file__).resolve().parents[1]
CASE_DIR = REPO / "bench" / "real_role_matrix"
DEFAULT_CASES = CASE_DIR / "cases_worker_context_compaction.jsonl"
PROMPT_FILE = CASE_DIR / "worker_context_compaction.md"
USER_CONFIG = Path.home() / ".config" / "crush" / "crush.yaml"
USER_STATE = Path.home() / ".config" / "crush" / "state.yaml"
BENCH_BIN = os.environ.get("CRUSH_BENCH_BIN", "/tmp/crush-bench")
STATE_ROOT = Path.home() / ".local" / "state" / "crush-worker-commit-bench"

BASE_COMMIT = "a57d273841b263a096c65e8ad575f88892553fec"
TARGET_COMMIT = "c22f44ebe2372a0f96a93a99cd4eedd24f4a98ac"
TASK_NAME = "context_compaction"
ORACLE_FILES = [
    "internal/agent/context_compaction_test.go",
    "internal/agent/tools/dag_run_test.go",
]
TARGET_COPY_FILES = [
    "bench/real_role_matrix/worker_context_compaction.md",
]
TEST_COMMANDS = [
    ["go", "build", "./..."],
    ["go", "vet", "./internal/agent/...", "./internal/agent/tools/..."],
    ["go", "test", "./internal/agent", "-run", "TestCompactRedundantToolResults|TestNormalizeToolInput", "-count=1", "-v"],
    ["go", "test", "./internal/agent/tools", "-run", "TestDagRunToolBlocksForegroundSleepPolling", "-count=1", "-v"],
]

TASKS = {
    "context_compaction": {
        "default_cases": CASE_DIR / "cases_worker_context_compaction.jsonl",
        "prompt_file": CASE_DIR / "worker_context_compaction.md",
        "base_commit": "a57d273841b263a096c65e8ad575f88892553fec",
        "target_commit": "c22f44ebe2372a0f96a93a99cd4eedd24f4a98ac",
        "task_name": "context_compaction",
        "done_marker": "WORKER_DONE context_compaction",
        "oracle_files": [
            "internal/agent/context_compaction_test.go",
            "internal/agent/tools/dag_run_test.go",
        ],
        "target_copy_files": [
            "bench/real_role_matrix/worker_context_compaction.md",
        ],
        "test_commands": [
            ["go", "build", "./..."],
            ["go", "vet", "./internal/agent/...", "./internal/agent/tools/..."],
            ["go", "test", "./internal/agent", "-run", "TestCompactRedundantToolResults|TestNormalizeToolInput", "-count=1", "-v"],
            ["go", "test", "./internal/agent/tools", "-run", "TestDagRunToolBlocksForegroundSleepPolling", "-count=1", "-v"],
        ],
        "required_symbols": ["compactRedundantToolResults", "normalizeToolInput"],
        "red_commands": [
            ["go", "test", "./internal/agent", "-run", "TestCompactRedundantToolResults|TestNormalizeToolInput", "-count=1"],
            ["go", "test", "./internal/agent/tools", "-run", "TestDagRunToolBlocksForegroundSleepPolling", "-count=1"],
        ],
    },
    "hard_e2e_flows": {
        "default_cases": CASE_DIR / "cases_worker_hard_e2e.jsonl",
        "prompt_file": CASE_DIR / "worker_hard_e2e_flows.md",
        "base_commit": "f69d49a92add3db0931ce5ba9a65deb1abe57f57",
        "target_commit": "09574f8ae58df2ddb12455b0c642aac5f0de365a",
        "task_name": "hard_e2e_flows",
        "done_marker": "WORKER_DONE hard_e2e_flows",
        "oracle_files": [
            "acceptance/common.sh",
            "acceptance/scenarios/async_monitor_e2e.sh",
            "internal/agent/agent_test.go",
            "internal/agent/coordinator_test.go",
            "internal/agent/tools/code_triage_test.go",
            "internal/agent/tools/monitor_test.go",
            "internal/agent/tools/schedule_wakeup_test.go",
            "internal/runtime/session_test.go",
            "internal/shell/background_test.go",
        ],
        "target_copy_files": [
            "bench/real_role_matrix/worker_hard_e2e_flows.md",
        ],
        "test_commands": [
            ["go", "build", "./..."],
            ["go", "vet", "./internal/agent/...", "./internal/agent/tools/...", "./internal/runtime/...", "./internal/shell/..."],
            ["go", "test", "./internal/agent/tools", "-run", "TestCodeTriage|TestMonitorTool|TestScheduleWakeup|TestCron|TestScheduler|TestPublishWakeup", "-count=1", "-v"],
            ["go", "test", "./internal/runtime", "-run", "TestRuntimeSession", "-count=1", "-v"],
            ["go", "test", "./internal/shell", "-run", "TestBackgroundShell", "-count=1", "-v"],
            ["go", "test", "./internal/agent", "-run", "TestProviderRetryLogFields|TestCoordinatorPropagateSubAgentTracesDeduplicatesParentTrace|TestHandleBackgroundJobEventDoesNotMirrorToEventbus", "-count=1", "-v"],
            ["go", "build", "-o", "crush", "."],
        ],
        "required_symbols": ["NewCodeTriageTool", "NewMonitorTool", "AppendTrace"],
        "red_commands": [
            ["go", "test", "./internal/agent/tools", "-run", "TestCodeTriage|TestMonitorTool|TestScheduleWakeup|TestCron|TestScheduler|TestPublishWakeup", "-count=1"],
            ["go", "test", "./internal/runtime", "-run", "TestRuntimeSessionAppendTraceDeduplicatesIdenticalEntries", "-count=1"],
            ["go", "test", "./internal/shell", "-run", "TestBackgroundShellManager_MonitorSuppressesDoneWake", "-count=1"],
            ["go", "test", "./internal/agent", "-run", "TestProviderRetryLogFields|TestCoordinatorPropagateSubAgentTracesDeduplicatesParentTrace|TestHandleBackgroundJobEventDoesNotMirrorToEventbus", "-count=1"],
        ],
    },
}


def configure_task(task: str) -> None:
    global DEFAULT_CASES, PROMPT_FILE, BASE_COMMIT, TARGET_COMMIT, TASK_NAME
    global ORACLE_FILES, TARGET_COPY_FILES, TEST_COMMANDS
    spec = TASKS[task]
    DEFAULT_CASES = spec["default_cases"]
    PROMPT_FILE = spec["prompt_file"]
    BASE_COMMIT = spec["base_commit"]
    TARGET_COMMIT = spec["target_commit"]
    TASK_NAME = spec["task_name"]
    ORACLE_FILES = spec["oracle_files"]
    TARGET_COPY_FILES = spec["target_copy_files"]
    TEST_COMMANDS = spec["test_commands"]


def load_cases(path: Path) -> list[dict]:
    cases: list[dict] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line:
            cases.append(json.loads(line))
    return cases


def goenv(base: dict) -> dict:
    env = dict(base)
    env["CGO_ENABLED"] = "0"
    env["GOEXPERIMENT"] = "greenteagc"
    return env


def sh(cmd: list[str], cwd: Path, env: dict, timeout: int = 480) -> subprocess.CompletedProcess:
    return subprocess.run(
        cmd,
        cwd=str(cwd),
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=timeout,
        check=False,
    )


def git_show(commit: str, rel: str) -> str:
    return subprocess.run(
        ["git", "show", f"{commit}:{rel}"],
        cwd=str(REPO),
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=True,
    ).stdout


def archive_commit(dst: Path, commit: str) -> None:
    dst.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(["git", "archive", "--format=tar", commit], cwd=str(REPO), stdout=subprocess.PIPE)
    try:
        assert proc.stdout is not None
        with tarfile.open(fileobj=proc.stdout, mode="r|") as tar:
            tar.extractall(dst)
    finally:
        rc = proc.wait()
    if rc != 0:
        raise RuntimeError(f"git archive failed for {commit}: exit {rc}")


def make_replica(dst: Path) -> dict[str, str]:
    """Create a git-stripped replica at BASE_COMMIT and plant target oracle tests."""
    if dst.exists():
        shutil.rmtree(dst)
    archive_commit(dst, BASE_COMMIT)
    hashes: dict[str, str] = {}
    for rel in ORACLE_FILES:
        raw = git_show(TARGET_COMMIT, rel)
        path = dst / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(raw, encoding="utf-8")
        hashes[rel] = hashlib.sha256(raw.encode()).hexdigest()
    for rel in TARGET_COPY_FILES:
        src = REPO / rel
        if src.exists():
            out = dst / rel
            out.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, out)
    return hashes


def clone_replica(template: Path, dst: Path) -> None:
    if dst.exists():
        shutil.rmtree(dst)
    dst.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["cp", "-a", "--reflink=auto", str(template), str(dst)], check=True)


def write_state(cfg_dir: Path, case: dict) -> None:
    shutil.copy2(USER_CONFIG, cfg_dir / "crush.yaml")
    base = {}
    if USER_STATE.exists():
        base = yaml.safe_load(USER_STATE.read_text(encoding="utf-8")) or {}
    base.pop("recent_models", None)
    models = base.get("models", {}) or {}
    worker = {"provider": case["provider"], "model": case["model"]}
    for key in ("reasoning_effort", "max_tokens", "think", "thinking_budget"):
        if case.get(key) is not None:
            worker[key] = case[key]
    worker.setdefault("max_tokens", 32000)
    models["worker"] = worker
    base["models"] = models
    (cfg_dir / "state.yaml").write_text(yaml.safe_dump(base, allow_unicode=True), encoding="utf-8")


def parse_trace(trace: Path) -> dict:
    metrics = {
        "trace": str(trace),
        "root_model": "",
        "root_provider": "",
        "root_profile": "",
        "success": False,
        "duration_ms": 0,
        "first_event_latency_ms": 0,
        "input_tokens": 0,
        "output_tokens": 0,
        "reasoning_tokens": 0,
        "cache_creation_tokens": 0,
        "cache_read_tokens": 0,
        "llm_requests": 0,
        "tool_started": 0,
        "tool_finished": 0,
        "tool_failed": 0,
        "finish_reason": "",
        "error": "",
    }
    if not trace.exists():
        return metrics
    for line in trace.read_text(encoding="utf-8", errors="replace").splitlines():
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
            if is_root and not metrics["root_model"]:
                metrics["root_model"] = ev.get("model_id", "")
                metrics["root_provider"] = ev.get("provider_id", "")
                metrics["root_profile"] = ev.get("profile", "")
        elif kind == "llm_first_event" and is_root:
            metrics["first_event_latency_ms"] = max(metrics["first_event_latency_ms"], int(ev.get("first_event_latency_ms") or 0))
        elif is_root and kind in ("llm_request_finished", "task_finished", "task_failed"):
            for key in ("input_tokens", "output_tokens", "reasoning_tokens", "cache_creation_tokens", "cache_read_tokens", "finish_reason"):
                val = ev.get(key)
                if val not in ("", 0, None):
                    metrics[key] = metrics[key] + val if isinstance(val, int) and key.endswith("tokens") else val
            if kind == "task_finished":
                metrics["success"] = bool(ev.get("success"))
                metrics["duration_ms"] = int(ev.get("duration_ms") or 0)
            elif kind == "task_failed":
                metrics["duration_ms"] = int(ev.get("duration_ms") or 0)
        if ev.get("error"):
            metrics["error"] = str(ev.get("error"))[:300]
    return metrics


def verify(replica: Path, env: dict, oracle_hashes: dict[str, str]) -> dict:
    verdict: dict[str, object] = {}
    for rel, expected_hash in oracle_hashes.items():
        path = replica / rel
        raw = path.read_text(encoding="utf-8", errors="replace") if path.exists() else ""
        prefix = rel.replace("/", "_").replace(".", "_")
        sha_ok = hashlib.sha256(raw.encode()).hexdigest() == expected_hash
        verdict[f"{prefix}_present"] = path.exists()
        verdict[f"{prefix}_sha_ok"] = sha_ok
        verdict[f"{prefix}_no_skip_cheat"] = sha_ok or not any(s in raw for s in ("t.Skip", "SkipNow", "//go:build ignore", "+build ignore"))
    command_results = []
    for cmd in TEST_COMMANDS:
        try:
            proc = sh(cmd, replica, env)
            ok = proc.returncode == 0
            output = proc.stdout[-2400:]
        except subprocess.TimeoutExpired:
            ok = False
            output = "TIMEOUT"
        key = "cmd_" + "_".join(part.replace("/", "_").replace(".", "_").replace("-", "_") for part in cmd[:4])
        verdict[f"{key}_ok"] = ok
        command_results.append({"cmd": cmd, "ok": ok, "tail": output})
    verdict["commands"] = command_results
    agent_impl = ""
    for path in (replica / "internal/agent").glob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        agent_impl += path.read_text(encoding="utf-8", errors="replace")
    required_symbols = TASKS[TASK_NAME]["required_symbols"]
    verdict["implementation_symbol_present"] = all(symbol in agent_impl for symbol in required_symbols)
    verdict["closed_loop_pass"] = (
        all(bool(verdict[k]) for k in verdict if k.endswith("_sha_ok") or k.endswith("_no_skip_cheat"))
        and all(r["ok"] for r in command_results)
        and bool(verdict["implementation_symbol_present"])
    )
    return verdict


def red_start_ok(replica: Path, env: dict) -> bool:
    failures = 0
    for cmd in TASKS[TASK_NAME]["red_commands"]:
        proc = sh(cmd, replica, env, timeout=240)
        if proc.returncode != 0:
            failures += 1
    return failures == len(TASKS[TASK_NAME]["red_commands"])


def run_case(case: dict, out_root: Path, timeout_s: int, template: Path | None = None, oracle_hashes: dict[str, str] | None = None) -> dict:
    cid = case["id"]
    cdir = out_root / cid
    cdir.mkdir(parents=True, exist_ok=True)
    replica = cdir / "replica"
    cfg = cdir / "cfg"
    cfg.mkdir(exist_ok=True)

    print(f"[bench] {cid}: creating {TASK_NAME} replica from {BASE_COMMIT[:7]} ...", flush=True)
    if template is not None:
        clone_replica(template, replica)
        if oracle_hashes is None:
            raise RuntimeError("template replica requires oracle_hashes")
    else:
        oracle_hashes = make_replica(replica)
    write_state(cfg, case)

    env = goenv(os.environ)
    env["CRUSH_GLOBAL_CONFIG"] = str(cfg)
    env["CRUSH_GLOBAL_DATA"] = str(cdir / "data")
    env["CRUSH_DISABLE_METRICS"] = "1"
    env["CRUSH_DISABLE_PROVIDER_AUTO_UPDATE"] = "1"
    for key in ("CRUSH_MOCK_API_KEY", "CRUSH_MOCK_KEY", "CRUSH_MOCK_BASE", "CRUSH_MOCK_LLM_BASE"):
        env.pop(key, None)

    red_ok = red_start_ok(replica, env)
    prompt = PROMPT_FILE.read_text(encoding="utf-8")
    trace = cdir / "trace.jsonl"
    print(f"[bench] {cid}: running worker ({case['provider']}/{case['model']}) timeout={timeout_s}s ...", flush=True)
    started = time.time()
    proc = subprocess.run(
        ["timeout", str(timeout_s), BENCH_BIN, "run", "--role", "worker", "--quiet", "--trace-file", str(trace), prompt],
        cwd=str(replica),
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    wall_ms = int((time.time() - started) * 1000)
    (cdir / "stdout.txt").write_text(proc.stdout, encoding="utf-8", errors="replace")
    (cdir / "stderr.txt").write_text(proc.stderr, encoding="utf-8", errors="replace")

    print(f"[bench] {cid}: verifying real commands ...", flush=True)
    verdict = verify(replica, env, oracle_hashes)
    (cdir / "verify.json").write_text(json.dumps(verdict, ensure_ascii=False, indent=2), encoding="utf-8")
    metrics = parse_trace(trace)
    result = {
        "id": cid,
        "provider": case["provider"],
        "model": case["model"],
        "reasoning_effort": case.get("reasoning_effort", ""),
        "exit_code": proc.returncode,
        "worker_done": TASKS[TASK_NAME]["done_marker"] in proc.stdout,
        "wall_ms": wall_ms,
        "red_start_ok": red_ok,
        **{f"v_{k}": v for k, v in verdict.items() if k != "commands"},
        **metrics,
    }
    status = "PASS" if verdict["closed_loop_pass"] else "FAIL"
    print(
        f"[bench] {cid} {status} root={metrics['root_profile']}/{metrics['root_model']} "
        f"tools={metrics['tool_finished']}/{metrics['tool_started']} fail={metrics['tool_failed']} "
        f"dur={metrics['duration_ms'] or wall_ms}ms",
        flush=True,
    )
    return result


def write_report(results: list[dict], out_root: Path) -> None:
    lines = [
        f"# Worker Commit-Oracle Benchmark - {TASK_NAME}",
        "",
        f"Run dir: `{out_root}`  ·  binary: `{BENCH_BIN}`",
        f"Base: `{BASE_COMMIT}`  ·  target/oracle: `{TARGET_COMMIT}`",
        "",
        "| case | provider/model | effort | red start | closed-loop | worker done | root profile/model | first token | duration | reasoning tok | turns | tools(f/s,fail) |",
        "|---|---|---|:--:|:--:|:--:|---|--:|--:|--:|--:|--:|",
    ]
    for r in sorted(results, key=lambda x: x["id"]):
        first = f"{r.get('first_event_latency_ms')}ms" if r.get("first_event_latency_ms") else "-"
        lines.append(
            f"| `{r['id']}` | `{r['provider']}/{r['model']}` | {r.get('reasoning_effort') or '-'} | "
            f"{'yes' if r.get('red_start_ok') else 'no'} | "
            f"{'PASS' if r.get('v_closed_loop_pass') else 'FAIL'} | "
            f"{'yes' if r.get('worker_done') else 'no'} | `{r.get('root_profile') or '?'}/{r.get('root_model') or '?'}` | "
            f"{first} | {r.get('duration_ms') or r.get('wall_ms')}ms | "
            f"{r.get('reasoning_tokens', 0)} | {r.get('llm_requests', 0)} | "
            f"{r.get('tool_finished', 0)}/{r.get('tool_started', 0)},{r.get('tool_failed', 0)} |"
        )
    lines += [
        "",
        "Closed-loop PASS means oracle tests stayed byte-identical, no skip/build-tag cheat was added, all real verification commands passed, and the implementation artifact exists.",
        "Per-case artifacts: `replica/`, `trace.jsonl`, `verify.json`, `stdout.txt`, `stderr.txt`.",
        "",
    ]
    (out_root / "REPORT.md").write_text("\n".join(lines), encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--task", choices=sorted(TASKS), default="context_compaction")
    parser.add_argument("--cases", type=Path, default=None)
    parser.add_argument("--only", action="append", default=[])
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--jobs", type=int, default=1, help="parallel case workers")
    parser.add_argument("--keep-replica", action="store_true")
    args = parser.parse_args()
    configure_task(args.task)

    if not Path(BENCH_BIN).exists():
        print(f"missing crush bench binary: {BENCH_BIN}", file=sys.stderr)
        return 2
    if not USER_CONFIG.exists():
        print(f"missing real Crush config: {USER_CONFIG}", file=sys.stderr)
        return 2

    run_id = os.environ.get("BENCH_RUN_ID") or dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    out_root = STATE_ROOT / run_id
    out_root.mkdir(parents=True, exist_ok=True)
    cases = load_cases(args.cases or DEFAULT_CASES)
    if args.only:
        allow = set(args.only)
        cases = [case for case in cases if case["id"] in allow]

    results = []
    results_path = out_root / "results.jsonl"

    def finish_result(result: dict) -> None:
        results.append(result)
        with results_path.open("a", encoding="utf-8") as f:
            f.write(json.dumps(result, ensure_ascii=False) + "\n")
        if not args.keep_replica:
            shutil.rmtree(out_root / result["id"] / "replica", ignore_errors=True)
        write_report(results, out_root)

    if args.jobs <= 1 or len(cases) <= 1:
        for case in cases:
            finish_result(run_case(case, out_root, args.timeout))
    else:
        template = out_root / "_base_replica"
        print(f"[bench] preparing shared base replica {template}", flush=True)
        oracle_hashes = make_replica(template)
        print(f"[bench] running {len(cases)} cases with jobs={args.jobs}", flush=True)
        with ThreadPoolExecutor(max_workers=args.jobs) as pool:
            futures = {pool.submit(run_case, case, out_root, args.timeout, template, oracle_hashes): case for case in cases}
            for fut in as_completed(futures):
                case = futures[fut]
                try:
                    finish_result(fut.result())
                except Exception as exc:  # noqa: BLE001
                    result = {
                        "id": case["id"],
                        "provider": case["provider"],
                        "model": case["model"],
                        "reasoning_effort": case.get("reasoning_effort", ""),
                        "exit_code": -1,
                        "worker_done": False,
                        "wall_ms": 0,
                        "red_start_ok": False,
                        "v_closed_loop_pass": False,
                        "root_profile": "",
                        "root_model": "",
                        "first_event_latency_ms": 0,
                        "duration_ms": 0,
                        "reasoning_tokens": 0,
                        "llm_requests": 0,
                        "tool_finished": 0,
                        "tool_started": 0,
                        "tool_failed": 0,
                        "error": f"runner exception: {exc}",
                    }
                    (out_root / case["id"]).mkdir(parents=True, exist_ok=True)
                    (out_root / case["id"] / "runner_error.txt").write_text(str(exc), encoding="utf-8")
                    finish_result(result)

    print(f"[bench] report={out_root / 'REPORT.md'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
