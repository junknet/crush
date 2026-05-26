# UI/UX Preferences

## Core TUI Behavior (May 2026)

### Scroll & Follow Mode
- **Sticky-bottom autoscroll**: Passive content (LLM streaming, `AppendMessages`) is "follow-aware" — won't scroll if user scrolled up (`follow=false`).
- **Explicit user actions**: Submitting prompt or pressing End use `ForceScrollToBottom` to re-enable follow + jump to end.
- **Sub-agent streaming**: `AppendMessages` now calls `ScrollToBottom()` directly so tool calls/chunks stream in view even if user scrolled to history.
- **Relay & external prompts**: `RelayPromptMsg` uses `ForceScrollToBottom` to reset follow mode.

### Input Composer & History
- **ESC cancellation**: When user ESC-cancels agent, textarea content is snapshot into `promptHistory.draft` (and `index = -1`) so `↑/↓` navigation doesn't erase unsent text.
- **Keyboard submit**: `ForceScrollToBottom` so agent's response streams in view regardless of scroll position.

### Plan Mode Closed Loop (May 2026)
- **Slash commands**: `/accept`, `/run` (both trigger plan→execute + auto-send "Implement the plan..."), `/cancel-plan`, `/exit-plan` (exit without executing).
- **Auto-prefill**: Plan agent's EndTurn fires toast + auto-fills `/accept` in textarea (if empty) so user only presses Enter.
- **Toast styling**: `InfoTypeSuccess` (green) for plan ready, 15s TTL.
- **Model switch UI**: Plan tab removed from Switch Model dialog; plan follows brain model. Auditor role added (Brain · Worker · Auditor · Explore).
- **Active model highlight**: Currently-selected model in Switch Model shows green ✓ mark in info slot.

### To-Do Panel
- Expanded by default (state `ctrl+t close`). 
- ANSI escape rendering fixed (`toolParamList` detects SGR, avoids double-render).

### Focus Management
- Mouse wheel scrolling no longer steals focus from input field.
- `handleClickFocus` in `internal/ui/model/ui.go` manages predictable focus transitions.

### Attachment UI (Deferred)
- **Current**: Chip-based rendering (horizontal line above textarea showing `[image]`, `[paste: Nkb]`, filenames).
- **free-code analogue**: Chips directly inline in textarea as tokens (`[Image #1]`), with cursor able to traverse them.
- **Status**: Chip layout works; visual alignment could improve (chip line should feel part of input frame, not floating above).
- **Deferred improvements**: Inline token insertion (requires paste handler rewrite + token parser on submit). Tracked as separate refactor.

## Mobile UI/UX Preferences (May 2026)

### Rendering & Typography
- **Markdown Tables**: Must be rendered as grids with horizontal scroll support (handled by `<MarkdownTable />`). Raw text fallback is considered "garbage".
- **Typography**: Preferred `lineHeight` of 22 for prose. Paragraphs should have clear vertical spacing.
- **Interactivity**: All message content, including code and terminal output, must be `selectable={true}` for easy copy-paste.
- **Bubbles**: Modern shapes with larger, smoother corners (`borderRadius: 22`). Distinct "tail" corners for user (bottom-right) vs assistant (bottom-left) for directionality.

### Performance & Flow
- **Session Switching**: Loading must be near-instant. Initial history capped to 1 hour; older data fetched on-demand.
- **Scroll Position**: First render of a session must stick to the bottom to show the latest context.
- **Feedback**: "正在思考..." (Thinking) and "运行中..." (Running) indicators must be clear and animated (spinner/cursor).
