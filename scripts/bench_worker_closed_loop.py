#!/usr/bin/env python3
"""Worker-agent closed-loop E2E benchmark.

Drives `crush run --role worker` to reimplement the reverted apply_patch tool
across a model×effort matrix, in fully isolated git-stripped working-tree
replicas, and judges each run objectively (go build/vet/test must really pass,
oracle test file must be byte-unchanged, no t.Skip cheats).

This measures BOTH worker capability (does it close the loop, how fast, how many
turns/tokens) AND crush's own tool/prompt/runtime behaviour under a hard task.

Usage:
  scripts/bench_worker_closed_loop.py --only gemini_medium --timeout 600   # dry-run one cell
  scripts/bench_worker_closed_loop.py --timeout 1500                       # full 8-cell matrix
Env:
  CRUSH_BENCH_BIN  path to the crush binary built with --role support (default /tmp/crush-bench)
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
import time
from pathlib import Path

import yaml

REPO = Path(__file__).resolve().parents[1]
CASE_DIR = REPO / "bench" / "real_role_matrix"
DEFAULT_CASES = CASE_DIR / "cases_worker_apply_patch.jsonl"
PROMPT_FILE = CASE_DIR / "worker_apply_patch.md"
USER_CONFIG = Path.home() / ".config" / "crush" / "crush.yaml"
USER_STATE = Path.home() / ".config" / "crush" / "state.yaml"
BENCH_BIN = os.environ.get("CRUSH_BENCH_BIN", "/tmp/crush-bench")
STATE_ROOT = Path.home() / ".local" / "state" / "crush-worker-bench"

ORACLE_COMMIT = "b9a5cbe"
ORACLE_TEST_REL = "internal/agent/tools/apply_patch_test.go"
ORACLE_SHA = "39ba4571524d0b7e9961d3cbf426f3cb725cb50d65e2f95108c7996fb88dad4c"
IMPL_REL = "internal/agent/tools/apply_patch.go"
MD_REL = "internal/agent/tools/apply_patch.md"
# Other test files that define shared mocks — worker must not tamper with them.
GUARDED_TESTS = ["internal/agent/tools/multiedit_test.go", "internal/agent/tools/write_test.go"]
TEST_RUN_FILTER = "ApplyPatch|ParsePatch|ApplyHunks"

RSYNC_EXCLUDES = [".git", "mobile", "acceptance/artifacts",
                  "bench/real_role_matrix/artifacts", "tmp", "*.log"]


def load_cases(path: Path) -> list[dict]:
    out = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line:
            out.append(json.loads(line))
    return out


def goenv(base: dict) -> dict:
    e = dict(base)
    e["CGO_ENABLED"] = "0"
    e["GOEXPERIMENT"] = "greenteagc"
    return e


def sh(cmd: list[str], cwd: Path, env: dict, timeout: int = 360) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=str(cwd), env=env, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)


def make_replica(dst: Path) -> str:
    """rsync the working tree (minus heavy/irrelevant dirs), strip git history
    (anti-cheat: worker can't `git show` the reference impl), plant the oracle
    test as the RED starting point, and remove any apply_patch impl/md."""
    dst.mkdir(parents=True, exist_ok=True)
    ex = []
    for e in RSYNC_EXCLUDES:
        ex += ["--exclude", e]
    subprocess.run(["rsync", "-a", *ex, str(REPO) + "/", str(dst) + "/"], check=True)
    shutil.rmtree(dst / ".git", ignore_errors=True)
    oracle = subprocess.run(["git", "show", f"{ORACLE_COMMIT}:{ORACLE_TEST_REL}"],
                            cwd=str(REPO), check=True, text=True, capture_output=True).stdout
    (dst / ORACLE_TEST_REL).write_text(oracle, encoding="utf-8")
    for rel in (IMPL_REL, MD_REL):
        (dst / rel).unlink(missing_ok=True)
    return oracle


def write_state(cfg_dir: Path, case: dict) -> None:
    """Copy real crush.yaml (providers/keys) and a state.yaml that preserves the
    real model selection for all roles but overrides `worker` for this cell."""
    shutil.copy2(USER_CONFIG, cfg_dir / "crush.yaml")
    base = {}
    if USER_STATE.exists():
        base = yaml.safe_load(USER_STATE.read_text(encoding="utf-8")) or {}
    base.pop("recent_models", None)
    models = base.get("models", {}) or {}
    w = {"provider": case["provider"], "model": case["model"]}
    for k in ("reasoning_effort", "max_tokens", "think", "thinking_budget"):
        if case.get(k) is not None:
            w[k] = case[k]
    w.setdefault("max_tokens", 32000)
    models["worker"] = w
    base["models"] = models
    (cfg_dir / "state.yaml").write_text(yaml.safe_dump(base, allow_unicode=True), encoding="utf-8")


def parse_trace(trace: Path) -> dict:
    m = {"trace": str(trace), "root_model": "", "root_provider": "", "root_profile": "",
         "success": False, "duration_ms": 0, "first_event_latency_ms": 0,
         "input_tokens": 0, "output_tokens": 0, "reasoning_tokens": 0,
         "cache_creation_tokens": 0, "cache_read_tokens": 0,
         "llm_requests": 0, "tool_started": 0, "tool_finished": 0, "tool_failed": 0,
         "finish_reason": "", "error": ""}
    if not trace.exists():
        return m
    for line in trace.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        kind = ev.get("kind")
        is_root = ev.get("parent_id") == ""
        if kind == "tool_started":
            m["tool_started"] += 1
        elif kind == "tool_finished":
            m["tool_finished"] += 1
        elif kind == "tool_failed":
            m["tool_failed"] += 1
        elif kind == "llm_request_started":
            m["llm_requests"] += 1
            if is_root and not m["root_model"]:
                m["root_model"] = ev.get("model_id", "")
                m["root_provider"] = ev.get("provider_id", "")
                m["root_profile"] = ev.get("profile", "")
        elif kind == "llm_first_event" and is_root:
            m["first_event_latency_ms"] = max(m["first_event_latency_ms"],
                                              int(ev.get("first_event_latency_ms") or 0))
        elif is_root and kind in ("llm_request_finished", "task_finished", "task_failed"):
            for k in ("input_tokens", "output_tokens", "reasoning_tokens",
                      "cache_creation_tokens", "cache_read_tokens", "finish_reason"):
                v = ev.get(k)
                if v not in ("", 0, None):
                    m[k] = m[k] + v if isinstance(v, int) and k.endswith("tokens") else v
            if kind == "task_finished":
                m["success"] = bool(ev.get("success"))
                m["duration_ms"] = int(ev.get("duration_ms") or 0)
            elif kind == "task_failed":
                m["duration_ms"] = int(ev.get("duration_ms") or 0)
        if ev.get("error"):
            m["error"] = str(ev.get("error"))[:300]
    return m


def verify(replica: Path, env: dict, oracle_raw: str) -> dict:
    """Objective closed-loop verdict: oracle untouched + build/vet/test all green."""
    v = {}
    tf = replica / ORACLE_TEST_REL
    cur = tf.read_text(encoding="utf-8") if tf.exists() else ""
    v["oracle_present"] = tf.exists()
    v["oracle_sha_ok"] = (hashlib.sha256(cur.encode()).hexdigest()
                          == hashlib.sha256(oracle_raw.encode()).hexdigest())
    v["no_skip_cheat"] = not any(s in cur for s in
                                 ("t.Skip", "SkipNow", "//go:build ignore", "+build ignore"))
    v["require_count"] = cur.count("require.")
    v["guarded_tests_ok"] = all((replica / g).exists() for g in GUARDED_TESTS)
    v["impl_present"] = (replica / IMPL_REL).exists()
    try:
        b = sh(["go", "build", "./..."], replica, env, timeout=420)
        v["build_ok"] = b.returncode == 0
        v["build_tail"] = b.stdout[-600:]
    except subprocess.TimeoutExpired:
        v["build_ok"] = False
        v["build_tail"] = "TIMEOUT"
    try:
        vet = sh(["go", "vet", "./internal/agent/tools/"], replica, env, timeout=300)
        v["vet_ok"] = vet.returncode == 0
    except subprocess.TimeoutExpired:
        v["vet_ok"] = False
    try:
        t = sh(["go", "test", "./internal/agent/tools/", "-run", TEST_RUN_FILTER,
                "-count=1", "-v"], replica, env, timeout=420)
        v["test_pass"] = t.stdout.count("--- PASS")
        v["test_fail"] = t.stdout.count("--- FAIL")
        v["test_ok"] = t.returncode == 0 and v["test_fail"] == 0 and v["test_pass"] > 0
        v["test_tail"] = t.stdout[-1800:]
    except subprocess.TimeoutExpired:
        v["test_pass"] = v["test_fail"] = 0
        v["test_ok"] = False
        v["test_tail"] = "TIMEOUT"
    v["closed_loop_pass"] = all([v["oracle_sha_ok"], v["no_skip_cheat"], v["guarded_tests_ok"],
                                 v["build_ok"], v["vet_ok"], v["test_ok"]])
    return v


def run_case(case: dict, out_root: Path, timeout_s: int) -> dict:
    cid = case["id"]
    cdir = out_root / cid
    cdir.mkdir(parents=True, exist_ok=True)
    replica = cdir / "replica"
    cfg = cdir / "cfg"
    cfg.mkdir(exist_ok=True)
    print(f"[bench] {cid}: building isolated replica …", flush=True)
    oracle_raw = make_replica(replica)
    write_state(cfg, case)

    env = goenv(os.environ)
    env["CRUSH_GLOBAL_CONFIG"] = str(cfg)
    env["CRUSH_GLOBAL_DATA"] = str(cdir / "data")
    env["CRUSH_DISABLE_METRICS"] = "1"
    env["CRUSH_DISABLE_PROVIDER_AUTO_UPDATE"] = "1"
    for k in ("CRUSH_MOCK_API_KEY", "CRUSH_MOCK_KEY", "CRUSH_MOCK_BASE", "CRUSH_MOCK_LLM_BASE"):
        env.pop(k, None)

    # red-start sanity: package must not compile before the worker implements it.
    red = sh(["go", "vet", "./internal/agent/tools/"], replica, env, timeout=240)
    red_ok = red.returncode != 0 and "patchHunk" in red.stdout

    prompt = PROMPT_FILE.read_text(encoding="utf-8")
    trace = cdir / "trace.jsonl"
    print(f"[bench] {cid}: running worker ({case['provider']}/{case['model']}, "
          f"effort={case.get('reasoning_effort','-')}) timeout={timeout_s}s …", flush=True)
    started = time.time()
    try:
        proc = subprocess.run(["timeout", str(timeout_s), BENCH_BIN, "run", "--role", "worker",
                               "--quiet", "--trace-file", str(trace), prompt],
                              cwd=str(replica), env=env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        rc = proc.returncode
        (cdir / "stdout.txt").write_text(proc.stdout, encoding="utf-8", errors="replace")
        (cdir / "stderr.txt").write_text(proc.stderr, encoding="utf-8", errors="replace")
        worker_done = "WORKER_DONE" in proc.stdout
    except Exception as exc:  # noqa: BLE001
        rc = -1
        worker_done = False
        (cdir / "stderr.txt").write_text(f"runner exception: {exc}", encoding="utf-8")
    wall_ms = int((time.time() - started) * 1000)

    print(f"[bench] {cid}: verifying (go build/vet/test) …", flush=True)
    vd = verify(replica, env, oracle_raw)
    m = parse_trace(trace)

    impl = replica / IMPL_REL
    if impl.exists():
        shutil.copy2(impl, cdir / "apply_patch.produced.go")
        vd["impl_lines"] = impl.read_text(encoding="utf-8", errors="replace").count("\n") + 1
    else:
        vd["impl_lines"] = 0

    result = {
        "id": cid, "provider": case["provider"], "model": case["model"],
        "reasoning_effort": case.get("reasoning_effort", ""),
        "exit_code": rc, "worker_done": worker_done, "wall_ms": wall_ms,
        "red_start_ok": red_ok,
        **{f"v_{k}": val for k, val in vd.items() if k not in ("build_tail", "test_tail")},
        **m,
    }
    (cdir / "verify.json").write_text(json.dumps(vd, ensure_ascii=False, indent=2), encoding="utf-8")
    status = "PASS" if vd["closed_loop_pass"] else "FAIL"
    print(f"[bench] {cid} {status}  test={vd.get('test_pass',0)}P/{vd.get('test_fail',0)}F "
          f"build={vd['build_ok']} cheat_ok={vd['oracle_sha_ok'] and vd['no_skip_cheat']} "
          f"root_model={m['root_model']} dur={m['duration_ms']}ms reasoning_tok={m['reasoning_tokens']} "
          f"tools={m['tool_finished']}/{m['tool_started']} fail={m['tool_failed']}", flush=True)
    return result


def write_report(results: list[dict], out_root: Path) -> None:
    lines = [
        "# Worker Closed-Loop Benchmark — apply_patch reimplementation",
        "",
        f"Run dir: `{out_root}`  ·  binary: `{BENCH_BIN}`  ·  oracle commit: `{ORACLE_COMMIT}`",
        "",
        "| case | provider/model | effort | closed-loop | test P/F | build | cheat-safe | root model | first-tok | duration | reasoning tok | turns | tools(f/s,fail) | impl LOC |",
        "|---|---|---|:--:|--:|:--:|:--:|---|--:|--:|--:|--:|--:|--:|",
    ]
    for r in sorted(results, key=lambda x: x["id"]):
        cheat = r.get("v_oracle_sha_ok") and r.get("v_no_skip_cheat") and r.get("v_guarded_tests_ok")
        cl = "✅" if r.get("v_closed_loop_pass") else "❌"
        first = f"{r['first_event_latency_ms']}ms" if r["first_event_latency_ms"] else "-"
        dur = f"{r['duration_ms']}ms" if r["duration_ms"] else f"{r['wall_ms']}ms(wall)"
        lines.append(
            f"| `{r['id']}` | `{r['provider']}/{r['model']}` | {r['reasoning_effort'] or '-'} | {cl} | "
            f"{r.get('v_test_pass',0)}/{r.get('v_test_fail',0)} | {'✅' if r.get('v_build_ok') else '❌'} | "
            f"{'✅' if cheat else '⚠️CHEAT'} | {r['root_model'] or '?'} | {first} | {dur} | "
            f"{r['reasoning_tokens']} | {r['llm_requests']} | "
            f"{r['tool_finished']}/{r['tool_started']},{r['tool_failed']} | {r.get('v_impl_lines',0)} |")
    lines += ["",
              "- **closed-loop ✅** = oracle test byte-unchanged + no skip cheat + go build/vet/test all green.",
              "- **cheat-safe ⚠️** flags a run whose oracle test hash changed or added t.Skip — its PASS is void.",
              "- root model column proves which model actually executed as the worker root.",
              "- per-case artifacts: `replica/` (final tree), `apply_patch.produced.go`, `trace.jsonl`, `verify.json`, `stdout/stderr.txt`.",
              ""]
    (out_root / "REPORT.md").write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--cases", type=Path, default=DEFAULT_CASES)
    ap.add_argument("--only", action="append", default=[])
    ap.add_argument("--timeout", type=int, default=1500)
    ap.add_argument("--keep-replica", action="store_true", help="keep replica/ dirs (large)")
    args = ap.parse_args()

    if not Path(BENCH_BIN).exists():
        print(f"missing crush bench binary: {BENCH_BIN} (build with --role support)", file=sys.stderr)
        return 2
    if not USER_CONFIG.exists():
        print(f"missing real crush config: {USER_CONFIG}", file=sys.stderr)
        return 2

    run_id = os.environ.get("BENCH_RUN_ID") or dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    out_root = STATE_ROOT / run_id
    out_root.mkdir(parents=True, exist_ok=True)
    cases = load_cases(args.cases)
    if args.only:
        allow = set(args.only)
        cases = [c for c in cases if c["id"] in allow]

    results = []
    rp = out_root / "results.jsonl"
    for case in cases:
        r = run_case(case, out_root, args.timeout)
        results.append(r)
        with rp.open("a", encoding="utf-8") as f:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
        if not args.keep_replica:
            shutil.rmtree(out_root / case["id"] / "replica", ignore_errors=True)
        write_report(results, out_root)

    print(f"[bench] report={out_root / 'REPORT.md'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
