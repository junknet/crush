# Mobile APK Release: Completion Log (2026-05-25)

## Session Objective
Build and deploy signed release APK of `crush-mobile` to 4 test devices; verify NATS WebSocket connectivity from mobile to public relay at `47.110.255.240:8443`.

## Actions Completed ✅

### 1. Generated Release Keystore
```bash
cd mobile/crush_mobile/android/app
keytool -genkeypair -v -keystore release.keystore \
  -alias crush -keyalg RSA -keysize 2048 -validity 36500 \
  -storepass crushrelease -keypass crushrelease \
  -dname "CN=crush, OU=junknet, O=junknet, L=Hangzhou, ST=ZJ, C=CN"
```
**Result**: `release.keystore` (2.7 KB) generated with fingerprint `4d9bba18`

### 2. Integrated Release Signing into build.gradle
**File**: `mobile/crush_mobile/android/app/build.gradle` (lines 100-112)
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
**Before**: `release` buildType was using `debug` signing config  
**After**: `release` buildType now references `signingConfigs.release`

### 3. Created `.env` (Expo)
**File**: `mobile/crush_mobile/.env`
```env
EXPO_PUBLIC_CRUSH_SERVER_URL=ws://47.110.255.240:8443
EXPO_PUBLIC_CRUSH_RELAY_TOKEN=ymm_rpc_2026
```

### 4. Activated eas.json
**File**: `mobile/crush_mobile/eas.json`  
Copied from `eas.json.example` (no modifications needed; all profiles already configured)

### 5. Built Release APK
```bash
cd mobile/crush_mobile/android
export ANDROID_HOME=/home/junknet/Android/Sdk
./gradlew assembleRelease
```
**Build Stats**:
- Time: 6m 24s
- Tasks: 512 actionable (462 executed, 50 up-to-date)
- Warnings: Only Kotlin/Java deprecation hints (acceptable)
- Output: `/mobile/crush_mobile/android/app/build/outputs/apk/release/app-release.apk` (83 MB)

### 6. Deployed to 4 Test Devices

| Device IP | Model | install() Result | Notes |
|-----------|-------|------------------|-------|
| 192.168.0.103:5555 | PEPM00 (OPPO) | ✅ Success | Initial stream error (OPPO ROM), fixed via `adb push + pm install` |
| 192.168.0.105:5555 | V2130A (Vivo) | ✅ Success | Same OPPO/Vivo ROM workaround |
| 192.168.0.106:5555 | V2130A (Vivo) | ✅ Success | **Test machine** (focal device) |
| 192.168.0.109:5555 | PLD110 | ✅ Success | Same ROM workaround |

**Installation Technique** (国产 ROM Fix):
```bash
APK=/path/to/app-release.apk
adb -s $DEV push "$APK" /data/local/tmp/crush.apk
adb -s $DEV shell pm install -r -t /data/local/tmp/crush.apk
```
Standard `adb install -r` returned `DELETE_FAILED_INTERNAL_ERROR` with binary output corruption.

### 7. Verified Signatures & Connectivity

**Signature Verification** (all 4 devices):
```
versionName=0.1.0
signatures=PackageSignatures{...4d9bba18...}
```
All devices have identical fingerprint (release keystore signature), enabling seamless reinstall/upgrade.

**NATS WebSocket Connectivity** (Test Machine 192.168.0.106):
- App PID: 44401
- Established TCP connection: `192.168.0.106:40160 → 47.110.255.240:8443` (state=01 ESTABLISHED)
- Public NATS server confirmed online:
  - TCP 4222: `NATS v2.10.24` responding to `nats://47.110.255.240:4222`
  - WebSocket 8443: HTTP 101 Upgrade successful

## Key Files Modified / Created

| File | Change |
|------|--------|
| `mobile/crush_mobile/android/app/release.keystore` | **NEW** (generated) |
| `mobile/crush_mobile/android/app/build.gradle:100-112` | Added release signingConfigs; updated release buildType |
| `mobile/crush_mobile/.env` | **NEW** (created from template) |
| `mobile/crush_mobile/eas.json` | **NEW** (copied from .example) |

## Build Command Reference

### For Next Release
```bash
cd /home/junknet/Desktop/_cli_bases/crush/mobile/crush_mobile/android

# Increment version in ../app.config.js (currently "0.1.0")
# Then:
export ANDROID_HOME=/home/junknet/Android/Sdk
./gradlew clean assembleRelease

# Output location:
# android/app/build/outputs/apk/release/app-release.apk
```

### Installation on Device (any ROM)
```bash
adb -s $DEV push android/app/build/outputs/apk/release/app-release.apk /data/local/tmp/crush.apk
adb -s $DEV shell pm install -r -t /data/local/tmp/crush.apk
```

## NATS Relay Configuration

| Role | Protocol | Endpoint | Token | Notes |
|------|----------|----------|-------|-------|
| TUI (Go) | NATS TCP | `nats://47.110.255.240:4222` | `ymm_rpc_2026` | Configured in `scripts/launch_crush*.sh` |
| Mobile (RN) | NATS WebSocket | `ws://47.110.255.240:8443` | `ymm_rpc_2026` | Configured in `.env` & `app.config.js:31` |

Token and endpoints are plaintext in this private dev repo; migrate to GitHub Secrets for production CI/CD.

## Known Issues & Workarounds

### 1. National ROM `adb install` Failure
**Symptom**: `adb install` returns binary garbage ending with `DELETE_FAILED_INTERNAL_ERROR`  
**Root Cause**: OPPO/Vivo ROM intercepts streaming install output  
**Fix**: Use `push + pm install` instead (proven on OPPO PEPM00, Vivo V2130A, PLD110)

### 2. Hardcoded Version (0.1.0)
**Location**: `mobile/crush_mobile/app.config.js` line 6  
**Impact**: No auto-versioning per release  
**Future**: Add CI step to read version from `package.json` and auto-increment

### 3. Cleartext WebSocket Traffic
**Security**: Acceptable for dev/staging (explicitly enabled in app.config.js:31)  
**Production**: Upgrade to `wss://` and TLS-enabled NATS on public server

## Risk Summary
- 🟢 **Low**: Build & deployment process is solid; release keystore created and integrated
- 🟡 **Medium**: CI/CD pipeline not yet fully automated; version manually incremented
- 🔴 **High**: None remaining (release APK is production-ready for private distro)

## Next Steps (Optional)
1. Add GitHub Actions workflow to auto-build on tag push (currently manual)
2. Upload APK to GitHub Releases for team distribution
3. Migrate NATS endpoint/token to GitHub Secrets
4. Add Play Store submission step (if desired)
5. Implement `wss://` + TLS for production NATS

