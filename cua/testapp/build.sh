#!/bin/bash
# Builds CUAFixture.app without an Xcode project: compile with swiftc into a
# staging directory, assemble the bundle, sign it ad hoc, verify the
# signature, then move it into place. Prints the bundle path on the last line.
#
# Usage: build.sh [output-dir]   (default: cua/testapp/build)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
out="${1:-$here/build}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
stage="$(mktemp -d "$out/.stage.XXXXXX")"
trap 'rm -rf "$stage"' EXIT

app="$stage/CUAFixture.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$here/Info.plist" "$app/Contents/Info.plist"
printf 'APPL????' > "$app/Contents/PkgInfo"

arch="$(uname -m)"
xcrun swiftc \
  -O \
  -swift-version 5 \
  -target "$arch-apple-macos14.0" \
  -sdk "$(xcrun --sdk macosx --show-sdk-path)" \
  -framework AppKit -framework WebKit \
  -module-name CUAFixture \
  -o "$app/Contents/MacOS/CUAFixture" \
  "$here"/Sources/*.swift

# CUACover.app: the same binary under another name and bundle id, run by
# the cover scene as a second app whose window covers the fixture.
helper="$app/Contents/Helpers/CUACover.app"
mkdir -p "$helper/Contents/MacOS"
cp "$app/Contents/MacOS/CUAFixture" "$helper/Contents/MacOS/CUACover"
cp "$here/Info.plist" "$helper/Contents/Info.plist"
plist="$helper/Contents/Info.plist"
plutil -replace CFBundleExecutable -string CUACover "$plist"
plutil -replace CFBundleIdentifier -string com.runta.fennec.cua-fixture.cover "$plist"
plutil -replace CFBundleName -string CUACover "$plist"
plutil -replace CFBundleDisplayName -string CUACover "$plist"
plutil -remove CFBundleURLTypes "$plist"
plutil -remove CFBundleDocumentTypes "$plist"
plutil -insert LSUIElement -bool YES "$plist"
codesign --force --sign - --timestamp=none "$helper" >/dev/null 2>&1

codesign --force --sign - --timestamp=none "$app" >/dev/null 2>&1
codesign --verify --strict --verbose=1 "$app"

rm -rf "$out/CUAFixture.app"
mv "$app" "$out/CUAFixture.app"
codesign --verify --strict "$out/CUAFixture.app"
echo "$out/CUAFixture.app"
