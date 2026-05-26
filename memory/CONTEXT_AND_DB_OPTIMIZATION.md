# Context Compaction & Database Hygiene (May 2026)

## Overview
To support deep architecting and long-running sessions, Crush now implements proactive context compaction and database pruning, specifically targeting large binary/Base64 image data.

## Core Implementations

### 1. Historical Media Truncation (Context Compaction)
- **Problem**: Image data (Base64) in the conversation history consumes massive amounts of the Context Window and increases per-turn token costs.
- **Solution**: 
    - Modified `ToAIMessage` in `internal/message/content.go` to support a `TruncateMedia` option.
    - Updated `preparePrompt` in `internal/agent/agent.go` to force-truncate all media in historical messages.
- **Result**: The model only sees a placeholder like `[Image: path (truncated)]` for historical images. Only the **current turn's** attachments are sent in full.

### 2. Database Pruning (DB Hygiene)
- **Problem**: The `.crush/crush.db` file grows indefinitely as Base64 fragments accumulate in the `messages` table.
- **Solution**:
    - Added a `Prune` method to `message.Service` (`internal/message/message.go`).
    - The method scans the DB and wipes the `Data` field from any `BinaryContent` or `ToolResult` part that is:
        - Older than 24 hours.
        - Already "seen" by the model (i.e., not part of the most recent messages).
    - **Trigger**: The cleanup task is automatically triggered in a background goroutine on application startup (`internal/app/app.go`).

## Code References
- **`internal/message/content.go`**: `ToAIMessageOptions` and truncation logic.
- **`internal/message/message.go`**: `Prune` implementation.
- **`internal/agent/agent.go`**: Application of truncation in `preparePrompt`.
- **`internal/app/app.go`**: Background pruning trigger.

## Benefits
- **Token Efficiency**: Dramatic reduction in input tokens for long sessions.
- **Model Attention**: Prevents noise from large binary blobs, keeping the model focused on architectural logic.
- **Storage Sustainability**: Keeps the local database light and fast.
