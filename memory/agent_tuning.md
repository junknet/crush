# Agent Configuration Tuning (May 2026)

## Explore Agent (Model Selection & Turn Budget)

### MaxTurns Increase: 8 → 16
**Reason**: 8 turns was too tight for multi-question briefs. A typical brief has 3-4 sub-questions, each needing 2-3 reads; at 8 turns the agent would run out mid-investigation and emit a half-finished "going to look at X" line that looked like a truncated return to the parent.

**Config**: `internal/config/config.go:868`
```go
AgentExplore: {
    MaxTurns: 16,
    ParallelTools: []string{"glob", "grep", "view", "ls", "bash"},
}
```

### Prompt Constraints: NO NARRATION
**Rule addition in explore.md.tpl (rule #1)**:
```
1. NO NARRATION: Never emit interim prose like "Now let me look at X" or "查看 Y 的实现".
   Every tool call is enough on its own — the parent sees them. Speak only in the **final** message.
```

**Why**: Interim prose burns a turn and looks like truncated output to the brain agent. Tool calls are self-explanatory to the parent (which has its own session context).

### Tool Preference: rg over grep/find
- **bash.md.tpl**: Explicitly ban `grep` in bash (slow, ignores .gitignore, breaks on binary). Recommend `rg -a` for ad-hoc binary searches.
- **grep.md.tpl**: Clarify that the tool IS `rg` (ripgrep), not traditional grep. Binary-safe, .gitignore-respecting, with JSON output.
- **explore.md.tpl rules**: Use `rg` for text search (rule #6), `rg --files | rg PATTERN` or `fd` for filenames (rule #7), not `grep`/`find`.

## Plan Agent Prompt Rewrite (May 2026)

### Output Format Enforcement
**New section in plan.md.tpl (rules #8, output block)**:
- **CLOSED LOOP rule**: Crush UI auto-pre-fills `/accept` in composer the moment plan's final message ends.
- **User has exactly two next moves**: Press Enter → implement; edit to `/cancel-plan` → exit.
- **Write plan self-contained**: Brain will implement from it in the same session without re-asking.

### Output Structure (Mandatory Sections, Exact Order)
```
- **Current understanding**
- **Root cause classification**
- **Proposed approach**
- **Files to change**
- **Risks and dependencies**
- **Verification plan**
- **Open questions** (omit if none)
```
**Rationale**: Brain can parse & act directly; no prose fluff after plan ("Let me know if...").

### Integration with free-code/free-system Patterns
- Plan agent focuses on **why**, not **what-to-type**.
- Plan accepted → Brain automatically flips to execute mode + sends "Implement the plan above. Use worker sub-agents...".
- No modal dialog (unlike free-code's ExitPlanModePermissionRequest); toast + textarea pre-fill is Crush's lighter idiom.

## Build & Deployment Commands

### Build Variants
```bash
# Quick dev (auto-rebuild, dev cache):
crush-dev

# Prod binary (into ~/.cache/crush-prod/):
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -trimpath -o /tmp/crush-verify ./
cp /tmp/crush-verify ~/.cache/crush-prod/crush

# Minimal test (no VCR, short timeout):
go test ./internal/ui/... ./internal/config/... -count=1 -timeout=60s -short
```

### Pre-existing Test Timeouts
- `TestWorkerAgent` (internal/agent/coordinator_test.go) uses VCR cassettes; needs 3m+ for LLM interaction. Not a blocker; ignore in `task test:record` flow.

## Known Deferred Work

1. **Attachment chip → inline token** (free-code style): Requires paste handler + token parser. Separate refactor (medium effort).
2. **Compose + Input box styling boundary**: Chat attachment chips sit above textarea; visual integration could tighten but works as-is.
3. **Sub-agent narration detection**: Could auto-detect half-finished "Now checking..." lines and re-run explore with more turns (future optimization).

## Validator Checkpoints

After each change:
1. `go build` → succeeds silently.
2. `go test ./internal/ui/... -count=1 -timeout=60s` → no hangs.
3. `crush-dev` → new session loads all UI updates live.
4. Plan mode: Input `/test plan message` → enter plan mode → prompt arrives → toast + `/accept` pre-filled.

## Agent Iteration & Workflow Optimization (May 2026)

### Default Tool Timeout: 3s → 60s
**Reason**: The previous 3s timeout was too aggressive for real-world engineering tasks like Nim compilation or large codebase greps, causing premature agent stops. 60s provides a stable buffer.
**Location**: `internal/agent/coordinator.go` (wrapToolsWithTimeout call).

### Active To-Do Iteration Drive
**Mechanism**: 
1. **Dynamic System Reminder**: When the todo list is non-empty, a reminder is injected into every turn context, explicitly forbidding the agent from stopping or asking for permission until all tasks reach a terminal state (`completed` or `failed`).
2. **Wake-up Prompt Reinforcement**: Background job completion notifications now include instructions to address failures and finish remaining tasks immediately.
**Goal**: Force autonomous completion of multi-step plans without user intervention.

### To-Do "Failed" Status
**Logic**: Added `failed` status to `TodoStatus` in `internal/session/session.go`. Agents are now instructed to use this status for tasks that cannot be completed due to permanent errors, enabling better tracking and visualization.
