#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    cat <<'EOF'
Usage:
  CRUSH_MOBILE_CONFIRM_RELEASE=1 npm run release:android:private

Environment:
  CRUSH_MOBILE_GITHUB_REPO       GitHub owner/repo. Default: junknet/crush
  CRUSH_MOBILE_RELEASE_CHANNEL   Release tag prefix. Default: crush-mobile
  CRUSH_MOBILE_ANDROID_VERSION_CODE
                                  Android versionCode consumed by app.config.js.

This command builds the Android release APK and creates or updates a GitHub
pre-release tagged <channel>-v<package.json version>.
EOF
    exit 0
fi

if [[ "${CRUSH_MOBILE_CONFIRM_RELEASE:-}" != "1" ]]; then
    echo "Refusing to publish. Set CRUSH_MOBILE_CONFIRM_RELEASE=1 to write a GitHub release." >&2
    exit 2
fi

VERSION="$(node -p "require('./package.json').version")"
REPO="${CRUSH_MOBILE_GITHUB_REPO:-junknet/crush}"
CHANNEL="${CRUSH_MOBILE_RELEASE_CHANNEL:-crush-mobile}"
TAG="${CHANNEL}-v${VERSION}"
ASSET_NAME="crush-mobile-android-v${VERSION}.apk"
APK_PATH="$ROOT_DIR/android/app/build/outputs/apk/release/app-release.apk"
DIST_DIR="$ROOT_DIR/dist"
DIST_APK="$DIST_DIR/$ASSET_NAME"

mkdir -p "$DIST_DIR"

(cd android && ./gradlew assembleRelease)
cp "$APK_PATH" "$DIST_APK"

NOTES="channel=${CHANNEL}
version=${VERSION}
asset=${ASSET_NAME}"

if gh release view "$TAG" --repo "$REPO" >/dev/null 2>&1; then
    gh release upload "$TAG" "$DIST_APK#$ASSET_NAME" --repo "$REPO" --clobber
    gh release edit "$TAG" \
        --repo "$REPO" \
        --title "Crush Mobile ${VERSION}" \
        --notes "$NOTES" \
        --prerelease \
        --latest=false \
        --draft=false
else
    gh release create "$TAG" "$DIST_APK#$ASSET_NAME" \
        --repo "$REPO" \
        --title "Crush Mobile ${VERSION}" \
        --notes "$NOTES" \
        --prerelease \
        --latest=false
fi

echo "Published $TAG to $REPO with $ASSET_NAME"
