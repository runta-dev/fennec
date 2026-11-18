#!/usr/bin/env bash
# Builds Fennec for iPhone (ios/) and uploads it to TestFlight.
#
# Runs on a macOS runner with Xcode 26 or later. It needs:
#   APPLE_TEAM_ID              the Apple Developer team that owns com.runta.fennec.ios
#   APPLE_ASC_KEY_BASE64       an App Store Connect API key (.p8, base64)
#   APPLE_ASC_KEY_ID           its key ID
#   APPLE_ASC_ISSUER_ID        its issuer ID
# Signing is either
#   automatic: Xcode creates the Apple Distribution certificate and App Store
#              profile with the API key, which then needs the Admin role; or
#   manual:    IOS_DISTRIBUTION_CERTIFICATE_BASE64 (+ _PASSWORD), an Apple
#              Distribution .p12, and IOS_PROVISIONING_PROFILE_BASE64, an App
#              Store profile for com.runta.fennec.ios; the key then only uploads
#              (App Manager or Developer role).
# Optional: FENNEC_IOS_VERSION (marketing version, default from project.yml)
# and FENNEC_BUILD_NUMBER (must grow with every upload; CI passes run_number).
#
# --check reports every missing prerequisite at once and stops before building.
set -euo pipefail

bundle_id=com.runta.fennec.ios
check_only=false
case "${1:-}" in
  '') ;;
  --check) check_only=true ;;
  *) echo 'usage: package-testflight-ios.sh [--check]' >&2; exit 2 ;;
esac

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

missing=()
for name in APPLE_TEAM_ID APPLE_ASC_KEY_BASE64 APPLE_ASC_KEY_ID APPLE_ASC_ISSUER_ID; do
  [ -n "${!name:-}" ] || missing+=("$name")
done
manual=false
if [ -n "${IOS_DISTRIBUTION_CERTIFICATE_BASE64:-}${IOS_PROVISIONING_PROFILE_BASE64:-}" ]; then
  manual=true
  for name in IOS_DISTRIBUTION_CERTIFICATE_BASE64 IOS_DISTRIBUTION_CERTIFICATE_PASSWORD IOS_PROVISIONING_PROFILE_BASE64; do
    [ -n "${!name:-}" ] || missing+=("$name")
  done
fi
xcode=$(xcodebuild -version 2>/dev/null | awk 'NR==1{print $2}' || true)
xcode_major=${xcode%%.*}
if [ -z "$xcode" ] || [ "${xcode_major:-0}" -lt 26 ]; then
  missing+=("Xcode 26 or later (found ${xcode:-none})")
fi
command -v go >/dev/null || missing+=(go)
if [ ${#missing[@]} -gt 0 ]; then
  printf '::error::Missing for TestFlight: %s\n' "${missing[@]}" >&2
  exit 1
fi
echo "Xcode $xcode; signing: $([ "$manual" = true ] && echo manual || echo automatic)"
$check_only && exit 0

work=$(mktemp -d "${RUNNER_TEMP:-/tmp}/fennec-ios.XXXXXX")
keychain="$work/signing.keychain-db"
cleanup() {
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  [ -n "${profile_path:-}" ] && rm -f "$profile_path"
  rm -rf "$work"
}
trap cleanup EXIT

key="$work/AuthKey_${APPLE_ASC_KEY_ID}.p8"
printf '%s' "$APPLE_ASC_KEY_BASE64" | base64 --decode > "$key"
auth=(-allowProvisioningUpdates -authenticationKeyPath "$key" -authenticationKeyID "$APPLE_ASC_KEY_ID" -authenticationKeyIssuerID "$APPLE_ASC_ISSUER_ID")

# Distribution from the start: automatic signing otherwise asks for a
# development profile, which needs a registered device.
signing=(DEVELOPMENT_TEAM="$APPLE_TEAM_ID" CODE_SIGN_STYLE=Automatic CODE_SIGN_IDENTITY="Apple Distribution")
profile_name=
if $manual; then
  password=$(uuidgen)
  security create-keychain -p "$password" "$keychain"
  security set-keychain-settings -lut 3600 "$keychain"
  security unlock-keychain -p "$password" "$keychain"
  security list-keychains -d user -s "$keychain" $(security list-keychains -d user | tr -d '"')
  printf '%s' "$IOS_DISTRIBUTION_CERTIFICATE_BASE64" | base64 --decode > "$work/distribution.p12"
  security import "$work/distribution.p12" -k "$keychain" -P "$IOS_DISTRIBUTION_CERTIFICATE_PASSWORD" -T /usr/bin/codesign -T /usr/bin/security >/dev/null
  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$password" "$keychain" >/dev/null
  security find-identity -v -p codesigning "$keychain" | grep -q '"Apple Distribution:' \
    || { echo '::error::IOS_DISTRIBUTION_CERTIFICATE_BASE64 is not an Apple Distribution certificate' >&2; exit 1; }
  printf '%s' "$IOS_PROVISIONING_PROFILE_BASE64" | base64 --decode > "$work/profile.mobileprovision"
  security cms -D -i "$work/profile.mobileprovision" > "$work/profile.plist"
  profile_uuid=$(/usr/libexec/PlistBuddy -c 'Print :UUID' "$work/profile.plist")
  profile_name=$(/usr/libexec/PlistBuddy -c 'Print :Name' "$work/profile.plist")
  profile_app=$(/usr/libexec/PlistBuddy -c 'Print :Entitlements:application-identifier' "$work/profile.plist")
  [ "$profile_app" = "$APPLE_TEAM_ID.$bundle_id" ] \
    || { echo "::error::Provisioning profile is for $profile_app, expected $APPLE_TEAM_ID.$bundle_id" >&2; exit 1; }
  profiles="$HOME/Library/MobileDevice/Provisioning Profiles"
  mkdir -p "$profiles"
  profile_path="$profiles/$profile_uuid.mobileprovision"
  cp "$work/profile.mobileprovision" "$profile_path"
  signing=(DEVELOPMENT_TEAM="$APPLE_TEAM_ID" CODE_SIGN_STYLE=Manual CODE_SIGN_IDENTITY="Apple Distribution" PROVISIONING_PROFILE_SPECIFIER="$profile_name" OTHER_CODE_SIGN_FLAGS="--keychain $keychain")
fi

# The shared Go client, bound for the device and the simulator.
go install golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind
export PATH="$(go env GOPATH)/bin:$PATH"
gomobile init
make ios-core

command -v xcodegen >/dev/null || brew install xcodegen
(cd ios && xcodegen generate --quiet)

version=${FENNEC_IOS_VERSION:-$(awk '/MARKETING_VERSION:/{gsub(/"/,"",$2); print $2; exit}' ios/project.yml)}
build=${FENNEC_BUILD_NUMBER:-$(date -u +%Y%m%d%H%M)}
echo "Fennec for iPhone $version ($build)"

archive="$work/Fennec.xcarchive"
xcodebuild -project ios/Fennec.xcodeproj -scheme Fennec -configuration Release \
  -destination 'generic/platform=iOS' -archivePath "$archive" \
  -skipPackagePluginValidation -skipMacroValidation "${auth[@]}" \
  MARKETING_VERSION="$version" CURRENT_PROJECT_VERSION="$build" "${signing[@]}" \
  archive

options="$work/ExportOptions.plist"
cat > "$options" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>method</key><string>app-store-connect</string>
  <key>destination</key><string>upload</string>
  <key>teamID</key><string>$APPLE_TEAM_ID</string>
  <key>signingStyle</key><string>$([ "$manual" = true ] && echo manual || echo automatic)</string>
  <key>manageAppVersionAndBuildNumber</key><false/>
  <key>uploadSymbols</key><true/>
$(if $manual; then printf '  <key>provisioningProfiles</key><dict><key>%s</key><string>%s</string></dict>\n' "$bundle_id" "$profile_name"; fi)
</dict>
</plist>
EOF

# Signs for the App Store and uploads; TestFlight processes the build after.
xcodebuild -exportArchive -archivePath "$archive" -exportOptionsPlist "$options" \
  -exportPath "$work/export" "${auth[@]}"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  printf '## Fennec for iPhone %s (%s)\n\nUploaded to App Store Connect; it appears in TestFlight once Apple finishes processing.\n' "$version" "$build" >> "$GITHUB_STEP_SUMMARY"
fi
