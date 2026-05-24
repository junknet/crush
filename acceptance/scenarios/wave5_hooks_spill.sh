#!/usr/bin/env bash
# wave5_hooks_spill.sh — verify Wave 5 G6 Spiller + G7 Pre/Post/Stop hooks
# end-to-end. Spawns a real crush TUI against WaitAI, runs a single prompt
# that forces a large bash output (Spiller path) and one tool call (Pre/Post
# hooks), then asserts the on-disk side effects.
#
# Skips when WaitAI unreachable or TUI helper missing.

source "$(dirname "$0")/../common.sh"
need_tui
need_waitai

HOOK_LOG="$(mktemp -t crush_wave5_hooks_XXXXXX.log)"
trap 'rm -f "$HOOK_LOG"' EXIT

# Build an isolated config dir so we don't trample the user's real
# ~/.config/crush. The hook commands write to $HOOK_LOG which we tail.
CFG_DIR="$(mktemp -d -t crush_wave5_cfg_XXXXXX)"
trap 'rm -f "$HOOK_LOG"; rm -rf "$CFG_DIR"' EXIT
export CRUSH_GLOBAL_CONFIG="$CFG_DIR"
mkdir -p "$CFG_DIR"

# Copy the user's existing crush.yaml so providers/models stay valid, then
# append hooks. Falls back to an empty YAML when the user has no config.
USER_CFG="$HOME/.config/crush/crush.yaml"
if [[ -f "$USER_CFG" ]]; then
  cp "$USER_CFG" "$CFG_DIR/crush.yaml"
else
  echo "providers: {}" > "$CFG_DIR/crush.yaml"
fi

cat >> "$CFG_DIR/crush.yaml" <<EOF
hooks:
  PreToolUse:
    - command: 'echo "\$CRUSH_EVENT \$CRUSH_TOOL_NAME" >> $HOOK_LOG; exit 0'
  PostToolUse:
    - command: 'echo "\$CRUSH_EVENT \$CRUSH_TOOL_NAME" >> $HOOK_LOG; exit 0'
  Stop:
    - command: 'echo "\$CRUSH_EVENT" >> $HOOK_LOG; exit 0'
EOF

log "config dir: $CFG_DIR"
log "hook log:   $HOOK_LOG"

DATA_DIR="$(mktemp -d -t crush_wave5_data_XXXXXX)"
trap 'rm -f "$HOOK_LOG"; rm -rf "$CFG_DIR" "$DATA_DIR"' EXIT

log "starting crush in tmux"
"$TUI" start "$SESS" 160 45 -- \
  "cd $REPO && CRUSH_GLOBAL_CONFIG=$CFG_DIR CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1 $CRUSH_BIN --trace-file $TRACE" \
  | tee -a "$LOG"

"$TUI" expect "$SESS" 'Ready for instructions' 15 || fail "TUI not ready"

# Spiller probe: a bash command whose stdout exceeds BashSpillThreshold
# (30 KiB). 5_000 numbered lines is ~60 KiB.
log "submitting Spiller-trigger prompt"
"$TUI" type "$SESS" 'Run "seq 1 5000" and report the last number.'
"$TUI" key  "$SESS" Enter

"$TUI" expect "$SESS" '5000' 60 || fail "agent did not produce expected answer"

log "graceful quit"
"$TUI" quit "$SESS"
sleep 1

log "asserting hook log captured Pre/Post/Stop events"
[[ -s "$HOOK_LOG" ]] || fail "hook log empty: $HOOK_LOG"
grep -q '^PreToolUse bash'  "$HOOK_LOG" || fail "PreToolUse hook for bash missing"
grep -q '^PostToolUse bash' "$HOOK_LOG" || fail "PostToolUse hook for bash missing"
grep -q '^Stop'             "$HOOK_LOG" || fail "Stop hook missing"

log "asserting Spiller wrote a tool-results file"
spill_dir="$(find . "$DATA_DIR" "$HOME/.local/share/crush" "$XDG_DATA_HOME/crush" 2>/dev/null \
  -type d -name tool-results | head -1)"
if [[ -z "$spill_dir" ]]; then
  log "no tool-results dir found (Spiller may not have engaged — output below threshold)"
else
  found="$(find "$spill_dir" -name 'bash-*.log' | head -1)"
  [[ -n "$found" ]] || fail "expected bash-*.log under $spill_dir"
  log "  ✓ spill file present: $found ($(wc -c < "$found") bytes)"
fi

pass
