package setup

import "strings"

// Vendor distributions are installed in a fresh prefix. No vendor installer is
// run against the user's home, so neither can replace the shared `agent` alias.
func ensureVendorAgentScript(id string) string {
	name := "grok"
	download := `case "$(uname -m)" in
 x86_64) arch=x86_64 ;; aarch64|arm64) arch=aarch64 ;; *) exit 69 ;; esac
base=https://x.ai/cli
version=$(curl -fsSL --retry 2 --max-time 30 "$base/stable") || {
 base=https://storage.googleapis.com/grok-build-public-artifacts/cli
 version=$(curl -fsSL --retry 2 --max-time 30 "$base/stable")
}
printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9._]+)?$'
curl -fsSL --retry 2 --max-time 180 "$base/grok-$version-linux-$arch.gz" -o "$stage/grok.gz"
gzip -d "$stage/grok.gz"
chmod 700 "$stage/grok"
source="$stage/grok"
`
	if id == "cursor" {
		name = "cursor-agent"
		download = `case "$(uname -m)" in
 x86_64) arch=x64 ;; aarch64|arm64) arch=arm64 ;; *) exit 69 ;; esac
curl -fsSL --retry 2 --max-time 30 https://cursor.com/install -o "$stage/install.sh"
version=$(sed -n 's|.*https://downloads.cursor.com/lab/\([^/]*\)/.*|\1|p' "$stage/install.sh" | head -n 1)
printf '%s' "$version" | grep -Eq '^[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[a-zA-Z0-9]+$'
curl -fsSL --retry 2 --max-time 180 "https://downloads.cursor.com/lab/$version/linux/$arch/agent-cli-package.tar.gz" -o "$stage/package.tar.gz"
mkdir "$stage/package"
tar -xzf "$stage/package.tar.gz" --strip-components=1 -C "$stage/package"
source="$stage/package/cursor-agent"
`
	}
	if id == "agy" {
		// Google's installer (antigravity.google/cli/install.sh) reads the same
		// manifest, but also edits shell profiles through `agy install`.
		name = "agy"
		download = `case "$(uname -m)" in
 x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) exit 69 ;; esac
platform=linux_$arch
if [ -f /lib/libc.musl-x86_64.so.1 ] || [ -f /lib/libc.musl-aarch64.so.1 ]; then platform=${platform}_musl; fi
manifest=$(curl -fsSL --retry 2 --max-time 30 "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/$platform.json")
field() { printf '%s' "$manifest" | tr -d '\n' | sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'; }
url=$(field url)
sha512=$(field sha512)
case "$url" in https://storage.googleapis.com/antigravity-public/*.tar.gz) ;; *) exit 65 ;; esac
printf '%s' "$sha512" | grep -Eq '^[0-9a-f]{128}$'
curl -fsSL --retry 2 --max-time 300 "$url" -o "$stage/agy.tar.gz"
printf '%s  %s\n' "$sha512" "$stage/agy.tar.gz" | sha512sum -c - >/dev/null
tar -xzf "$stage/agy.tar.gz" -C "$stage" antigravity
chmod 700 "$stage/antigravity"
source="$stage/antigravity"
`
	}
	return strings.ReplaceAll(`set -eu
export PATH="$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
umask 077
mkdir -p "$HOME/.local/fennec/install-locks" "$HOME/.local/fennec/agent-versions" "$HOME/.local/fennec/agents/bin" "$HOME/.local/bin"
exec 9>"$HOME/.local/fennec/install-locks/@NAME@.lock"
flock -w 180 9
binary="$HOME/.local/fennec/agents/bin/@NAME@"
publish_alias() {
 ln -s "$binary" "$HOME/.local/bin/@NAME@.tmp.$$"
 mv -f "$HOME/.local/bin/@NAME@.tmp.$$" "$HOME/.local/bin/@NAME@"
}
if [ -x "$binary" ]; then publish_alias; exit 0; fi
if command -v @NAME@ >/dev/null 2>&1; then exit 0; fi
stage=$(mktemp -d "$HOME/.local/fennec/agent-versions/@NAME@.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
`+download+`
test -s "$source"
"$source" --version >/dev/null
ln -s "$source" "$binary.tmp.$$"
mv -f "$binary.tmp.$$" "$binary"
trap - EXIT HUP INT TERM
publish_alias
`, "@NAME@", name)
}
