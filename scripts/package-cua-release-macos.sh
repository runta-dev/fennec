#!/bin/bash
# Builds the signed, notarized Fennec Computer Use release with
# cua/cmd/cua-dist: FennecCUA-V-macos-arm64.dmg (FennecCUA.app and an
# Applications shortcut to drag it to: what the website offers), the
# archives fennec-cua-V-macos-arm64.tar.gz (with Chromium) and
# fennec-cua-V-macos-arm64-core.tar.gz (without) for scripted installs, each
# archive with its .sha256, plus SHA256SUMS and provenance.json.
#
# It takes the same Apple credentials as package-release-macos.sh
# (APPLE_CERTIFICATE_BASE64, APPLE_CERTIFICATE_PASSWORD,
# APPLE_NOTARY_KEY_BASE64, APPLE_NOTARY_KEY_ID, APPLE_NOTARY_ISSUER_ID).
# --dry-run signs with the local identity (FENNEC_SIGN_IDENTITY, default
# "Fennec Local Development") and skips notarization. It never uploads or
# publishes; nothing in this script does.
#
# Environment:
#   FENNEC_CUA_VERSION      version (default 0.1.<Singapore MMDDHHMM>)
#   FENNEC_CUA_RELEASE_DIR  output directory (default: release-cua)
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)
dry_run=0
case "${1:-}" in
  --dry-run) dry_run=1 ;;
  '') ;;
  *) echo 'usage: package-cua-release-macos.sh [--dry-run]' >&2; exit 2 ;;
esac
[[ "$(uname -m)" == arm64 ]] || { echo 'An arm64 Mac is required' >&2; exit 1; }
if [[ $dry_run == 0 ]]; then
  for name in APPLE_CERTIFICATE_BASE64 APPLE_CERTIFICATE_PASSWORD APPLE_NOTARY_KEY_BASE64 APPLE_NOTARY_KEY_ID APPLE_NOTARY_ISSUER_ID; do
    [[ -n "${!name:-}" ]] || { echo "Missing $name" >&2; exit 1; }
  done
fi
version="${FENNEC_CUA_VERSION:-0.1.$(TZ=Asia/Singapore date +%m%d%H%M | sed 's/^0*//')}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "Version $version is not N.N.N" >&2; exit 1; }
out="${FENNEC_CUA_RELEASE_DIR:-release-cua}"
[[ "$out" = /* ]] || out="$root/$out"
umask 077
work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/fennec-cua-release.XXXXXX")
keychain="$work/signing.keychain-db"
original_keychains=()
original_default=""
mounted=""
cleanup() {
  [[ -z "$mounted" ]] || hdiutil detach "$mounted" >/dev/null 2>&1 || true
  if [[ $dry_run == 0 ]]; then
    security list-keychains -d user -s "${original_keychains[@]}" >/dev/null 2>&1 || true
    [[ -z "$original_default" ]] || security default-keychain -d user -s "$original_default" >/dev/null 2>&1 || true
    security delete-keychain "$keychain" >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

notarize_args=()
if [[ $dry_run == 0 ]]; then
  while IFS= read -r entry; do
    entry="${entry#*\"}"; entry="${entry%\"*}"
    [[ -z "$entry" ]] || original_keychains+=("$entry")
  done < <(security list-keychains -d user)
  original_default=$(security default-keychain -d user | sed 's/^ *"//; s/"$//')
  password=$(openssl rand -hex 24)
  printf '%s' "$APPLE_CERTIFICATE_BASE64" | base64 --decode > "$work/certificate.p12"
  printf '%s' "$APPLE_NOTARY_KEY_BASE64" | base64 --decode > "$work/notary.p8"
  security create-keychain -p "$password" "$keychain"
  security set-keychain-settings -lut 21600 "$keychain"
  security unlock-keychain -p "$password" "$keychain"
  security import "$work/certificate.p12" -k "$keychain" -P "$APPLE_CERTIFICATE_PASSWORD" -T /usr/bin/codesign -T /usr/bin/security >/dev/null
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$password" "$keychain" >/dev/null
  security list-keychains -d user -s "$keychain" "${original_keychains[@]}"
  # cua-dist submits with `notarytool --keychain-profile` and no --keychain,
  # so the profile goes into the default keychain for the run.
  security default-keychain -d user -s "$keychain"
  identity=$(security find-identity -v -p codesigning "$keychain" | awk '/"Developer ID Application:/{print $2;exit}')
  [[ "$identity" =~ ^[0-9A-Fa-f]{40}$ ]] || { echo 'Developer ID Application identity missing' >&2; exit 1; }
  xcrun notarytool store-credentials fennec-cua-release --key "$work/notary.p8" --key-id "$APPLE_NOTARY_KEY_ID" --issuer "$APPLE_NOTARY_ISSUER_ID" --keychain "$keychain" >/dev/null
  rm -f "$work/certificate.p12" "$work/notary.p8"
  notarize_args=(-notarize-profile fennec-cua-release)
else
  identity="${FENNEC_SIGN_IDENTITY:-Fennec Local Development}"
  [[ "$identity" != - ]] || { echo 'A dry run still needs a signing identity' >&2; exit 1; }
  keychain="${FENNEC_SIGN_KEYCHAIN:-$HOME/Library/Keychains/login.keychain-db}"
fi
export FENNEC_SIGN_KEYCHAIN="$keychain"

rm -rf "$out"
mkdir -p "$out"
cd cua
# One signed Chromium staging for the full archive.
go run ./cmd/cua-package -stage "$work/chromium-stage" -skip-build -identity "$identity" -keychain "$keychain"
go run ./cmd/cua-dist -out "$work/full" -version "$version" -chromium-stage "$work/chromium-stage" -identity "$identity" -keychain "$keychain" ${notarize_args[@]+"${notarize_args[@]}"}
go run ./cmd/cua-dist -out "$work/core" -version "$version" -no-chromium -identity "$identity" -keychain "$keychain" ${notarize_args[@]+"${notarize_args[@]}"}
cd "$root"
for d in full core; do
  cp "$work/$d"/*.tar.gz "$work/$d"/*.tar.gz.sha256 "$out/"
done
full="fennec-cua-$version-macos-arm64.tar.gz"
core="fennec-cua-$version-macos-arm64-core.tar.gz"
for f in "$full" "$full.sha256" "$core" "$core.sha256"; do
  [[ -s "$out/$f" ]] || { echo "Missing $f" >&2; exit 1; }
done
(cd "$out" && shasum -a 256 -c "$full.sha256" "$core.sha256" >/dev/null)

# The disk image holds the app from the verified full archive (notarized and
# stapled in a release) beside a link to /Applications.
app_name="FennecCUA.app"
dmg="FennecCUA-$version-macos-arm64.dmg"
mkdir "$work/dmg-src" "$work/dmg"
tar -xzf "$out/$full" -C "$work/dmg-src"
ditto "$work/dmg-src/fennec-cua-$version-macos-arm64/$app_name" "$work/dmg/$app_name"
ln -s /Applications "$work/dmg/Applications"
# The script runs with umask 077; the installed app must be readable by everyone.
/bin/chmod -R -P a+rX,go-w "$work/dmg/$app_name"
/bin/chmod 755 "$work/dmg"
# ULMO (LZMA), as Fennec's image: smaller download, macOS 10.15 or later to mount.
hdiutil create -volname FennecCUA -srcfolder "$work/dmg" -format ULMO -ov "$out/$dmg" >/dev/null
codesign --force --timestamp --keychain "$keychain" --sign "$identity" "$out/$dmg"
if [[ $dry_run == 0 ]]; then
  xcrun notarytool submit "$out/$dmg" --keychain-profile fennec-cua-release --wait --timeout 30m --output-format json > "$work/notarization-dmg.json"
  [[ "$(plutil -extract status raw -o - "$work/notarization-dmg.json")" == Accepted ]] || { echo 'Apple notarization of the disk image was not accepted' >&2; cat "$work/notarization-dmg.json" >&2; exit 1; }
  xcrun stapler staple "$out/$dmg"
  xcrun stapler validate "$out/$dmg"
fi
codesign --verify --strict "$out/$dmg"
# Check the app inside the shipped image, not the staging tree.
mkdir "$work/mounted"
hdiutil attach -readonly -nobrowse -mountpoint "$work/mounted" "$out/$dmg" >/dev/null
mounted="$work/mounted"
test -L "$mounted/Applications"
codesign --verify --deep --strict "$mounted/$app_name"
grep -q "<string>$version</string>" "$mounted/$app_name/Contents/Info.plist"
if [[ $dry_run == 0 ]]; then
  xcrun stapler validate "$mounted/$app_name"
  spctl --assess --type execute "$mounted/$app_name"
fi
hdiutil detach "$mounted" >/dev/null
mounted=""

source_sha=$(git rev-parse HEAD)
notarized=$([[ $dry_run == 0 ]] && echo true || echo false)
jq -n --arg version "$version" --arg sha "$source_sha" --argjson notarized "$notarized" \
  --arg run "${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-local}/actions/runs/${GITHUB_RUN_ID:-local}" \
  '{product:"Fennec Computer Use",version:$version,source_repository:"runta-dev/fennec",source_sha:$sha,build_run:$run,architecture:"arm64",notarized:$notarized}' > "$out/provenance.json"
(cd "$out" && shasum -a 256 "$dmg" "$full" "$core" provenance.json > SHA256SUMS)
chmod 0644 "$out"/*
echo "Fennec Computer Use $version in $out"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf '## Fennec Computer Use %s\n\nSource: `%s`\n\nDeveloper ID signing, Apple notarization, stapling, Gatekeeper assessment and readback passed for the disk image and the full and core archives.\n' "$version" "$source_sha" >> "$GITHUB_STEP_SUMMARY"
fi
