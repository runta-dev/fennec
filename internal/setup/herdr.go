package setup

// Herdr Core owns every remote terminal. Pin the official release manifest's
// checksums; never replace the user's already running Core with another version.
const HerdrSetupScript = `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
base="$HOME/.local/fennec"
mkdir -p "$base" "$HOME/.local/bin"
if [ -x "$HOME/.local/bin/herdr-core" ]; then
 actual=$("$HOME/.local/bin/herdr-core" --version)
 if [ "$actual" != 'hc 0.1.18' ]; then echo 'existing Herdr Core version requires compatibility review' >&2; exit 69; fi
 if ! "$HOME/.local/bin/herdr-core" server status >/dev/null 2>&1; then
  "$HOME/.local/bin/herdr-core" server start >/dev/null
 fi
 "$HOME/.local/bin/herdr-core" ls --json >/dev/null
 exit 0
fi
case "$(uname -m)" in
 x86_64) arch=x86_64; sum=5b2f97cb4b2a9d7a6b2cc5942628203ed9fbb7cc0c2b4b09ad8a6f5648e246b7 ;;
 aarch64|arm64) arch=aarch64; sum=cf33393cf950bb763734dc3e16610ab9daf8492391a4ef0de56026cd7f752853 ;;
 *) exit 69 ;;
esac
work=$(mktemp -d "$base/herdr-download.XXXXXX")
trap 'rm -rf "$work"' EXIT
curl --fail --silent --show-error --location --max-time 180 "https://releases.herdr.dev/core/0.1.18/herdr-core-linux-$arch.tar.gz" -o "$work/core.tar.gz"
printf '%s  %s\n' "$sum" "$work/core.tar.gz" | sha256sum -c -
tar -xzf "$work/core.tar.gz" -C "$work"
test "$("$work/herdr-core" --version)" = 'hc 0.1.18'
chmod 755 "$work/herdr-core"
mv "$work/herdr-core" "$HOME/.local/bin/herdr-core"
if [ ! -e "$HOME/.local/bin/hc" ]; then ln -s herdr-core "$HOME/.local/bin/hc"; fi
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
herdr-core server start >/dev/null
herdr-core ls --json >/dev/null
`
