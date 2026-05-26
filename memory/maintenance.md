# Project Maintenance & Environment

## Build and Test Commands

### Go Backend
- **Build relay**: `go build ./internal/relay/...`
- **Build full**: `CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build -trimpath -ldflags='-s -w' -o crush .`
- **Test**: `go test -count=1 -timeout=60s ./internal/scheduler/...`

### Mobile (React Native + Expo)
- **Build release APK**: 
  ```bash
  cd mobile/crush_mobile/android
  export ANDROID_HOME=/home/junknet/Android/Sdk
  ./gradlew clean assembleRelease
  ```
  Output: `android/app/build/outputs/apk/release/app-release.apk` (~83 MB)

- **Install to device** (works on OPPO/Vivo national ROMs):
  ```bash
  adb -s $DEV push app-release.apk /data/local/tmp/crush.apk
  adb -s $DEV shell pm install -r -t /data/local/tmp/crush.apk
  ```

## Database Maintenance
- **SQLite Database**: `.crush/crush.db`
- **Auto-Pruning**: large binary data (Base64 images) >10KB are automatically pruned from the `messages` table on application startup if they are >24h old or already "seen" by the model. 
- **Tool**: `message.Service.Prune(ctx)` implements this logic.
- **Trace Logs**: JSONL files in `/home/junknet/.local/state/crush-dev/` contain task execution DAGs and performance metrics. Use PTC to analyze these for hotspots.

## Deprecated Components (Cleaned up in Commit b683801)
- **Deleted**: `internal/server/`, `internal/client/`, `internal/swagger/`.
- **Relay**: `WrapEvent` logic moved from `internal/server/events.go` to `internal/relay/events.go`. `internal/relay/` remains active.
- **CLI**: `crush server` subcommand removed from `internal/cmd/server.go`.
- **Build**: `swag` task removed from `Taskfile.yaml`.

## Android Release Keystore
- **Location**: `mobile/crush_mobile/android/app/release.keystore`
- **Credentials**: alias=`crush`, password=`crushrelease` (plaintext in private repo; use .gitignore for production)
- **Fingerprint**: `4d9bba18`
- **Validity**: 100 years (2026-05-25 to 2126-05-25)
- **Never committed to git** — regenerate if lost

## Scratch Files
- Files in `scratch/` currently cause `DuplicateDecl` errors in gopls because they use the same package name and main function. These should be ignored or cleaned up if they interfere with development.
