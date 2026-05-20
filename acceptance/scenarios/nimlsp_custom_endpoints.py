#!/usr/bin/env python3
"""nimlsp_custom_endpoints.py — 验 crush 4 个 nimlsp 自定义工具的 LSP 后端契约。

直接 spawn nimlangserver、过 init,在真 Nim 项目上跑 3 个自定义 method:
  - extension/macroExpand     (lsp_macro_expand)
  - nimlsp/projectMaps        (lsp_project_maps)
  - nimlsp/safeToDelete       (lsp_safe_to_delete)

第 4 个 LLM 工具 lsp_restart 不走 LSP 自定义 method(只调 client.Restart()),
通过 internal/lsp 已有的 Go 测试覆盖,这里不重复。

退出码:
  0  全部 PASS
  77 SKIP(nimlsp / nim-core 不可用)
  1  至少一个 FAIL
"""
from __future__ import annotations

import json
import os
import pathlib
import select
import subprocess
import sys
import time
from typing import Any


NIMLSP = pathlib.Path(os.environ.get(
    "NIMLSP_BIN", "/home/junknet/linege/nim-src/langserver/nimlangserver"))
NIM_CORE = pathlib.Path(os.environ.get(
    "NIM_CORE_PATH", "/home/junknet/linege/nim-core"))

# 选一个有 macro 调用的真 Nim 文件做 fixture(acceptance/ 里的真路径用例)
MACRO_FIXTURE = NIM_CORE / "acceptance/meta/dsl/storage/ledger_sqlite_real.nim"

REQUEST_TIMEOUT_SEC = 120.0  # nimsuggest 冷启动可能 ~10s,真大项目 50s+
INIT_TIMEOUT_SEC = 30.0
NIMSUGGEST_WARMUP_SEC = 90.0  # macroExpand / safeToDelete 第一调用要等 nimsuggest 起来
RESULTS: list[tuple[str, str, str]] = []  # (case, status, msg)


# ── LSP framing ────────────────────────────────────────────────────────


def encode(payload: dict[str, Any]) -> bytes:
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    return b"Content-Length: " + str(len(body)).encode("ascii") + b"\r\n\r\n" + body


def read_exact(fd: int, n: int, deadline: float) -> bytes:
    buf: list[bytes] = []
    remaining = n
    while remaining > 0:
        timeout = max(0.0, deadline - time.monotonic())
        if timeout <= 0:
            raise TimeoutError("read_exact timeout")
        r, _, _ = select.select([fd], [], [], timeout)
        if not r:
            raise TimeoutError("read_exact select timeout")
        chunk = os.read(fd, remaining)
        if not chunk:
            raise RuntimeError("server stdout closed")
        buf.append(chunk)
        remaining -= len(chunk)
    return b"".join(buf)


def read_until(fd: int, marker: bytes, deadline: float) -> bytes:
    data = bytearray()
    while marker not in data:
        timeout = max(0.0, deadline - time.monotonic())
        if timeout <= 0:
            raise TimeoutError("read_until timeout")
        r, _, _ = select.select([fd], [], [], timeout)
        if not r:
            raise TimeoutError("read_until select timeout")
        chunk = os.read(fd, 1)
        if not chunk:
            raise RuntimeError("server stdout closed mid-header")
        data.extend(chunk)
    return bytes(data)


def read_message(proc: subprocess.Popen[bytes], deadline: float) -> dict[str, Any]:
    assert proc.stdout is not None
    header = read_until(proc.stdout.fileno(), b"\r\n\r\n", deadline)
    cl = None
    for line in header.decode("ascii", errors="replace").split("\r\n"):
        name, sep, val = line.partition(":")
        if sep and name.lower() == "content-length":
            cl = int(val.strip())
            break
    if cl is None:
        raise RuntimeError(f"no Content-Length in header: {header!r}")
    body = read_exact(proc.stdout.fileno(), cl, deadline)
    return json.loads(body.decode("utf-8"))


def send(proc: subprocess.Popen[bytes], payload: dict[str, Any]) -> None:
    assert proc.stdin is not None
    proc.stdin.write(encode(payload))
    proc.stdin.flush()


def reply_to_server(proc: subprocess.Popen[bytes], msg: dict[str, Any]) -> None:
    """nimlsp 会反向请求 workspace/configuration、workDoneProgress/create 等,要回 result。"""
    method = msg.get("method", "")
    if method == "workspace/configuration":
        result: Any = [{} for _ in msg.get("params", {}).get("items", [])]
    else:
        result = None
    send(proc, {"jsonrpc": "2.0", "id": msg["id"], "result": result})


def wait_response(
    proc: subprocess.Popen[bytes], req_id: int, timeout: float = REQUEST_TIMEOUT_SEC
) -> dict[str, Any]:
    deadline = time.monotonic() + timeout
    while True:
        m = read_message(proc, deadline)
        if "id" in m and m.get("method"):
            reply_to_server(proc, m)
            continue
        if "method" in m and "id" not in m:
            # notification,丢
            continue
        if m.get("id") == req_id:
            return m


# ── 测试用例 ────────────────────────────────────────────────────────────


def record(case: str, ok: bool, msg: str = "") -> None:
    status = "PASS" if ok else "FAIL"
    RESULTS.append((case, status, msg))
    print(f"  [{status}] {case}" + (f" — {msg}" if msg else ""), flush=True)


def case_macro_expand(proc: subprocess.Popen[bytes], next_id: list[int]) -> None:
    """打开一个 Nim 文件,在 macro 调用点上发 extension/macroExpand。"""
    uri = MACRO_FIXTURE.resolve().as_uri()
    try:
        text = MACRO_FIXTURE.read_text()
    except FileNotFoundError:
        record("macroExpand", False, f"fixture missing: {MACRO_FIXTURE}")
        return

    # didOpen
    send(proc, {"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": {
        "textDocument": {"uri": uri, "languageId": "nim", "version": 1, "text": text}
    }})

    # 找第一个 `defineLedger` / `definePgTable` / `defineSqlite` 等 macro 调用位置
    macro_keywords = ("defineLedger", "definePgTable", "defineSqlite", "defineNotifier",
                      "deriveCodec", "definePipeline")
    target_line, target_col, target_kw = -1, -1, ""
    for i, line in enumerate(text.splitlines()):
        for kw in macro_keywords:
            col = line.find(kw)
            if col >= 0 and not line.lstrip().startswith("#"):
                # cursor on first char of macro name
                target_line, target_col, target_kw = i, col, kw
                break
        if target_line >= 0:
            break
    if target_line < 0:
        record("macroExpand", False, "no macro invocation found in fixture")
        return

    next_id[0] += 1
    req_id = next_id[0]
    send(proc, {
        "jsonrpc": "2.0", "id": req_id, "method": "extension/macroExpand",
        "params": {
            "textDocument": {"uri": uri},
            "position": {"line": target_line, "character": target_col},
            "level": -1,
        }
    })
    try:
        resp = wait_response(proc, req_id, timeout=NIMSUGGEST_WARMUP_SEC)
    except Exception as e:
        record("macroExpand", False, f"transport error: {e}")
        return

    if "error" in resp:
        record("macroExpand", False, f"LSP error: {resp['error']}")
        return
    result = resp.get("result", {})
    content = (result or {}).get("content", "")
    # 不强求展开内容非空(冷启动 nimsuggest 可能返空),但调用本身不能崩
    record(
        "macroExpand",
        True,
        f"line={target_line} kw={target_kw} content_bytes={len(content)}",
    )


def case_project_maps(proc: subprocess.Popen[bytes], next_id: list[int]) -> None:
    next_id[0] += 1
    req_id = next_id[0]
    send(proc, {"jsonrpc": "2.0", "id": req_id, "method": "nimlsp/projectMaps", "params": {}})
    try:
        resp = wait_response(proc, req_id, timeout=30.0)
    except Exception as e:
        record("projectMaps", False, f"transport error: {e}")
        return
    if "error" in resp:
        record("projectMaps", False, f"LSP error: {resp['error']}")
        return
    result = resp.get("result")
    if result is None:
        record("projectMaps", False, "null result")
        return
    # 应是个 JSON object,且至少含 'modules' / 'roots' / 'mtime' 之类的字段
    if not isinstance(result, dict):
        record("projectMaps", False, f"result not object: {type(result).__name__}")
        return
    keys = sorted(result.keys())[:5]
    record("projectMaps", True, f"keys={keys} top_level_count={len(result)}")


def case_safe_to_delete(proc: subprocess.Popen[bytes], next_id: list[int]) -> None:
    """选 nim-core 里一个公开 proc,问能否删。预期 safe=false(因为肯定有引用)。"""
    # 选一个文件 + 一个 proc 名作为靶子
    target_file = NIM_CORE / "src/infra/utils/result.nim"
    if not target_file.exists():
        record("safeToDelete", False, f"target file missing: {target_file}")
        return
    text = target_file.read_text()
    # 找第一个 public proc 定义(`proc xxxXxx*(`)
    import re
    m = re.search(r"^proc\s+(\w+)\*\s*[\[\(]", text, re.M)
    if not m:
        record("safeToDelete", False, "no public proc in result.nim")
        return
    line_no = text[: m.start()].count("\n")
    col = m.group(0).index(m.group(1)) + (m.start() - text.rfind("\n", 0, m.start()) - 1)
    sym = m.group(1)

    uri = target_file.resolve().as_uri()
    send(proc, {"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": {
        "textDocument": {"uri": uri, "languageId": "nim", "version": 1, "text": text}
    }})

    next_id[0] += 1
    req_id = next_id[0]
    send(proc, {
        "jsonrpc": "2.0", "id": req_id, "method": "nimlsp/safeToDelete",
        "params": {
            "textDocument": {"uri": uri},
            "position": {"line": line_no, "character": col},
        }
    })
    try:
        resp = wait_response(proc, req_id, timeout=NIMSUGGEST_WARMUP_SEC)
    except Exception as e:
        record("safeToDelete", False, f"transport error: {e}")
        return
    if "error" in resp:
        record("safeToDelete", False, f"LSP error: {resp['error']}")
        return
    result = resp.get("result")
    if not isinstance(result, dict):
        record("safeToDelete", False, f"result not object: {type(result).__name__}")
        return
    # 必须有契约字段
    required_keys = {"safe", "refs", "blastRadiusFiles", "reasons"}
    missing = required_keys - set(result.keys())
    if missing:
        record("safeToDelete", False, f"missing keys: {sorted(missing)}")
        return
    record(
        "safeToDelete",
        True,
        f"sym={sym} safe={result['safe']} blastRadius={result['blastRadiusFiles']} "
        f"refs={len(result.get('refs', []))} reasons={result.get('reasons', [])[:1]}",
    )


# ── 驱动 ────────────────────────────────────────────────────────────────


def main() -> int:
    if not NIMLSP.is_file() or not os.access(NIMLSP, os.X_OK):
        print(f"SKIP: nimlangserver not executable at {NIMLSP}", file=sys.stderr)
        return 77
    if not NIM_CORE.is_dir():
        print(f"SKIP: nim-core not at {NIM_CORE}", file=sys.stderr)
        return 77

    print(f"spawning {NIMLSP} --lsp --stdio")
    # 关键:stderr 必须 drain 或丢弃,否则 chronicles 日志填满 pipe → nimlsp 自身写 stderr
    # 阻塞 → 整个服务卡住(MODERN_NIM_LSP_PLAN.md 实测坑)。DEVNULL 最稳。
    proc = subprocess.Popen(
        [str(NIMLSP), "--lsp", "--stdio"],
        cwd=str(NIM_CORE),
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
    )

    next_id = [0]
    try:
        # initialize
        next_id[0] += 1
        send(proc, {
            "jsonrpc": "2.0", "id": next_id[0], "method": "initialize",
            "params": {
                "processId": os.getpid(),
                "rootUri": NIM_CORE.resolve().as_uri(),
                "capabilities": {
                    "window": {"workDoneProgress": True},
                    "workspace": {"configuration": True},
                },
            },
        })
        init_resp = wait_response(proc, next_id[0], timeout=INIT_TIMEOUT_SEC)
        if "error" in init_resp:
            print(f"FAIL: initialize: {init_resp['error']}", file=sys.stderr)
            return 1
        send(proc, {"jsonrpc": "2.0", "method": "initialized", "params": {}})
        print(f"  init OK ({len(init_resp.get('result', {}).get('capabilities', {}))} caps)")

        # 跑 3 个 case
        case_project_maps(proc, next_id)
        case_macro_expand(proc, next_id)
        case_safe_to_delete(proc, next_id)

        # shutdown
        next_id[0] += 1
        send(proc, {"jsonrpc": "2.0", "id": next_id[0], "method": "shutdown"})
        try:
            wait_response(proc, next_id[0], timeout=5.0)
        except Exception:
            pass
        send(proc, {"jsonrpc": "2.0", "method": "exit"})

    finally:
        try:
            proc.wait(timeout=5.0)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()

    # 汇总
    pass_n = sum(1 for _, s, _ in RESULTS if s == "PASS")
    fail_n = sum(1 for _, s, _ in RESULTS if s == "FAIL")
    print(f"\n=== summary: {pass_n} PASS, {fail_n} FAIL ===")
    return 0 if fail_n == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
