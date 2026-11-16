#!/bin/bash
# Copies a published GitHub release of runta-dev/fennec into the R2 bucket
# the website serves downloads from (website/deploy/worker.js):
#
#   <product>/v<version>/<file>   the release files, checked against SHA256SUMS
#   <product>/latest.json         {product, version, tag, files} for /download/<product>/latest
#
# Tags v<version> are the Fennec app (product "fennec": the DMG and full ZIP,
# SHA256SUMS and provenance.json; update components stay on GitHub, which
# the updater reads). Tags fennec-cua-v<version> are Fennec Computer Use
# (product "computer-use": the disk image and every archive).
#
# usage: sync-release-r2.sh TAG [--no-latest]
# Needs gh, and wrangler logged in to the Runta account (or
# CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID with R2 write access).
# Environment: FENNEC_R2_BUCKET (default fennec-releases), WRANGLER (default "npx -y wrangler@4").
set -euo pipefail
tag="${1:-}"
set_latest=1
[[ "${2:-}" == --no-latest ]] && set_latest=0
[[ -n "$tag" ]] || { echo 'usage: sync-release-r2.sh TAG [--no-latest]' >&2; exit 2; }
bucket="${FENNEC_R2_BUCKET:-fennec-releases}"
wrangler="${WRANGLER:-npx -y wrangler@4}"
repo=runta-dev/fennec
case "$tag" in
  fennec-cua-v*) product=computer-use; version="${tag#fennec-cua-v}"
    full="fennec-cua-$version-macos-arm64.tar.gz"; core="fennec-cua-$version-macos-arm64-core.tar.gz"
    files=("$full" "$full.sha256" "$core" "$core.sha256" SHA256SUMS provenance.json)
    # 0.1.0 shipped archives only; later releases lead with the disk image.
    dmg="FennecCUA-$version-macos-arm64.dmg"
    if gh release view "$tag" -R runta-dev/fennec --json assets -q '.assets[].name' | grep -qx "$dmg"; then files=("$dmg" "${files[@]}"); else dmg=""; fi
    latest_files=$(jq -n --arg dmg "$dmg" --arg full "$full" --arg core "$core" '{full:$full, core:$core, checksums:"SHA256SUMS"} + (if $dmg == "" then {} else {dmg:$dmg} end)') ;;
  v*) product=fennec; version="${tag#v}"
    dmg="Fennec-$version-macos-arm64.dmg"; zip="Fennec-$version-macos-arm64.zip"
    files=("$dmg" "$zip" SHA256SUMS provenance.json)
    latest_files=$(jq -n --arg dmg "$dmg" --arg zip "$zip" '{dmg:$dmg, zip:$zip, checksums:"SHA256SUMS"}') ;;
  *) echo "Unknown tag $tag" >&2; exit 2 ;;
esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "Bad version in $tag" >&2; exit 2; }
test "$(gh release view "$tag" -R "$repo" --json isDraft -q .isDraft)" = false || { echo "$tag is a draft" >&2; exit 1; }

work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/fennec-r2.XXXXXX")
trap 'rm -rf "$work"' EXIT
args=()
for f in "${files[@]}"; do args+=(-p "$f"); done
gh release download "$tag" -R "$repo" -D "$work" "${args[@]}"
cd "$work"
for f in "${files[@]}"; do [[ -s "$f" ]] || { echo "Missing $f in $tag" >&2; exit 1; }; done
# Every downloaded file SHA256SUMS lists must match it.
grep -E "  ($(printf '%s|' "${files[@]}" | sed 's/\./\\./g; s/|$//'))$" SHA256SUMS > checked.sums
shasum -a 256 -c checked.sums
for f in "${files[@]}"; do
  [[ "$f" == SHA256SUMS || "$f" == provenance.json || "$f" == *.sha256 ]] || grep -q "  $f$" checked.sums || { echo "$f is not in SHA256SUMS" >&2; exit 1; }
done

type_of() {
  case "$1" in
    *.dmg) echo application/x-apple-diskimage ;;
    *.zip) echo application/zip ;;
    *.tar.gz) echo application/gzip ;;
    *.json) echo application/json ;;
    *) echo text/plain ;;
  esac
}
for f in "${files[@]}"; do
  echo "uploading $product/v$version/$f"
  $wrangler r2 object put "$bucket/$product/v$version/$f" --file "$f" --content-type "$(type_of "$f")" --remote >/dev/null
done
if [[ $set_latest == 1 ]]; then
  jq -n --arg product "$product" --arg version "$version" --arg tag "$tag" --argjson files "$latest_files" \
    '{product:$product, version:$version, tag:$tag, files:$files}' > latest.json
  $wrangler r2 object put "$bucket/$product/latest.json" --file latest.json --content-type application/json --cache-control no-store --remote >/dev/null
  echo "$product/latest.json -> $version"
fi
