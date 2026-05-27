# To-Do System Optimization & Iteration Drive (May 2026)

## Core Changes (Implemented)

### 1. Extended To-Do Status
- **New Status**: failed.
- **Logic**: Tasks can now be explicitly marked as failed via the todos tool. 
- **Visualization**: 
    - Sidebar Pill shows (N failed) in red.
    - Chat message list shows a red × icon for failed items.
- **Rationale**: Previously, failed tasks were indistinguishable from in_progress or pending tasks, leading to loss of context when an agent turn ended prematurely.

### 2. Tool Timeout Increase
- **Change**: Hardcoded tool timeout increased from 3s → 60s.
- **File**: internal/agent/coordinator.go
- **Reason**: 3s was too aggressive for complex engineering tasks. 60s provides a safer buffer.

### 3. Agent Iteration Drive (The "Todo Nagger")
- **Mechanism**:
    - **System Reminder**: If the todo list is NOT empty, a <system_reminder> is injected every turn to enforce completion.
    - **Wake-up Prompt**: Background job completion notifications now include an explicit command to fix failures and finish the list.

### 4. Semantic Folding & Parallelism
- **Folding**: view tool now supports fold: true for Go/Nim, collapsing function bodies to reduce context size.
- **Parallelism**: Mandatory parallel discovery rules added to all Agent templates.

## Architectural Learnings

### Tool Sovereignty
- Moved from Go-native implementations of grep/glob/ls to industrial CLI tools (rg, ls).
- Result: >1000x acceleration in search and listing on large repos like nim-src.

### PTC (Foreman) usage
- PTC is the preferred tool for composite tasks. Use it to collapse search-edit-verify cycles.

## Next Steps
- Implement **Context Diff Compaction** in PrepareStep to automatically collapse redundant tool outputs in history.
- Further refine **Nushell (nu)** tool integration for high-density data pipelines.
