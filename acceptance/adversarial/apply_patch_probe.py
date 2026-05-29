#!/usr/bin/env python3
"""Empirical probe: can crush's brain model (gemini-3-flash via antigravity)
reliably PRODUCE Codex-style apply_patch envelopes? This is the precondition
for adopting apply_patch as crush's edit protocol — if the flash model can't
write the format, the architecture won't help.

For each tab-indented base case (the class old_string editing tends to miss),
it asks the model (via `crush run`, pure-text output) for ONLY a *** Begin
Patch ... *** End Patch envelope, then parses + applies it with a reference
applier (exact context match, then rstrip-trailing-ws fuzzy, mimicking Codex's
seek_sequence) and checks the result equals the expected file.

Usage: apply_patch_probe.py [reps]
"""
import re
import subprocess
import sys
import tempfile

PREAMBLE = """You produce edits as an apply_patch envelope. Grammar:
*** Begin Patch
*** Update File: <path>
@@ [optional header]
 context line (leading space = unchanged)
-removed line
+added line
*** End Patch

Rules: show a few unchanged CONTEXT lines (leading single space) around each
change so the hunk locates uniquely. Removed lines start with '-', added with
'+'. PRESERVE EXACT INDENTATION (tabs vs spaces) of every line. Output ONLY the
envelope — no prose, no tools, no code fences."""

# Each case: tab-indented Go. expected = file after the change.
CASES = [
    {
        "name": "two_line_change_tabs",
        "path": "calc.go",
        "content": "package demo\n\nfunc Compute() int {\n\ttotal := 0\n\tfor i := 1; i < 10; i++ {\n\t\ttotal += i\n\t}\n\treturn total\n}\n",
        "task": "Make Compute multiply instead of add: initialize total to 1 (not 0) and change `total += i` to `total *= i`.",
        "expected": "package demo\n\nfunc Compute() int {\n\ttotal := 1\n\tfor i := 1; i < 10; i++ {\n\t\ttotal *= i\n\t}\n\treturn total\n}\n",
    },
    {
        "name": "insert_guard_tabs",
        "path": "h.go",
        "content": "package demo\n\nfunc Head(s []int) int {\n\treturn s[0]\n}\n",
        "task": "Add a guard at the start of Head: if len(s) == 0 { return -1 } before the existing return.",
        "expected": "package demo\n\nfunc Head(s []int) int {\n\tif len(s) == 0 {\n\t\treturn -1\n\t}\n\treturn s[0]\n}\n",
    },
    {
        "name": "rename_local_tabs",
        "path": "r.go",
        "content": "package demo\n\nfunc Sum(xs []int) int {\n\tacc := 0\n\tfor _, v := range xs {\n\t\tacc += v\n\t}\n\treturn acc\n}\n",
        "task": "Rename the local variable `acc` to `total` everywhere in Sum.",
        "expected": "package demo\n\nfunc Sum(xs []int) int {\n\ttotal := 0\n\tfor _, v := range xs {\n\t\ttotal += v\n\t}\n\treturn total\n}\n",
    },
]


def extract_patch(text):
    m = re.search(r"\*\*\* Begin Patch.*?\*\*\* End Patch", text, re.DOTALL)
    return m.group(0) if m else None


def apply_patch(content, patch):
    """Reference applier: Update-File hunks only. Locate the (context+removed)
    block in content (exact, then per-line rstrip fuzzy) and swap for
    (context+added). Returns (new_content, error)."""
    lines = patch.split("\n")
    # find the single Update File section's hunks
    try:
        ui = next(i for i, l in enumerate(lines) if l.startswith("*** Update File:"))
    except StopIteration:
        return None, "no Update File header"
    body = []
    for l in lines[ui + 1:]:
        if l.startswith("*** End Patch") or l.startswith("*** "):
            break
        body.append(l)
    # split into hunks on @@
    hunks, cur = [], None
    for l in body:
        if l.startswith("@@"):
            if cur is not None:
                hunks.append(cur)
            cur = []
        elif cur is not None:
            cur.append(l)
        elif l.strip() == "":
            continue
        else:
            # lines before first @@ (some models omit @@) — treat as one hunk
            if cur is None:
                cur = []
            cur.append(l)
    if cur:
        hunks.append(cur)
    if not hunks:
        return None, "no hunks"

    text = content
    for h in hunks:
        search, replace = [], []
        for l in h:
            if not l:
                continue
            tag, rest = l[0], l[1:]
            if tag == " ":
                search.append(rest)
                replace.append(rest)
            elif tag == "-":
                search.append(rest)
            elif tag == "+":
                replace.append(rest)
            else:
                # tolerate a stray line (model glitch) as context
                search.append(l)
                replace.append(l)
        sblock = "\n".join(search)
        rblock = "\n".join(replace)
        if sblock in text:
            text = text.replace(sblock, rblock, 1)
            continue
        # rstrip-trailing-ws fuzzy (Codex seek_sequence tier 2)
        def rstrip_block(b):
            return "\n".join(x.rstrip() for x in b.split("\n"))
        tnorm = rstrip_block(text)
        snorm = rstrip_block(sblock)
        idx = tnorm.find(snorm)
        if idx < 0:
            return None, "hunk context not found:\n" + sblock[:120]
        # map back: rebuild by line index
        tlines = text.split("\n")
        nlines = snorm.count("\n") + 1
        pre = tnorm[:idx].count("\n")
        text = "\n".join(tlines[:pre] + rblock.split("\n") + tlines[pre + nlines:])
    return text, None


def run_model(prompt):
    work = tempfile.mkdtemp(prefix="appprobe-")
    try:
        p = subprocess.run(
            ["crush-dev", "run", prompt],
            cwd=work, capture_output=True, text=True, timeout=180,
        )
        return p.stdout
    except subprocess.TimeoutExpired:
        return ""
    finally:
        subprocess.run(["rm", "-rf", work])


def main():
    reps = int(sys.argv[1]) if len(sys.argv) > 1 else 2
    total = ok_parse = ok_apply = ok_correct = 0
    for c in CASES:
        for r in range(reps):
            total += 1
            prompt = f"{PREAMBLE}\n\nFile `{c['path']}`:\n{c['content']}\nTask: {c['task']}"
            out = run_model(prompt)
            patch = extract_patch(out)
            if not patch:
                print(f"[{c['name']}#{r}] NO PATCH in output")
                continue
            ok_parse += 1
            result, err = apply_patch(c["content"], patch)
            if err:
                print(f"[{c['name']}#{r}] APPLY FAIL: {err}")
                continue
            ok_apply += 1
            if result == c["expected"]:
                ok_correct += 1
                print(f"[{c['name']}#{r}] OK")
            else:
                print(f"[{c['name']}#{r}] APPLIED BUT WRONG RESULT")
    print(f"\n=== apply_patch probe ({total} trials) ===")
    print(f"valid envelope:  {ok_parse}/{total}")
    print(f"applies cleanly: {ok_apply}/{total}")
    print(f"correct result:  {ok_correct}/{total}")


if __name__ == "__main__":
    main()
