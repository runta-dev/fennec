#!/bin/sh
set -eu
# Application contents are public software, even when the release caller uses
# umask 077 to protect its signing material outside the bundle.
umask 022
cd "$(dirname "$0")/.."
app="${FENNEC_BUILD_DIR:-$PWD/build}/Fennec.app"
identity="${FENNEC_SIGN_IDENTITY:-Fennec Local Development}"
deployment_target=13.0
swift_target="$(uname -m)-apple-macos$deployment_target"
sign_keychain="${FENNEC_SIGN_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
# One build timestamp shared by bundle metadata and every version label.
build_stamp=$(TZ=Asia/Singapore date +%m%d%H%M | sed 's/^0*//')
app_version="${FENNEC_VERSION:-0.2.$build_stamp}"
build_number="${FENNEC_BUILD_NUMBER:-1}"
printf '%s\n' "$app_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo 'Invalid FENNEC_VERSION' >&2; exit 1; }
printf '%s\n' "$build_number" | grep -Eq '^[0-9]+$' || { echo 'Invalid FENNEC_BUILD_NUMBER' >&2; exit 1; }
if [ "${FENNEC_RELEASE:-0}" = 1 ] && [ "$identity" = - ]; then
  echo "Release builds require FENNEC_SIGN_IDENTITY (Developer ID Application)." >&2
  exit 1
fi
if [ "$identity" != - ]; then
  # Resolve to one valid identity before touching an existing application.
  identity=$(security find-identity "$sign_keychain" | go run ./cmd/signing-identity "$identity")
fi
sign_artifact() {
  artifact="$1"
  shift
  if [ "$identity" = - ]; then
    codesign --force --sign - "$@" "$artifact"
  else
    codesign --force --options runtime --timestamp --keychain "$sign_keychain" --sign "$identity" "$@" "$artifact"
  fi
}
# Always build the daemon from the same source snapshot as the native shell.
# Alternatively accept an explicit cross-built artifact from the dev machine.
mkdir -p "$app/Contents/MacOS"
helper_cache="$PWD/build/credential-helper-cache"
helper_source=$(shasum -a 256 native/macos/CredentialHelper.swift | cut -d ' ' -f 1)
helper_key=$(printf '%s' "$identity:$helper_source:$swift_target" | shasum -a 256 | cut -d ' ' -f 1)
helper="$helper_cache/$helper_key/fennec-credentials"
if [ ! -f "$helper" ]; then
  mkdir -p "$helper_cache/$helper_key"
  swiftc -target "$swift_target" native/macos/CredentialHelper.swift -o "$helper" -framework Security
  sign_artifact "$helper"
fi
codesign --verify --strict "$helper"
cp "$helper" "$app/Contents/MacOS/fennec-credentials"
mkdir -p "$app/Contents/Resources"
go run ./cmd/fennec-terminal protocol-version > "$app/Contents/Resources/terminal-protocol-version"
for remote_arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$remote_arch" go build -trimpath -o "$app/Contents/Resources/fennec-terminal-linux-$remote_arch" ./cmd/fennec-terminal
done
if [ -n "${FENNEC_DAEMON_BINARY:-}" ]; then
  test -f "$FENNEC_DAEMON_BINARY"
  cp "$FENNEC_DAEMON_BINARY" "$app/Contents/MacOS/fen"
elif [ -f "$app/Contents/MacOS/fennec-daemon" ]; then
  echo "Set FENNEC_DAEMON_BINARY to a freshly cross-built daemon; refusing silent reuse." >&2
  exit 1
else
  echo "FENNEC_DAEMON_BINARY is required to package a complete application." >&2
  exit 1
fi
ln -sfn fen "$app/Contents/MacOS/fennec-daemon"
# Removed UI features must not survive in a reused staging bundle.
rm -rf "$app/Contents/Resources/UI"
mkdir -p "$app/Contents/Resources/UI"
cp -R native/macos/UI/. "$app/Contents/Resources/UI/"
english_source="$(dirname "$app")/FennecEnglish.swift"
go run ./cmd/build-locales native/macos/UI/translations.json "$app/Contents/Resources/UI/locale-data.js" "$english_source"
swiftc -target "$swift_target" native/macos/Fennec.swift native/macos/ControlClient.swift native/macos/PlatformAdapter.swift native/macos/TerminalPTY.swift native/macos/PopupPlacement.swift native/macos/Localization.swift "$english_source" -o "$app/Contents/MacOS/Fennec" -framework AppKit -framework WebKit -framework Security
cp native/macos/AppIcon.icns "$app/Contents/Resources/AppIcon.icns"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.runta.fennec</string>
<key>CFBundleName</key><string>Fennec</string>
<key>CFBundleIconFile</key><string>AppIcon</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>LSMinimumSystemVersion</key><string>$deployment_target</string>
<key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
<key>CFBundleExecutable</key><string>Fennec</string>
<key>CFBundleVersion</key><string>$build_number</string>
<key>CFBundleShortVersionString</key><string>$app_version</string>
<key>LSUIElement</key><true/>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
mkdir -p "$app/Contents/Resources/licenses"
cp docs/licenses/magpie-MIT.txt "$app/Contents/Resources/licenses/"
/bin/chmod -R -P a+rX,go-w "$app"
if [ -f "$app/Contents/MacOS/fennec-daemon" ]; then
  # Keep the stable helper byte-identical: the daemon satisfies the same
  # certificate+identifier requirement already used for the native shell.
  sign_artifact "$app/Contents/MacOS/fen" --identifier com.runta.fennec
fi
shasum -a 256 "$app/Contents/MacOS/fen" | cut -d ' ' -f 1 > "$app/Contents/Resources/control-build-id"
go run ./cmd/check-control-contract "$app" native/macos/Fennec.swift native/macos/ControlClient.swift
sign_artifact "$app"
printf '%s\n' "$app"
