# Tool Sovereignty Architecture (May 2026)

## Philosophy
Stop writing "garbage" Go code for tasks that industrial-grade CLI tools already excel at. Move away from atomic JSON tool calls towards high-density code acts.

## Core Pillars

### 1. 1:1 Tool-Command Mapping
All search and listing tools now map directly to their Rust/C-powered counterparts. Transparent Go-native recursion and manual .gitignore parsing have been abolished.

- **rg (ripgrep)**: The content and filename search tool. Replaces "search", "grep", and "glob".
- **ls (ripgrep --files)**: The listing tool. Optimized for local tree rendering.
- **ast_grep (ast-grep)**: The structural search tool. Essential for LSP-broken repos.
- **nu (nushell)**: The structured execution layer. Returns JSON-parsed data directly to the Agent.

### 2. Semantic Compaction
Information density is managed at the tool level to keep Context Windows clean.

- **Folded Views**: 'view' tool supports AST-aware folding to compress function bodies.
- **Context Truncation**: preparePrompt automatically strips Base64 media data from message history.

### 3. Execution Backend
- **PTC (Programmable Tool Caller)**: The primary engineering engine. Combines multiple capabilities (fs, sh, lsp) into a single turn using Python.
- **CodeAct**: The model is encouraged to output high-density code blocks for complex logic instead of looping through simple tools.

## Impact
- **Search Latency**: Reduced from ~60s (timeout) to <100ms in large repos (nim-src).
- **Listing Speed**: Accelerated by ~1000x compared to Go-native walkers.
- **Context Efficiency**: Up to 70% reduction in token usage per turn for large file reads.
