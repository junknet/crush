# Mobile APK Release & NATS Integration (Updated 2026-05-25)

## Current Public NATS Setup

**Deployment Status**: Active, plain HTTP/TCP (no TLS hardening yet)

| Component | Protocol | Endpoint | Token | Notes |
|-----------|----------|----------|-------|-------|
| TUI Relay (Go) | NATS TCP | `nats://47.110.255.240:4222` | `ymm_rpc_2026` | Defined in `scripts/launch_crush*.sh` (lines 47, 63, 93) |
| Mobile Client (RN) | NATS WebSocket | `ws://47.110.255.240:8443` | `ymm_rpc_2026` | Default in `mobile/crush_mobile/app/index.tsx:48` |
| Local Dev NATS | NATS TCP | `localhost:4222` | n/a | Launched by `acceptance/scenarios/relay_mobile_joint.sh:85` |

**Environment Override**:
- Mobile uses `EXPO_PUBLIC_CRUSH_SERVER_URL` env var or falls back to `ws://47.110.255.240:8443`
- Android cleartext traffic allowed (app.config.js:31 `usesCleartextTraffic: true`)
- Mobile UI accepts manual address entry (app/index.tsx:3248 placeholder)

---

## Mobile APK Release: Current State (2026-05-25 Assessment)

### ✅ Already in Place

1. **Expo/React Native foundation** (v0.1.0)
   - `mobile/crush_mobile/app.config.js` defines package `com.junknet.crushmobile` (or `.dev` for debug)
   - `android/app/build.gradle` has multi-ABI support: `armeabi-v7a,arm64-v8a,x86,x86_64`
   - Hermes + WebP/GIF support enabled in `android/gradle.properties`
   - Android 9+ cleartext WebSocket traffic explicitly enabled

2. **Local build fully tested**
   - `expo prebuild` already ran → `android/` directory complete with Gradle wrapper
   - Dev buildType works: `./gradlew assembleDebug` → `app/build/outputs/apk/debug/`
   - `eas.json.example` exists with `development`, `preview`, `preview2`, `production` profiles
   - Gradle build system ready for release signing

3. **Integration testing harness ready**
   - `acceptance/scenarios/relay_mobile_joint.sh` automates full e2e test (NATS + TUI + mobile)
   - Emulator bridge configured: `ws://10.0.2.2:${NATS_WS_PORT}` for localhost testing

### ❌ Critical Blockers for APK Release

1. **`eas.json` file is missing** (CONFIRMED 2026-05-25)
   - Only `eas.json.example` exists in repo
   - **Action**: `cp eas.json.example eas.json` + fill Expo token & SDK paths

2. **Release Keystore Not Configured**
   - `android/app/build.gradle:115` references debug signing config
   - Comment at line 118: "You need to generate your own signing config"
   - No `release.keystore` file committed or documented
   - Production APK cannot be signed without this

3. **CI/CD Release Pipeline Missing**
   - `.github/workflows/build.yml` only triggers external Tailscale build API
   - No step to download signed APK
   - No GitHub Releases upload
   - No Play Store submission step
   - No automated versioning (currently hardcoded `0.1.0` in app.config.js)

4. **Security/Versioning Gaps**
   - Public NATS tokens in scripts (plain text `ymm_rpc_2026`)
   - Version not auto-incremented per build
   - No distinction between staging/production environment URLs

---

## Release Paths: Two Options

### Option A: Local Gradle Build (Quickest, Production-Ready)

```bash
cd mobile/crush_mobile/android

# 1. Generate release keystore (one-time)
keytool -genkeypair -v -keystore release.keystore \
  -alias crush -keyalg RSA -keysize 2048 -validity 10000
# (interactively enter password, CN, org, etc. — store safely)

# 2. Update app/build.gradle signingConfigs.release 
#    (currently mirrors debug; edit to reference release.keystore)

# 3. Build signed release APK
./gradlew assembleRelease

# Output: android/app/build/outputs/apk/release/app-release.apk
```

**Pros**: Full control, no cloud account, repeatable  
**Cons**: Keystore must be managed & versioned securely

### Option B: EAS Cloud Build (CI-Ready, No Local Signing)

```bash
cd mobile/crush_mobile

# 1. Activate EAS config (one-time)
cp eas.json.example eas.json
# Edit eas.json: add Expo account token, fill Android SDK ROOT path

# 2. Trigger build (per-release)
npx eas-cli build -p android --profile production

# 3. Download signed APK from EAS dashboard or CLI
```

**Pros**: Zero keystore management, integrated CI/CD  
**Cons**: Requires Expo account & internet, not fully offline

---

## Command Reference for Developer

### Build & Test Locally
```bash
# Full integration test (NATS + TUI + mobile emulator)
bash acceptance/scenarios/relay_mobile_joint.sh

# Debug build (dev sign)
cd mobile/crush_mobile/android && ./gradlew assembleDebug

# Release build (production sign — after keystore setup)
./gradlew assembleRelease

# EAS cloud build (after eas.json activated)
cd mobile/crush_mobile && npx eas-cli build -p android --profile production
```

### Environment Overrides
```bash
# Override NATS endpoint for testing
export EXPO_PUBLIC_CRUSH_SERVER_URL="ws://your-nats-server:8443"
npx expo run:android  # or EAS build

# Set NATS relay token (if changed from ymm_rpc_2026)
export CRUSH_RELAY_TOKEN="your_new_token"
```

---

## Key Files to Watch / Modify for Release

| File | Purpose | Status |
|------|---------|--------|
| `mobile/crush_mobile/eas.json` | EAS build config | **MISSING** — copy from `.example` |
| `mobile/crush_mobile/android/release.keystore` | Release signing cert | **TODO** — generate via keytool |
| `mobile/crush_mobile/android/app/build.gradle` | Gradle build definition | **READY** — edit signingConfigs.release |
| `mobile/crush_mobile/app.config.js` | App version & package | **HARDCODED 0.1.0** — auto-increment on release |
| `mobile/crush_mobile/app/index.tsx:48` | NATS endpoint default | **CONFIGURED** — use env override for staging |
| `.github/workflows/build.yml` | CI release trigger | **INCOMPLETE** — add APK download/upload steps |
| `scripts/launch_crush*.sh` | NATS token/server | **PUBLIC TOKEN** — migrate to secrets manager |

---

## Risk Assessment

**🔴 High**: No production signing configured; `eas.json` missing  
**🟡 Medium**: Hardcoded version number; public NATS token in scripts  
**🟢 Low**: Cleartext WebSocket acceptable for dev/staging; local test harness solid

---

## Next Immediate Steps

1. **Generate release keystore** (or use EAS auto-signing)
2. **Activate `eas.json`** from template
3. **Run `acceptance/scenarios/relay_mobile_joint.sh`** end-to-end test
4. **Build & sign first APK** (local gradle or EAS)
5. **Upload to GitHub Releases** (optional Play Store)
