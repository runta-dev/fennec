#!/bin/bash
set -euo pipefail
app="$1"
version="$2"
[[ -d "$app" ]]
go run ./cmd/check-bundle-permissions "$app"
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Contents/Info.plist")" == com.runta.fennec ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundlePackageType' "$app/Contents/Info.plist")" == APPL ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print LSMinimumSystemVersion' "$app/Contents/Info.plist")" == 13.0 ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$app/Contents/Info.plist")" == "$version" ]]
for binary in Fennec fennec-daemon fennec-credentials; do
  [[ -x "$app/Contents/MacOS/$binary" ]]
  lipo -archs "$app/Contents/MacOS/$binary" | grep -qw arm64
  codesign --verify --strict "$app/Contents/MacOS/$binary"
  minimum=$(xcrun vtool -show-build "$app/Contents/MacOS/$binary" | awk '$1 == "minos" || $1 == "version" {print $2;exit}')
  awk -v version="$minimum" 'BEGIN { n=split(version,v,"."); exit !(n>=2 && (v[1]<13 || (v[1]==13 && v[2]==0))) }' || { echo "$binary requires macOS $minimum; expected macOS 13.0 or earlier" >&2; exit 1; }
done
for arch in amd64 arm64; do
  [[ -s "$app/Contents/Resources/fennec-terminal-linux-$arch" ]]
  file "$app/Contents/Resources/fennec-terminal-linux-$arch" | grep -q ELF
done
[[ -s "$app/Contents/Resources/UI/index.html" ]]
[[ -s "$app/Contents/Resources/control-build-id" ]]
codesign --verify --deep --strict "$app"
"$app/Contents/MacOS/fennec-daemon" help >/dev/null
"$app/Contents/MacOS/fen" install --check | jq -e '(.source | ascii_downcase | endswith(".app")) and (.application | ascii_downcase | endswith(".app")) and (.required | type == "boolean")' >/dev/null
printf 'Verified Fennec %s (arm64)\n' "$version"
