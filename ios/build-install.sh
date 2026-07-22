#!/usr/bin/env bash
# Build, sign, install, and launch Raven on a connected iPhone — one command.
#
#   ./build-install.sh
#
# Everything machine-specific (device, bundle id, Apple team, signing key) lives
# in build.local.sh, which is gitignored. Copy build.local.sh.example, fill it in
# once, and this is the only thing you run afterwards.
#
# Why a script and not a documented command: the invocation is ~15 flags, and
# getting PRODUCT_BUNDLE_IDENTIFIER wrong installs a *second* app instead of
# updating the one on your phone. The script always passes your real bundle id,
# so the repo can keep a generic default with no footgun.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

[ -f build.local.sh ] || {
  echo "missing build.local.sh — copy build.local.sh.example and fill it in" >&2; exit 1; }
[ -f raven-host.local ] || {
  echo "missing raven-host.local — copy raven-host.local.example and set your Mac's Tailscale IP" >&2; exit 1; }

. ./build.local.sh
RAVEN_HOST="$(cat raven-host.local)"

: "${RAVEN_DEVICE_ID:?set RAVEN_DEVICE_ID in build.local.sh (find it: xcrun devicectl list devices)}"
: "${RAVEN_BUNDLE_ID:?set RAVEN_BUNDLE_ID in build.local.sh}"
: "${RAVEN_TEAM_ID:?set RAVEN_TEAM_ID in build.local.sh}"
: "${RAVEN_KEY_PATH:?set RAVEN_KEY_PATH in build.local.sh}"
: "${RAVEN_KEY_ID:?set RAVEN_KEY_ID in build.local.sh}"
: "${RAVEN_ISSUER_ID:?set RAVEN_ISSUER_ID in build.local.sh}"
[ -f "$RAVEN_KEY_PATH" ] || { echo "signing key not found: $RAVEN_KEY_PATH" >&2; exit 1; }

DD="$PWD/build/DerivedData"
APP="$DD/Build/Products/Release-iphoneos/Ear.app"

echo "==> building Ear.app   host=$RAVEN_HOST  bundle=$RAVEN_BUNDLE_ID"
xcodebuild \
  -project Ear.xcodeproj -scheme Ear -configuration Release \
  -destination "id=$RAVEN_DEVICE_ID" -derivedDataPath "$DD" \
  RAVEN_HOST="$RAVEN_HOST" \
  DEVELOPMENT_TEAM="$RAVEN_TEAM_ID" \
  PRODUCT_BUNDLE_IDENTIFIER="$RAVEN_BUNDLE_ID" \
  CODE_SIGN_STYLE=Automatic \
  -allowProvisioningUpdates -allowProvisioningDeviceRegistration \
  -authenticationKeyPath "$RAVEN_KEY_PATH" \
  -authenticationKeyID "$RAVEN_KEY_ID" \
  -authenticationKeyIssuerID "$RAVEN_ISSUER_ID" \
  clean build >/dev/null

echo "==> verifying signature"
codesign --verify --deep --strict "$APP"
echo "    baked-in host: $(plutil -extract RavenHost raw "$APP/Info.plist")"

echo "==> installing on device"
xcrun devicectl device install app --device "$RAVEN_DEVICE_ID" "$APP" >/dev/null

echo "==> launching"
xcrun devicectl device process launch --device "$RAVEN_DEVICE_ID" "$RAVEN_BUNDLE_ID" >/dev/null 2>&1 \
  || echo "    launch failed (phone locked?) — the app IS installed; just open it"

echo "==> done"
