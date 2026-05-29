#!/usr/bin/env python3
"""A/B: old_string edit/multiedit (OLD binary /tmp/crush-old) vs apply_patch
(NEW binary /tmp/crush-new), SAME tab-indented edit tasks, SAME flash model.
Measures the edit optimization with evidence: final correctness (ground truth),
first-try-clean rate (no failed edit attempt), and total edit attempts/fails.

Cases reproduce the real failure mode behind the corpus' ~20% edit tax —
ambiguity (near-identical blocks), deep nesting, context pollution — not
trivial isolated edits.
"""
import json
import os
import subprocess
import tempfile

OLD, NEW = "/tmp/crush-old", "/tmp/crush-new"
EDIT_TOOLS = {"edit", "edit_tool", "multiedit", "multiedit_tool", "apply_patch", "apply_patch_tool"}


def big_funcs(transform_idx):
    out = "package d\n\n"
    for i in range(12):
        body = "\treturn x * 2\n" if (transform_idx is not None and i == transform_idx) else "\treturn x\n"
        out += "func F%d() int {\n\tx := %d\n%s}\n\n" % (i, i, body)
    return out


CASES = [
    ("ambiguous_big", "svc.go", big_funcs(None),
     "把 F7 函数里的 `return x` 改成 `return x * 2`，只改 F7，别动其他 F 函数。",
     big_funcs(7)),
    ("deep_nest", "loop.go",
     'package d\n\nfunc R() {\n\tfor a := 0; a < 2; a++ {\n\t\tfor b := 0; b < 2; b++ {\n\t\t\tfor c := 0; c < 2; c++ {\n\t\t\t\tval := a + b + c\n\t\t\t\tprintln(val)\n\t\t\t}\n\t\t}\n\t}\n}\n',
     '把最内层 `println(val)` 改成 `println(val * 10)`。',
     'package d\n\nfunc R() {\n\tfor a := 0; a < 2; a++ {\n\t\tfor b := 0; b < 2; b++ {\n\t\t\tfor c := 0; c < 2; c++ {\n\t\t\t\tval := a + b + c\n\t\t\t\tprintln(val * 10)\n\t\t\t}\n\t\t}\n\t}\n}\n'),
    ("twin_blocks", "twin.go",
     'package d\n\nfunc A() {\n\tcfg := load()\n\tcfg.timeout = 30\n\trun(cfg)\n}\n\nfunc B() {\n\tcfg := load()\n\tcfg.timeout = 30\n\trun(cfg)\n}\n',
     '只把函数 B 里的 `cfg.timeout = 30` 改成 `cfg.timeout = 60`，A 保持不变。',
     'package d\n\nfunc A() {\n\tcfg := load()\n\tcfg.timeout = 30\n\trun(cfg)\n}\n\nfunc B() {\n\tcfg := load()\n\tcfg.timeout = 60\n\trun(cfg)\n}\n'),
    ("ctx_polluted", "target.go",
     'package d\n\nfunc Target() string {\n\treturn "before"\n}\n',
     '先用 view 完整读同目录的 noise1.go、noise2.go、noise3.go 理解全局，然后把 target.go 里 Target 的返回值改成 "after"。',
     'package d\n\nfunc Target() string {\n\treturn "after"\n}\n'),
]


def run(binary, task, content, path):
    work = tempfile.mkdtemp(prefix="abedit-")
    open(os.path.join(work, path), "w").write(content)
    if path == "target.go":
        for k in range(1, 4):
            noise = "package d\n\n" + "".join(
                "func Noise%d_%d() int { return %d }\n" % (k, j, j) for j in range(120)
            )
            open(os.path.join(work, "noise%d.go" % k), "w").write(noise)
    subprocess.run(["git", "init", "-q"], cwd=work)
    trace = os.path.join(work, "t.jsonl")
    subprocess.run(
        [binary, "--trace-file", trace, "--data-dir", os.path.join(work, ".d"),
         "run", "%s（文件在当前目录）" % task],
        cwd=work, capture_output=True, text=True, timeout=240,
    )
    final = ""
    fp = os.path.join(work, path)
    if os.path.exists(fp):
        final = open(fp).read()
    attempts = fails = 0
    if os.path.exists(trace):
        for line in open(trace):
            try:
                e = json.loads(line)
            except Exception:
                continue
            if e.get("tool_name", "") in EDIT_TOOLS:
                if e.get("kind") == "tool_started":
                    attempts += 1
                elif e.get("kind") == "tool_failed":
                    fails += 1
    subprocess.run(["rm", "-rf", work])
    return final, attempts, fails


def main():
    print("%-14s | %-30s | %s" % ("case", "OLD correct/1st/att/fail", "NEW correct/1st/att/fail"))
    agg = {"OLD": [0, 0, 0, 0, 0], "NEW": [0, 0, 0, 0, 0]}
    for name, path, content, task, expected in CASES:
        row = {}
        for label, binary in (("OLD", OLD), ("NEW", NEW)):
            final, att, fail = run(binary, task, content, path)
            correct = final == expected
            firsttry = fail == 0 and att >= 1
            row[label] = (correct, firsttry, att, fail)
            a = agg[label]
            a[0] += int(correct); a[1] += int(firsttry); a[2] += att; a[3] += fail; a[4] += 1
        print("%-14s | %-30s | %s" % (name, str(row["OLD"]), row["NEW"]))
    print("\n=== AGGREGATE ===")
    for label in ("OLD", "NEW"):
        c, ft, att, fail, n = agg[label]
        print("%s: correct %d/%d | first-try-clean %d/%d | edit attempts %d | edit fails %d"
              % (label, c, n, ft, n, att, fail))


if __name__ == "__main__":
    main()
