#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ "$(uname -m)" == arm64 ]] || { echo 'An arm64 Mac is required' >&2; exit 1; }
for name in APPLE_CERTIFICATE_BASE64 APPLE_CERTIFICATE_PASSWORD APPLE_NOTARY_KEY_BASE64 APPLE_NOTARY_KEY_ID APPLE_NOTARY_ISSUER_ID; do
  [[ -n "${!name:-}" ]] || { echo "Missing $name" >&2; exit 1; }
done
umask 077
release_work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/fennec-release.XXXXXX")
release_keychain="$release_work/signing.keychain-db"
release_password=$(openssl rand -hex 24)
original_keychains=()
while IFS= read -r entry; do
  entry="${entry#*\"}"; entry="${entry%\"*}"
  [[ -z "$entry" ]] || original_keychains+=("$entry")
done < <(security list-keychains -d user)
cleanup() {
  security list-keychains -d user -s "${original_keychains[@]}" >/dev/null 2>&1 || true
  security delete-keychain "$release_keychain" >/dev/null 2>&1 || true
  rm -rf "$release_work"
}
trap cleanup EXIT
printf '%s' "$APPLE_CERTIFICATE_BASE64" | base64 --decode > "$release_work/certificate.p12"
printf '%s' "$APPLE_NOTARY_KEY_BASE64" | base64 --decode > "$release_work/notary.p8"
security create-keychain -p "$release_password" "$release_keychain"
security set-keychain-settings -lut 21600 "$release_keychain"
security unlock-keychain -p "$release_password" "$release_keychain"
security import "$release_work/certificate.p12" -k "$release_keychain" -P "$APPLE_CERTIFICATE_PASSWORD" -T /usr/bin/codesign -T /usr/bin/security >/dev/null
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$release_password" "$release_keychain" >/dev/null
security list-keychains -d user -s "$release_keychain" "${original_keychains[@]}"
identity=$(security find-identity -v -p codesigning "$release_keychain" | awk '/"Developer ID Application:/{print $2;exit}')
[[ "$identity" =~ ^[0-9A-Fa-f]{40}$ ]] || { echo 'Developer ID Application identity missing' >&2; exit 1; }
version="${FENNEC_VERSION:-0.2.$(TZ=Asia/Singapore date +%m%d%H%M | sed 's/^0*//')}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid version' >&2; exit 1; }
export FENNEC_VERSION="$version" FENNEC_SIGN_IDENTITY="$identity" FENNEC_SIGN_KEYCHAIN="$release_keychain" FENNEC_RELEASE=1
export FENNEC_BUILD_DIR="$release_work/build" FENNEC_DAEMON_BINARY="$release_work/fennec-daemon"
go build -trimpath -o "$FENNEC_DAEMON_BINARY" .
./scripts/build-macos.sh
app="$FENNEC_BUILD_DIR/Fennec.app"
./scripts/verify-macos-package.sh "$app" "$version"
codesign -dvv "$app" 2>&1 | grep '^Authority=Developer ID Application:'
mkdir -p release
notarize() {
  local file="$1" report="$2"
  xcrun notarytool submit "$file" --key "$release_work/notary.p8" --key-id "$APPLE_NOTARY_KEY_ID" --issuer "$APPLE_NOTARY_ISSUER_ID" --wait --timeout 30m --output-format json > "$report"
  [[ "$(plutil -extract status raw -o - "$report")" == Accepted ]] || { echo 'Apple notarization was not accepted' >&2; cat "$report" >&2; exit 1; }
}
ditto -c -k --sequesterRsrc --keepParent "$app" "$release_work/notarize.zip"
notarize "$release_work/notarize.zip" release/notarization-app.json
xcrun stapler staple "$app"
xcrun stapler validate "$app"
spctl --assess --type execute --verbose=2 "$app"
ditto -c -k --sequesterRsrc --keepParent "$app" "release/Fennec-$version-macos-arm64.zip"
mkdir "$release_work/dmg"
ditto "$app" "$release_work/dmg/Fennec.app"
ln -s /Applications "$release_work/dmg/Applications"
dmg="release/Fennec-$version-macos-arm64.dmg"
hdiutil create -volname Fennec -srcfolder "$release_work/dmg" -format UDZO -ov "$dmg" >/dev/null
codesign --force --timestamp --keychain "$release_keychain" --sign "$identity" "$dmg"
notarize "$dmg" release/notarization-dmg.json
xcrun stapler staple "$dmg"
xcrun stapler validate "$dmg"
codesign --verify --strict "$dmg"
# Verify the shipped ZIP, not just the build directory.
ditto -x -k "release/Fennec-$version-macos-arm64.zip" "$release_work/unpacked"
./scripts/verify-macos-package.sh "$release_work/unpacked/Fennec.app" "$version"
xcrun stapler validate "$release_work/unpacked/Fennec.app"
spctl --assess --type execute "$release_work/unpacked/Fennec.app"
source_sha=$(git rev-parse HEAD)
jq -n --arg version "$version" --arg sha "$source_sha" --arg run "${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-runta-dev/fennec}/actions/runs/${GITHUB_RUN_ID:-local}" '{product:"Fennec",version:$version,source_repository:"runta-dev/fennec",source_sha:$sha,build_run:$run,architecture:"arm64",notarized:true}' > release/provenance.json
(cd release && shasum -a 256 *.zip *.dmg provenance.json notarization-*.json > SHA256SUMS)
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf '## Fennec %s\n\nSource: `%s`\n\nDeveloper ID signing, Apple notarization, stapling, Gatekeeper assessment, and ZIP readback passed.\n' "$version" "$source_sha" >> "$GITHUB_STEP_SUMMARY"
fi
