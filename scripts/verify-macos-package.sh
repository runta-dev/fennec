#!/bin/bash
set -euo pipefail
app="$1"
version="$2"
[[ -d "$app" ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Contents/Info.plist")" == com.runta.fennec ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$app/Contents/Info.plist")" == "$version" ]]
for binary in Fennec fennec-daemon fennec-credentials; do
  [[ -x "$app/Contents/MacOS/$binary" ]]
  lipo -archs "$app/Contents/MacOS/$binary" | grep -qw arm64
  codesign --verify --strict "$app/Contents/MacOS/$binary"
done
for arch in amd64 arm64; do
  [[ -s "$app/Contents/Resources/fennec-terminal-linux-$arch" ]]
  file "$app/Contents/Resources/fennec-terminal-linux-$arch" | grep -q ELF
done
[[ -s "$app/Contents/Resources/UI/index.html" ]]
[[ -s "$app/Contents/Resources/control-build-id" ]]
codesign --verify --deep --strict "$app"
"$app/Contents/MacOS/fennec-daemon" help >/dev/null
printf 'Verified Fennec %s (arm64)\n' "$version"
