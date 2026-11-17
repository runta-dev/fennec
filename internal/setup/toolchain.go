package setup

// Downloads are pinned and verified before extraction/execution. Tools stay in
// the runtime user's home and need neither sudo nor a system package manager.
const NodeSetupScript = `set -eu
base="$HOME/.local/fennec"
mkdir -p "$base" "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
case "$(uname -m)" in
 x86_64) arch=x64; fd_arch=x86_64; sum=c33c39ed9c80deddde77c960d00119918b9e352426fd604ba41638d6526a4744; fd_sum=d9bfa25ec28624545c222992e1b00673b7c9ca5eb15393c40369f10b28f9c932 ;;
 aarch64|arm64) arch=arm64; fd_arch=aarch64; sum=25ba95dfb96871fa2ef977f11f95ea90818c8fa15c0f2110771db08d4ba423be; fd_sum=4e8e596646d047d904f2c5ca74b39dccc69978b6e1fb101094e534b0b59c1bb0 ;;
 *) echo 'unsupported runtime architecture' >&2; exit 69 ;;
esac
(
 if [ ! -x "$base/node/bin/node" ] || [ "$("$base/node/bin/node" --version)" != v22.22.0 ]; then
  work=$(mktemp -d "$base/node-download.XXXXXX")
  trap 'rm -rf "$work"' EXIT
  curl --fail --silent --show-error --location --retry 2 --max-time 180 "https://nodejs.org/dist/v22.22.0/node-v22.22.0-linux-$arch.tar.gz" -o "$work/node.tar.gz"
  printf '%s  %s\n' "$sum" "$work/node.tar.gz" | sha256sum -c - >/dev/null
  tar -xzf "$work/node.tar.gz" -C "$work"
  test ! -e "$base/node" || mv "$base/node" "$base/node.previous.$(date +%s)"
  mv "$work/node-v22.22.0-linux-$arch" "$base/node"
  "$base/node/bin/node" --version >/dev/null
 fi
) &
node_pid=$!
(
 if ! command -v fd >/dev/null 2>&1; then
  if command -v fdfind >/dev/null 2>&1; then
   ln -sfn "$(command -v fdfind)" "$HOME/.local/bin/fd"
  elif [ -x "$HOME/.pi/agent/bin/fd" ]; then
   ln -sfn "$HOME/.pi/agent/bin/fd" "$HOME/.local/bin/fd"
  else
   fd_work=$(mktemp -d "$base/fd-download.XXXXXX")
   trap 'rm -rf "$fd_work"' EXIT
   curl --fail --silent --show-error --location --retry 2 --max-time 120 "https://github.com/sharkdp/fd/releases/download/v10.2.0/fd-v10.2.0-$fd_arch-unknown-linux-musl.tar.gz" -o "$fd_work/fd.tar.gz"
   printf '%s  %s\n' "$fd_sum" "$fd_work/fd.tar.gz" | sha256sum -c - >/dev/null
   tar -xzf "$fd_work/fd.tar.gz" -C "$fd_work"
   chmod 755 "$fd_work/fd-v10.2.0-$fd_arch-unknown-linux-musl/fd"
   mv "$fd_work/fd-v10.2.0-$fd_arch-unknown-linux-musl/fd" "$HOME/.local/bin/fd"
  fi
 fi
) &
fd_pid=$!
wait "$node_pid"
wait "$fd_pid"
command -v fd >/dev/null
`

// The private downstream's runtime artifacts are published with Fennec so cloud
// installation never needs credentials for the source repository.
const MagpieRuntimeVersion = "0.1.4177-runta.3"
const MagpieAMD64SHA256 = "09f29fbe332686846da8bf5d623e5b748c3f16aa7a99502e41d66fca83f7534b"
const MagpieARM64SHA256 = "7519b50ee7eaaa4c68da52a63587cd57aef5d938af26cadad4a5376bf7c16234"

const MagpieSetupScript = `set -eu
base="$HOME/.local/fennec"
mkdir -p "$base" "$HOME/.local/bin"
case "$(uname -m)" in
 x86_64) arch=amd64; sum=` + MagpieAMD64SHA256 + ` ;;
 aarch64|arm64) arch=arm64; sum=` + MagpieARM64SHA256 + ` ;;
 *) exit 69 ;;
esac
if [ -f "$HOME/.local/bin/magpie" ] && printf '%s  %s\n' "$sum" "$HOME/.local/bin/magpie" | sha256sum -c - >/dev/null 2>&1; then exit 0; fi
work=$(mktemp -d "$base/magpie-download.XXXXXX")
trap 'rm -rf "$work"' EXIT
curl --fail --location --retry 2 --max-time 180 "https://github.com/runta-dev/fennec/releases/download/magpie-v` + MagpieRuntimeVersion + `/magpie-cli-linux-$arch" -o "$work/magpie"
printf '%s  %s\n' "$sum" "$work/magpie" | sha256sum -c -
chmod 755 "$work/magpie"
mv "$work/magpie" "$HOME/.local/bin/magpie"
`

// The cloud service manager owns gateway lifetime, independently of SSH.
const MagpieServiceScript = `set -eu
"$HOME/.local/fennec/bin/fennec-terminal" magpie-service install
`
