# Mobile Session History Loading Fix (2026-05-25)

## Problem: "10 Minutes Blank Screen" on Session Switch

User reported: switching to certain sessions (e.g., `3ee62208` audit session) resulted in **completely empty message list**, even after waiting 10+ minutes.

### Root Causes (3 layers)

#### 1. **30-Minute Time Window Filtering Out Old Sessions**
**File**: `mobile/crush_mobile/lib/crush/api.ts:223-225`

**Problem**: 
- Session `3ee62208` last event was **60 minutes ago** (TUI paused)
- JetStream ordered consumer created with `startAtTimeDelta(30 min)` finds **zero messages** in that window
- Consumer sits idle forever → UI stays blank

**Evidence**:
```
JetStream stream CRUSH_EVENTS:
  3ee62208: 4,971 events, last_event_age=60 min  ← Outside 30 min window!
  1ac7d28d: 21,079 events, last_event_age=12 min ✓
  4653d4ff: 2,162 events, last_event_age=0.1 sec ✓
```

**Fix**: Default to `deliverAll()` (full history), only use time window if caller explicitly passes `historyMs`.

#### 2. **sessionIDRef Stale During First Event Arrival**
**File**: `app/index.tsx:3110` (new) + `app/index.tsx:2417` (existing)

**Problem**:
- First NATS envelope arrives in 47-74ms (before React commit)
- `sessionIDRef.current` is still `''` (old value)
- `handleEvent` checks `nextMessage.session_id !== sessionIDRef.current` → **message filtered out**
- No messages enter Map → no flush → UI blank

**Fix**: Explicitly sync ref **before** subscribe in useEffect.

#### 3. **TUI Relay Publishing Sub-Agent Messages Under Parent Subject**
**File**: `app/index.tsx:2915-2926` (mobile filter removed)

**Problem**:
- TUI relay publishes sub-agent message events to **parent session's NATS subject**
- But message envelope has `session_id = sub_agent_session_id` (not parent)
- Mobile's `nextMessage.session_id !== sessionIDRef.current` filter **rejects all sub-agent messages**
- Result: 411 message envelopes arrive, ~400 get filtered

**Fix**: Trust NATS subject filter (already scoped to sessionID), remove redundant client-side filter.

---

## Solution Implemented

### Changes Made

| File | Line | Change |
|---|---|---|
| `lib/crush/api.ts` | 215-227 | Default `deliverAll()` instead of `startAtTimeDelta(30 min)` |
| `app/index.tsx` | 3110 | Add `sessionIDRef.current = sessionID` before subscribe |
| `app/index.tsx` | 2915-2926 | Remove `nextMessage.session_id !== sessionIDRef.current` filter |

---

## Performance Results (Post-Fix)

### Session: `3ee62208` (Audit, 4,971 events, last activity 60 min ago)

```
T0 session_change       0 ms
T1 subscribe_begin     +1 ms
T2 first_event        +28 ms
T3 first_flush       +252 ms   count=20 messages
─────────────────────────────────────────────
First screen visible:  252 ms ✅
```

**Before fix**: 10+ minutes → blank screen  
**After fix**: 252 ms → 20 messages visible

### Session: `1ac7d28d` (nim-src, 21,079 events, last activity 12 min ago)

```
T0 session_change       0 ms
T1 subscribe_begin      0 ms
T2 first_event         74 ms
T3 first_flush      2,661 ms   count=149 messages
─────────────────────────────────────────────
First screen visible: 2.66 sec ✅
```

---

## Key Insights

1. **Time windows are dangerous**: A seemingly reasonable "last 30 min" breaks the common case of paused sessions. Better to stream full history + let frontend virtualization handle volume.

2. **Ref sync timing matters**: React's batching can cause race conditions with async operations. Explicit ref sync before async call prevents stale closures.

3. **Subject-level filtering is sufficient**: Don't add redundant client-side session_id checks when NATS already filters by subject.

---

## Build & Deploy

```bash
cd mobile/crush_mobile/android
export ANDROID_HOME=/home/junknet/Android/Sdk
export ANDROID_SDK_ROOT=$ANDROID_HOME
./gradlew assembleRelease

# Deploy to test device
adb -s 192.168.0.106:5555 install -r app/build/outputs/apk/release/app-release.apk
```

---

## Next Steps

- [ ] Deploy to other 3 test devices (103, 105, 109)
- [ ] Monitor for any regressions with very large sessions (>50k events)
- [ ] Consider TUI relay fix to not publish sub-agent messages under parent subject (long-term)

---

## TUI Relay Sub-Agent Isolation Fix (2026-05-25 continuation)

### Problem: Sub-Agent Messages Contaminating Parent Session

**File**: `internal/relay/relay.go:141-150`, `internal/relay/events.go:32-46`

**Problem**:
- TUI relay publishes **all** events from app event bus to parent session's NATS subject
- Sub-agent message events have `session_id = sub_agent_id` but land on parent's subject
- Mobile's strict filter `nextMessage.session_id !== sessionIDRef.current` rejects them
- Result: 411 message envelopes in stream, ~400 filtered out by mobile

**Root Cause**: Relay didn't check event ownership before publishing.

### Solution: Session-Scoped Event Filtering in TUI Relay

**Changes**:

| File | Lines | Change |
|---|---|---|
| `internal/relay/events.go` | 32-46 | New `eventSessionID(ev any) string` — extracts owning session from raw event; handles `ParentSessionID` for sub-agent lifecycle events |
| `internal/relay/relay.go` | 141-150 | Events loop: `if owner := eventSessionID(ev); owner != "" && owner != sessionID { continue }` — drop foreign-session events before publishing |

**Key Logic**:
- `message.Message` → `e.Payload.SessionID`
- `session.Session` → `e.Payload.ParentSessionID` (if set, else `e.Payload.ID`)
- `history.File` → `e.Payload.SessionID`
- `permission.PermissionRequest` → `e.Payload.SessionID`
- Global events (mcp/lsp/permission_notification) → return `""` (forwarded by all relays)

### Verification

**Stream isolation check** (5 min window):
```
Subject: crush.sess.1ac7d28d-b20a-4793-bf10-8162db90ea34.events
Recent messages: 1 envelope
  payload.session_id=1ac7d28d : 1 ✓
  foreign session_id : 0 ✓

Result: ✅ ISOLATED — relay filtering working
```

**Before fix**: 411 message envelopes, ~400 with foreign `session_id`  
**After fix**: Only self-owned messages on subject

### Deployment Status

- **TUI binary**: Rebuilt 17:42 with `eventSessionID` filter, cached at `~/.cache/crush-prod/crush`
- **Mobile APK**: 069 build (md5 f9f427a1), strict filter + deliverAll, deployed to 106
- **Stream**: 24h of historical contamination still present (MaxAge=24h), will auto-purge; mobile's strict filter prevents UI impact

### End-to-End Semantics

```
TUI app event bus (global)
  ↓ a.Events(ctx)
relay.Run(sessionID=parent)
  ↓ filter: eventSessionID(ev) == sessionID
  ├─ self message ✓
  ├─ sub-agent message ✗ (sub-agent's relay handles it)
  └─ global event (mcp/lsp) ✓
NATS subject crush.sess.<parent>.events
  ↓ mobile subscribe
mobile handleEvent
  ├─ subject filter already scoped
  └─ strict session_id filter as defense layer
```

### Next Steps

- [x] Verify stream isolation (confirmed ✅)
- [ ] User restarts TUI to pick up new binary (smart-rebuild should auto-trigger)
- [ ] Test with new sub-agent tasks to confirm no cross-contamination
- [ ] Monitor 24h for historical stream purge
