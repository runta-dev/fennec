#!/bin/bash
set -euo pipefail
app="$1"
version="$2"
[[ -d "$app" ]]
app=$(cd "$app" && pwd)
go run ./cmd/check-bundle-permissions "$app"
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$app/Contents/Info.plist")" == com.runta.fennec ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundlePackageType' "$app/Contents/Info.plist")" == APPL ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print LSMinimumSystemVersion' "$app/Contents/Info.plist")" == 13.0 ]]
[[ "$(/usr/libexec/PlistBuddy -c 'Print CFBundleShortVersionString' "$app/Contents/Info.plist")" == "$version" ]]
# Fennec Terminal (FENNEC_BUILD_DESKTOP=1) is all or nothing; releases set
# FENNEC_REQUIRE_DESKTOP=1 so a bundle without it cannot ship.
if [[ "${FENNEC_REQUIRE_DESKTOP:-0}" == 1 && ! -e "$app/Contents/MacOS/fennec-desktop" ]]; then
  echo 'Fennec Terminal (Contents/MacOS/fennec-desktop) is missing' >&2; exit 1
fi
if [[ -e "$app/Contents/MacOS/fennec-desktop" ]]; then
  [[ -x "$app/Contents/MacOS/hc" && -x "$app/Contents/Resources/fennec-hc" && -x "$app/Contents/Resources/fennec-hc-local" ]]
  for binary in fennec-desktop hc; do
    lipo -archs "$app/Contents/MacOS/$binary" | grep -qw arm64
    codesign --verify --strict "$app/Contents/MacOS/$binary"
  done
else
  [[ ! -e "$app/Contents/MacOS/hc" && ! -e "$app/Contents/Resources/fennec-hc" && ! -e "$app/Contents/Resources/fennec-hc-local" ]]
fi
for binary in Fennec fennec-daemon fennec-credentials fennec-clipboard; do
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
[[ -s "$app/Contents/Resources/UI/translations.json" ]]
[[ -s "$app/Contents/Resources/UI/locale-data.js" ]]
[[ -s "$app/Contents/Resources/control-build-id" ]]
for notice in THIRD_PARTY_NOTICES.md magpie-MIT.txt; do
  [[ -s "$app/Contents/Resources/licenses/$notice" ]]
done
# Fennec CUA and Chromium ship separately, never inside Fennec.app.
[[ ! -e "$app/Contents/Helpers" && ! -e "$app/Contents/MacOS/fennec-cua" ]]
# components.json (read by incremental updates) must describe this bundle.
go run ./cmd/release-components verify -app "$app"
codesign --verify --deep --strict "$app"
"$app/Contents/MacOS/fennec-daemon" help >/dev/null
"$app/Contents/MacOS/fen" install --check | jq -e '(.source | ascii_downcase | endswith(".app")) and (.application | ascii_downcase | endswith(".app")) and (.required | type == "boolean")' >/dev/null
printf 'Verified Fennec %s (arm64)\n' "$version"
