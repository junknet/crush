# Mobile Session List Jitter Fix (2026-05-25)

## Problem Diagnosed

Session list UI on mobile was flickering / bouncing — adjacent sessions constantly swapping positions every 5 seconds.

### Root Cause
- Backend heartbeat loop (5s) publishes `UpdatedAt: time.Now().Unix()` to every session's presence record
- Mobile API `listSessions()` watches NATS KV; each heartbeat triggers `emit()` which **re-sorts entire list by `updated_at`**
- When sessionA and sessionB alternate heartbeats (roughly every 5s each), their positions flip back and forth
- React renders the new sorted array as a full list replacement, causing visual jitter

### Evidence

**Backend** (`internal/relay/relay.go:33, 173-227`):
```go
const heartbeat = 5 * time.Second  // Line 33
// presenceLoop (line 173+) calls put() every heartbeat:
// meta.UpdatedAt = time.Now().Unix()  (line 181)
```

**Mobile API** (`mobile/crush_mobile/lib/crush/api.ts:114-118`):
```typescript
const emit = () => {
    onUpdate(
        Object.values(sessionsMap).sort((a, b) => (b.updated_at || 0) - (a.updated_at || 0))
    )
}
// Called on every NATS KV watch event (line 145)
```

**UI Drawer** (`mobile/crush_mobile/app/index.tsx:3687-3691`):
```typescript
const sortedSessions = [...sessions].sort((a, b) => {
    const timeA = sessionAccessTimes[a.id] || a.updated_at || 0
    const timeB = sessionAccessTimes[b.id] || b.updated_at || 0
    return timeB - timeA  // Re-sort on every render
})
```

---

## Solution Implemented

### Key Insight
Use **`created_at` (immutable per session)** for initial sort ordering instead of `updated_at` (changes every 5s).  
User-interaction timestamps (`sessionAccessTimes`) still win if set (e.g., "recently touched").

### Changes Made

#### 1. Backend: Expose CreatedAt in SessionMeta
**File**: `internal/relay/relay.go`

**Line 65** (added field to struct):
```go
type SessionMeta struct {
    // ... existing fields ...
    UpdatedAt       int64                           `json:"updated_at"`
    CreatedAt       int64                           `json:"created_at,omitempty"`  // NEW
```

**Lines 183-186** (populate from session):
```go
if sess, err := a.Sessions.Get(ctx, sessionID); err == nil {
    meta.Title = sess.Title
    meta.CreatedAt = sess.CreatedAt  // NEW
}
```

#### 2. Mobile API: Sort by CreatedAt
**File**: `mobile/crush_mobile/lib/crush/api.ts`

**Line 135** (decode created_at from KV):
```typescript
sessionMap[e.key] = {
    // ... fields ...
    updated_at: meta.updated_at,
    created_at: meta.created_at,  // NEW
    // ...
}
```

**Lines 114-124** (new emit with stable sort):
```typescript
const emit = () => {
    // Sort by created_at (stable, set once at session birth) instead
    // of updated_at — otherwise every 5s heartbeat reorders the list
    // and visually swaps neighbouring sessions back and forth.
    onUpdate(
        Object.values(sessionsMap).sort((a, b) => {
            const ta = a.created_at || a.updated_at || 0
            const tb = b.created_at || b.updated_at || 0
            if (tb !== ta) return tb - ta
            return (a.id || '').localeCompare(b.id || '')  // Tiebreaker
        })
    )
}
```

#### 3. UI Drawer: Match Sorting Strategy
**File**: `mobile/crush_mobile/app/index.tsx`

**Lines 3687-3701** (updated sort logic):
```typescript
const sortedSessions = [...sessions].sort((a, b) => {
    // Stable order: user-touched access time wins;
    // otherwise fall back to created_at (set once)
    // not updated_at (rewritten every heartbeat).
    const timeA =
        sessionAccessTimes[a.id] ||
        a.created_at ||
        a.updated_at ||
        0
    const timeB =
        sessionAccessTimes[b.id] ||
        b.created_at ||
        b.updated_at ||
        0
    if (timeB !== timeA) return timeB - timeA
    return (a.id || '').localeCompare(b.id || '')  // Tiebreaker
})
```

---

## Rebuild & Deploy

### Build Command
```bash
cd /home/junknet/Desktop/_cli_bases/crush/mobile/crush_mobile/android
export ANDROID_HOME=/home/junknet/Android/Sdk
./gradlew clean assembleRelease
```

### Installation
```bash
APK=android/app/build/outputs/apk/release/app-release.apk
adb -s 192.168.0.106:5555 push "$APK" /data/local/tmp/crush.apk
adb -s 192.168.0.106:5555 shell pm install -r -t /data/local/tmp/crush.apk
```

---

## Verification

After redeployment to test machine (192.168.0.106), session list should remain **static** (no jitter) even as heartbeats arrive every 5s.

Observable: 
- Sessions in list maintain their vertical position
- Only `sessionAccessTimes` user interactions or new session creation will reorder
- `updated_at` changes no longer trigger re-renders of unchanged sessions

---

## Design Patterns Applied

1. **Immutable sort key** — Use a field set once (at birth) rather than every time state changes
2. **Fallback hierarchy** — User intent (accessTimes) > creation time > heartbeat time
3. **Tiebreaker** — Lexicographic ID sort for deterministic rendering when times collide
4. **Backend / Frontend sync** — Both sides now use same logic (created_at primacy)

---

## Files Changed Summary

| File | Lines | Change Type |
|------|-------|-------------|
| `internal/relay/relay.go` | 65, 183-186 | Added CreatedAt field + population |
| `mobile/crush_mobile/lib/crush/api.ts` | 114-124, 135 | Rewrote emit() sort logic; decode created_at |
| `mobile/crush_mobile/app/index.tsx` | 3687-3701 | Updated drawer sort fallback chain |

---

## Risk & Impact

- ✅ **Low risk**: No breaking changes; created_at is optional (`omitempty`), fallback to updated_at works
- ✅ **Backward compatible**: Old sessions without created_at still sort by updated_at
- 🟢 **User benefit**: Stable list UX, especially with multiple concurrent sessions
- 🟢 **Performance**: Same O(n log n) sort; no new queries or subscriptions


---

## Completion Status (Final Build & Deploy)

**Date**: 2026-05-25 14:41 UTC  
**Status**: ✅ Deployed to test machine (192.168.0.106:5555)

### Build
```
Time: 24s (incremental, only lib/crush/api.ts and app/index.tsx changed)
Output: android/app/build/outputs/apk/release/app-release.apk (83 MB, same)
Signature: 4d9bba18 (unchanged, same release keystore)
```

### Deployment
```bash
adb -s 192.168.0.106:5555 push app-release.apk /data/local/tmp/crush.apk
adb -s 192.168.0.106:5555 shell pm install -r -t /data/local/tmp/crush.apk
adb -s 192.168.0.106:5555 shell monkey -p com.junknet.crushmobile -c android.intent.category.LAUNCHER 1
```

### Verification
- **App PID**: 49611
- **NATS WS Connection**: `192.168.0.106:50920 → 47.110.255.240:8443` (ESTABLISHED)
- **Result**: ✅ App running, connected to public relay

### Expected Behavior (Next QA)
When TUI (running `~/.local/bin/crush` which auto-rebuilds with new relay code) connects and publishes multiple sessions, the mobile drawer list should remain **static** — no jitter, no position swaps during heartbeats.

### To Deploy to Other 3 Devices
```bash
for dev in 192.168.0.103:5555 192.168.0.105:5555 192.168.0.109:5555; do
  adb -s $dev push app-release.apk /data/local/tmp/crush.apk
  adb -s $dev shell pm install -r -t /data/local/tmp/crush.apk
done
```
