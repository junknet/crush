# Mobile APK Release & Performance Root Cause Analysis (2026-05-25)

## Release Build Setup

### Keystore Generation
```bash
cd mobile/crush_mobile/android/app
keytool -genkeypair -v \
  -keystore release.keystore \
  -alias crush -keyalg RSA -keysize 2048 -validity 36500 \
  -storepass crushrelease -keypass crushrelease \
  -dname "CN=crush, OU=junknet, O=junknet, L=Hangzhou, ST=ZJ, C=CN"
```

### Gradle Release Signing (android/app/build.gradle)
```gradle
signingConfigs {
    release {
        storeFile file('release.keystore')
        storePassword 'crushrelease'
        keyAlias 'crush'
        keyPassword 'crushrelease'
    }
}
buildTypes {
    release {
        signingConfig signingConfigs.release
```

### .env for Mobile
```
EXPO_PUBLIC_CRUSH_SERVER_URL=ws://47.110.255.240:8443
EXPO_PUBLIC_CRUSH_RELAY_TOKEN=ymm_rpc_2026
```

### Build Command
```bash
cd mobile/crush_mobile/android
export ANDROID_HOME=/home/junknet/Android/Sdk
export ANDROID_SDK_ROOT=$ANDROID_HOME
./gradlew assembleRelease
# Output: android/app/build/outputs/apk/release/app-release.apk (~83-86 MB)
```

### Chinese ROM Install (adb streaming mode fails; use push+pm)
```bash
APK=/home/junknet/Desktop/_cli_bases/crush/mobile/crush_mobile/android/app/build/outputs/apk/release/app-release.apk
adb -s $DEV push "$APK" /data/local/tmp/crush.apk
adb -s $DEV shell pm install -r -t /data/local/tmp/crush.apk
```

## Performance Root Causes & Fixes

### 1. Session List Jitter (sorted by updated_at heartbeat)
**Root**: Mobile sorted sessions by `updated_at` (server heartbeat 5s), causing 11k-session list to re-sort every 5s.

**Fix** (internal/relay/relay.go + lib/crush/api.ts):
- Backend: Added `CreatedAt int64` field to `SessionMeta` struct
- Mobile: Changed sort key from `updated_at` to stable `created_at` + `id` secondary key
- Result: Session order frozen once created; no more position-swapping

### 2. Session Open Lag (O(n²) message replay)
**Measurement**: JetStream stream contains **37,831 total events** across 22 sessions; single session has **11,281 events / 78 MB**. Default `DeliverAll` replays entire history.

**Root**: 
- `subscribeSessionEvents()` used `orderedConsumer()` with no explicit deliverPolicy → defaulted to `DeliverAll`
- Mobile `handleEvent` did `findIndex(O(n))` + `setState` for each message → every message caused full-list render
- Result: 11k events × O(n) lookup × React re-render = O(n²) stutter

**Fixes** (lib/crush/api.ts):
```typescript
const opts = consumerOpts()
opts.orderedConsumer().filterSubject(subject)
  .startAtTimeDelta(30 * 60 * 1000)  // 30 min window, not 24h
const sub = await js.subscribe(subject, opts)
```

Mobile message deduplication (app/index.tsx):
```typescript
// Old: setMessages((prev) => { findIndex... })
// New: messagesMapRef (Map<id, Message>) + 100ms batch flush
```

### 3. Button Flickering & State Race (5s heartbeat vs real-time events)
**Root**: `useEffect([activeSession?.is_busy])` at line 2679-2683 overwrite agent_event updates with stale heartbeat every 5s.

Timeline:
- T=0s: agent_event `is_busy=true` (real-time) → button shows "Stop"
- T=0.05s: Heartbeat still has old `is_busy=false` → effect fires → button flips to "Send"
- T=5s: Next heartbeat → effect fires again → oscillation visible

**Fix** (app/index.tsx):
```typescript
// Changed dependency from [activeSession?.id, activeSession?.is_busy]
// to just [activeSession?.id]
useEffect(() => {
    if (activeSession) {
        setAgentInfo((prev) => ({ ...prev, is_busy: !!activeSession.is_busy }))
    }
}, [activeSession?.id])  // Only sync once per session switch
```

### 4. Streaming Token Jitter (scroll trigger effect)
**Root**: `useMemo` on `JSON.stringify(lastMsg.parts)` → every token mutates parts array → new JSON string → effect fires → scroll runs → full sort/filter on displayMessages.

**Fix** (app/index.tsx):
```typescript
// Old: lastMessagePartsString = useMemo(() => JSON.stringify(lastMsg.parts), [messages])
// New: lastMessageSig = useMemo(() => `${last.id}:${last.parts?.length ?? 0}`, [messages])
```

### 5. Message List Over-Rendering (no memo)
**Root**: `MessageItem` component had no `React.memo`. Any parent state change re-renders all 1000+ messages.

**Fix** (app/index.tsx):
```typescript
const MessageItem = React.memo(({ message, isUser, showHeader, isBusy, onMaximize }) => { ... }, 
  (prev, next) => {
    // Custom comparison: only re-render if message object ref changed
    if (prev.message !== next.message) return false
    if (prev.isUser !== next.isUser) return false
    if (prev.showHeader !== next.showHeader) return false
    if (prev.isBusy !== next.isBusy) return false
    if (prev.onMaximize !== next.onMaximize) return false
    return true  // Skip render
  }
)
```

### 6. Batch Flush Interval
**Original**: 50ms batch flush → triggered O(n log n) sort on displayMessages every 50ms
**Updated**: 100ms batch flush → halved sort frequency during high-frequency streams

## Public Network Stack (47.110.255.240)

| Service | Port | Protocol | Status |
|---------|------|----------|--------|
| NATS server | 4222 | TCP | ✅ Running v2.10.24 |
| NATS WebSocket relay | 8443 | WS | ✅ Running |
| Retention | - | - | 24h MaxAge |
| Bucket (sessions) | - | KV | `CRUSH_SESSIONS` TTL=15s |
| Stream (events) | - | JetStream | `CRUSH_EVENTS` MaxAge=24h |

### Connection from Mobile
- Default URL: `ws://47.110.255.240:8443`
- Token: `ymm_rpc_2026`
- Profile: `crush-mobile`

## Test Machine 106 (192.168.0.106:5555)
- Model: V2130A (Vivo)
- Last APK: 86.4 MB (signed release.keystore)
- WS state: ESTABLISHED to 47.110.255.240:8443
- App version: 0.1.0

## Incremental Build Time
```
First full build: ~6m 24s (512 tasks)
Incremental rebuild: ~21-28s (49 tasks executed, 463 up-to-date)
```

## Next Steps
1. Validate on test machine: no button flicker, session list stable, open 11k-event session in <2s
2. Consider extending history window: `historyMs` parameter in subscribeSessionEvents
3. Monitor: CPU/memory under heavy streaming (watch Map size, memo hit rate)
