#!/bin/bash
# Recover the exact signed Chromium from the last public release on fresh CI
# runners. Never re-sign an unchanged component or weaken the reuse check.
set -euo pipefail
cd "$(dirname "$0")/.."
identity="$1"
cache="$2"
work="$3"
repository="${FENNEC_RELEASE_REPOSITORY:-runta-dev/fennec}"
source_digest=$(cd cua && find cmd/cua-package internal/chromium internal/credits -name '*.go' ! -name '*_test.go' | LC_ALL=C sort | xargs shasum -a 256 | shasum -a 256 | cut -d ' ' -f 1)
key=$(printf '%s' "$identity:$source_digest" | shasum -a 256 | cut -d ' ' -f 1)
mkdir -p "$work" "$cache"
curl -fSL --retry 3 "https://api.github.com/repos/$repository/releases/latest" -o "$work/release.json"
manifest_url=$(jq -er '[.assets[] | select(.name | endswith("-components.json"))][0].browser_download_url' "$work/release.json")
curl -fSL --retry 3 "$manifest_url" -o "$work/components.json"
previous_key=$(jq -er '.components[] | select(.name == "chromium") | .key' "$work/components.json")
if [[ "$previous_key" != "$key" ]]; then
  echo 'Chromium inputs changed; a new signed component will be built.'
  exit 0
fi
tree=$(jq -er '.components[] | select(.name == "chromium") | .tree' "$work/components.json")
stage="$cache/$key"
if [[ -f "$stage/MANIFEST.json" ]] && [[ "$(go run ./cmd/release-components tree "$stage/Chromium.app")" == "$tree" ]]; then
  echo 'Signed Chromium cache matches the previous release.'
  exit 0
fi
asset=$(jq -er '.components[] | select(.name == "chromium") | .asset' "$work/components.json")
digest=$(jq -er '.components[] | select(.name == "chromium") | .sha256' "$work/components.json")
[[ "$digest" =~ ^[a-f0-9]{64}$ ]]
url=$(jq -er --arg name "$asset" '.assets[] | select(.name == $name) | .browser_download_url' "$work/release.json")
curl -fSL --retry 3 "$url" -o "$work/chromium.zip"
printf '%s  %s\n' "$digest" "$work/chromium.zip" | shasum -a 256 -c -
staging=$(mktemp -d "$cache/.restore-XXXXXX")
trap 'rm -rf "$staging"' EXIT
ditto -x -k "$work/chromium.zip" "$staging"
codesign --verify --deep --strict "$staging/Chromium.app"
codesign -d --extract-certificates="$work/cert" "$staging/Chromium.app"
certificate=$(shasum -a 1 "$work/cert0" | cut -d ' ' -f 1 | tr '[:lower:]' '[:upper:]')
[[ "$certificate" == "$identity" ]] || { echo 'Previous Chromium uses a different signing certificate' >&2; exit 1; }
[[ "$(go run ./cmd/release-components tree "$staging/Chromium.app")" == "$tree" ]] || { echo 'Previous Chromium tree does not match its manifest' >&2; exit 1; }
mkdir "$staging/licenses"
for name in Chromium-LICENSE.txt Chromium-credits.html Chromium-credits.txt; do
  cp "$staging/Chromium.app/Contents/Resources/$name" "$staging/licenses/$name"
done
cp "$work/components.json" "$staging/MANIFEST.json"
if [[ -e "$stage" ]]; then mv "$stage" "$work/replaced-cache"; fi
mv "$staging" "$stage"
echo "Restored signed Chromium $tree from the previous release."
