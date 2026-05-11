#!/usr/bin/env bash
# Build a macOS .app bundle for the chess engine.
#
# Usage:
#   scripts/build-app.sh                   # build for the host arch
#   ARCH=universal scripts/build-app.sh    # universal binary (amd64 + arm64)
#   ARCH=amd64     scripts/build-app.sh    # force x86-64
#   ARCH=arm64     scripts/build-app.sh    # force Apple Silicon
#
# Output: ./build/Chess.app

set -euo pipefail

cd "$(dirname "$0")/.."

APP_NAME="Chess"
BUILD_DIR="${BUILD_DIR:-build}"
APP_DIR="${BUILD_DIR}/${APP_NAME}.app"
EXE_DIR="${APP_DIR}/Contents/MacOS"
RES_DIR="${APP_DIR}/Contents/Resources"
EXE="${EXE_DIR}/${APP_NAME}"

if [[ "$(uname)" != "Darwin" ]]; then
  echo "warning: building a .app on a non-Darwin host; the binary will only run on macOS" >&2
fi

ARCH="${ARCH:-$(uname -m)}"
case "${ARCH}" in
  x86_64|amd64) GOARCHES=(amd64) ;;
  arm64|aarch64) GOARCHES=(arm64) ;;
  universal)     GOARCHES=(amd64 arm64) ;;
  *) echo "unsupported ARCH=${ARCH}" >&2; exit 2 ;;
esac

rm -rf "${APP_DIR}"
mkdir -p "${EXE_DIR}" "${RES_DIR}"

# Build frontend assets
(cd "$(dirname "$0")/../frontend" && npm install && npm run build)

if [[ "${#GOARCHES[@]}" -eq 1 ]]; then
  GOOS=darwin GOARCH="${GOARCHES[0]}" CGO_ENABLED=0 \
    go build -trimpath -ldflags='-s -w' -o "${EXE}" .
else
  TMP="$(mktemp -d)"
  trap 'rm -rf "${TMP}"' EXIT
  for a in "${GOARCHES[@]}"; do
    GOOS=darwin GOARCH="${a}" CGO_ENABLED=0 \
      go build -trimpath -ldflags='-s -w' -o "${TMP}/${APP_NAME}-${a}" .
  done
  if ! command -v lipo >/dev/null; then
    echo "lipo not found (install Xcode Command Line Tools for universal builds)" >&2
    exit 2
  fi
  lipo -create -output "${EXE}" "${TMP}/${APP_NAME}-amd64" "${TMP}/${APP_NAME}-arm64"
fi

cat > "${APP_DIR}/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIdentifier</key>
    <string>local.chess</string>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>${APP_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleVersion</key>
    <string>1.0</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0</string>
    <key>LSMinimumSystemVersion</key>
    <string>10.15</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <!-- Run as a UI-less agent: no Dock icon, no menu bar. The Go
         binary never registers an NSWindow, so without this macOS
         would bounce the Dock icon forever waiting for launch to
         finish. The browser tab is the visible UI; closing it
         auto-quits the app via the heartbeat. -->
    <key>LSUIElement</key>
    <true/>
</dict>
</plist>
PLIST

# Strip macOS Gatekeeper quarantine so a freshly-built bundle opens
# without "unidentified developer" warnings on the build machine.
xattr -dr com.apple.quarantine "${APP_DIR}" 2>/dev/null || true

echo "Built ${APP_DIR}"
echo "Open with:  open ${APP_DIR}"
echo "Uninstall:  rm -rf ${APP_DIR}   (drag to Trash from Finder)"
