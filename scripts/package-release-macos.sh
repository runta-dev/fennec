#!/bin/bash
# Builds the signed, notarized Fennec release: the full ZIP and ULMO DMG for
# first installs, plus the incremental-update components (core archive,
# content-addressed Chromium archive, component manifest).
#
# --dry-run builds the app and every update archive locally with the local
# signing identity (FENNEC_SIGN_IDENTITY, default "Fennec Local Development")
# and skips the release keychain, notarization, stapling, Gatekeeper and the
# DMG. It never uploads or publishes; nothing in this script does.
#
# Environment:
#   FENNEC_RELEASE_DIR          output directory (default: release)
#   FENNEC_CUA_CACHE            signed Chromium cache. Release builds must keep
#                               it between runs: a re-signed Chromium changes
#                               its tree digest and every user downloads it
#                               again (default: ~/Library/Caches/fennec/release-cua-cache)
#   FENNEC_PREVIOUS_COMPONENTS  previous release's *-components.json; the build
#                               fails if Chromium's inputs are unchanged but
#                               its content is not
set -euo pipefail
cd "$(dirname "$0")/.."
dry_run=0
case "${1:-}" in
  --dry-run) dry_run=1 ;;
  '') ;;
  *) echo 'usage: package-release-macos.sh [--dry-run]' >&2; exit 2 ;;
esac
[[ "$(uname -m)" == arm64 ]] || { echo 'An arm64 Mac is required' >&2; exit 1; }
if [[ $dry_run == 0 ]]; then
  for name in APPLE_CERTIFICATE_BASE64 APPLE_CERTIFICATE_PASSWORD APPLE_NOTARY_KEY_BASE64 APPLE_NOTARY_KEY_ID APPLE_NOTARY_ISSUER_ID; do
    [[ -n "${!name:-}" ]] || { echo "Missing $name" >&2; exit 1; }
  done
fi
umask 077
release_dir="${FENNEC_RELEASE_DIR:-release}"
release_work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/fennec-release.XXXXXX")
release_keychain="$release_work/signing.keychain-db"
release_mount=""
original_keychains=()
cleanup() {
  local mount_cleanup_ok=true
  if [[ -n "$release_mount" ]]; then
    hdiutil detach "$release_mount" >/dev/null 2>&1 || mount_cleanup_ok=false
  fi
  if [[ $dry_run == 0 ]]; then
    security list-keychains -d user -s "${original_keychains[@]}" >/dev/null 2>&1 || true
    security delete-keychain "$release_keychain" >/dev/null 2>&1 || true
    rm -f "$release_work/certificate.p12" "$release_work/notary.p8" "$release_keychain"
  fi
  if [[ "$mount_cleanup_ok" != true ]]; then echo 'Unable to detach release validation image' >&2; return 1; fi
  rm -rf "$release_work"
}
trap cleanup EXIT
if [[ $dry_run == 0 ]]; then
  while IFS= read -r entry; do
    entry="${entry#*\"}"; entry="${entry%\"*}"
    [[ -z "$entry" ]] || original_keychains+=("$entry")
  done < <(security list-keychains -d user)
  release_password=$(openssl rand -hex 24)
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
  export FENNEC_SIGN_KEYCHAIN="$release_keychain"
  # One signed Chromium per pin, packager and identity, kept across releases
  # so its bytes (and tree digest) stay put and updates reuse it.
  export FENNEC_CUA_CACHE="${FENNEC_CUA_CACHE:-$HOME/Library/Caches/fennec/release-cua-cache}"
else
  identity="${FENNEC_SIGN_IDENTITY:-Fennec Local Development}"
  [[ "$identity" != - ]] || { echo 'A dry run still needs a signing identity' >&2; exit 1; }
fi
version="${FENNEC_VERSION:-0.3.$(TZ=Asia/Singapore date +%m%d%H%M | sed 's/^0*//')}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid version' >&2; exit 1; }
export FENNEC_VERSION="$version" FENNEC_SIGN_IDENTITY="$identity" FENNEC_RELEASE=1
export FENNEC_BUILD_DIR="$release_work/build" FENNEC_DAEMON_BINARY="$release_work/fennec-daemon"
if [[ $dry_run == 0 ]]; then
  ./scripts/restore-release-cua-cache.sh "$identity" "$FENNEC_CUA_CACHE" "$release_work/previous"
  export FENNEC_PREVIOUS_COMPONENTS="${FENNEC_PREVIOUS_COMPONENTS:-$release_work/previous/components.json}"
fi
go build -trimpath -o "$FENNEC_DAEMON_BINARY" .
./scripts/build-macos.sh
app="$FENNEC_BUILD_DIR/Fennec.app"
./scripts/verify-macos-package.sh "$app" "$version"
mkdir -p "$release_dir"
if [[ $dry_run == 0 ]]; then
  codesign -dvv "$app" 2>&1 | grep '^Authority=Developer ID Application:'
  notarize() {
    local file="$1" report="$2"
    xcrun notarytool submit "$file" --key "$release_work/notary.p8" --key-id "$APPLE_NOTARY_KEY_ID" --issuer "$APPLE_NOTARY_ISSUER_ID" --wait --timeout 30m --output-format json > "$report"
    [[ "$(plutil -extract status raw -o - "$report")" == Accepted ]] || { echo 'Apple notarization was not accepted' >&2; cat "$report" >&2; exit 1; }
  }
  ditto -c -k --sequesterRsrc --keepParent "$app" "$release_work/notarize.zip"
  notarize "$release_work/notarize.zip" "$release_dir/notarization-app.json"
  xcrun stapler staple "$app"
fi
/bin/chmod -R -P a+rX,go-w "$app"
if [[ $dry_run == 0 ]]; then
  xcrun stapler validate "$app"
  spctl --assess --type execute --verbose=2 "$app"
fi
ditto -c -k --sequesterRsrc --keepParent "$app" "$release_dir/Fennec-$version-macos-arm64.zip"
# Incremental updates: the stapled app split into core plus the Chromium
# component, then reassembled from those archives and compared with the app.
previous_components="${FENNEC_PREVIOUS_COMPONENTS:-}"
# The latest published release is the one users update from. It lives in
# the public Fennec repository, not the repository running the build (CI
# runs in another one), and is read without credentials.
release_repository="${FENNEC_RELEASE_REPOSITORY:-runta-dev/fennec}"
if [[ -z "$previous_components" && $dry_run == 0 ]]; then
  mkdir -p "$release_work/previous"
  previous_url=$(curl -fsSL "https://api.github.com/repos/$release_repository/releases/latest" | jq -r '[.assets[] | select(.name | endswith("-components.json")) | .browser_download_url][0] // empty' || true)
  if [[ -n "$previous_url" ]] && curl -fsSL -o "$release_work/previous/previous-components.json" "$previous_url"; then
    previous_components="$release_work/previous/previous-components.json"
  fi
fi
[[ -n "$previous_components" ]] || echo 'No previous component manifest: cannot prove Chromium reuse against the last release' >&2
go run ./cmd/release-components package -app "$app" -version "$version" -arch arm64 -out "$release_dir" ${previous_components:+-previous "$previous_components"}
go run ./cmd/release-components check -app "$app" -version "$version" -arch arm64 -out "$release_dir"
if [[ $dry_run == 0 ]]; then
  mkdir "$release_work/dmg"
  /bin/chmod 755 "$release_work/dmg"
  ditto "$app" "$release_work/dmg/Fennec.app"
  ln -s /Applications "$release_work/dmg/Applications"
  dmg="$release_dir/Fennec-$version-macos-arm64.dmg"
  # ULMO (LZMA) keeps the download small now that Fennec.app carries Chromium:
  # about 153 MB against 217 MB for UDZO. Mounting it needs macOS 10.15 or later.
  hdiutil create -volname Fennec -srcfolder "$release_work/dmg" -format ULMO -ov "$dmg" >/dev/null
  codesign --force --timestamp --keychain "$release_keychain" --sign "$identity" "$dmg"
  notarize "$dmg" "$release_dir/notarization-dmg.json"
  xcrun stapler staple "$dmg"
  xcrun stapler validate "$dmg"
  codesign --verify --strict "$dmg"
  # Validate the app inside the shipped image, not only the source staging tree.
  mount_point="$release_work/mounted"
  mkdir "$mount_point"
  hdiutil attach -readonly -nobrowse -mountpoint "$mount_point" "$dmg" >/dev/null
  release_mount="$mount_point"
  go run ./cmd/check-bundle-permissions --volume "$release_mount"
  ./scripts/verify-macos-package.sh "$release_mount/Fennec.app" "$version"
  xcrun stapler validate "$release_mount/Fennec.app"
  spctl --assess --type execute "$release_mount/Fennec.app"
  hdiutil detach "$release_mount" >/dev/null
  release_mount=""
fi
# Verify the shipped ZIP, not just the build directory.
ditto -x -k "$release_dir/Fennec-$version-macos-arm64.zip" "$release_work/unpacked"
./scripts/verify-macos-package.sh "$release_work/unpacked/Fennec.app" "$version"
if [[ $dry_run == 0 ]]; then
  xcrun stapler validate "$release_work/unpacked/Fennec.app"
  spctl --assess --type execute "$release_work/unpacked/Fennec.app"
  source_sha=$(git rev-parse HEAD)
  jq -n --arg version "$version" --arg sha "$source_sha" --arg run "${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-runta-dev/fennec}/actions/runs/${GITHUB_RUN_ID:-local}" '{product:"Fennec",version:$version,source_repository:"runta-dev/fennec",source_sha:$sha,build_run:$run,architecture:"arm64",notarized:true}' > "$release_dir/provenance.json"
fi
# Every published file the updater fetches is listed with its digest.
(cd "$release_dir" && shopt -s nullglob && files=(*.zip *.dmg *-components.json notarization-*.json) && { [[ ! -f provenance.json ]] || files+=(provenance.json); } && shasum -a 256 "${files[@]}" > SHA256SUMS)
ls -l "$release_dir"
if [[ $dry_run == 0 && -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  printf '## Fennec %s\n\nSource: `%s`\n\nDeveloper ID signing, Apple notarization, stapling, Gatekeeper assessment, ZIP readback, and incremental-update component reassembly passed.\n' "$version" "$source_sha" >> "$GITHUB_STEP_SUMMARY"
fi
