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

const MagpieSetupScript = `set -eu
base="$HOME/.local/fennec"
mkdir -p "$base" "$HOME/.local/bin"
case "$(uname -m)" in
 x86_64) arch=amd64; sum=0895e20d8187bbcc70e12ea25a6975ed6b9ffd6fd2d515e8786e421100171f38 ;;
 aarch64|arm64) arch=arm64; sum=7d4a30c8846992226d7a8b241a8b5f2186f42a8d4dd79380f6420c31424d06f0 ;;
 *) exit 69 ;;
esac
if [ -f "$HOME/.local/bin/magpie" ] && printf '%s  %s\n' "$sum" "$HOME/.local/bin/magpie" | sha256sum -c - >/dev/null 2>&1; then exit 0; fi
work=$(mktemp -d "$base/magpie-download.XXXXXX")
trap 'rm -rf "$work"' EXIT
curl --fail --location --retry 2 --max-time 180 "https://github.com/yetone/magpie-releases/releases/download/v0.1.3142/magpie-cli-linux-$arch" -o "$work/magpie"
printf '%s  %s\n' "$sum" "$work/magpie" | sha256sum -c -
chmod 755 "$work/magpie"
mv "$work/magpie" "$HOME/.local/bin/magpie"
`

// Magpie is supervised by a Fennec-owned loop, detached from SSH sessions.
// Installation is distinct from provider authorization and agent routing.
const MagpieServiceScript = `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
base="$HOME/.local/fennec"
mkdir -p "$base/logs" "$HOME/.config/fennec"
umask 077
cat > "$base/magpie-supervisor.sh" <<'SUPERVISOR'
#!/bin/sh
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
if [ -z "${NODE_EXTRA_CA_CERTS:-}" ] && [ -f /usr/local/share/ca-certificates/runta-egress.crt ]; then
 export NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/runta-egress.crt
fi
if [ -z "${SSL_CERT_FILE:-}" ] && [ -f /etc/ssl/certs/ca-certificates.crt ]; then
 export SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
fi
export MAGPIE_ADDR=127.0.0.1:3425
export CODEX_HOME="$HOME/.local/fennec/native/codex"
export CLAUDE_CONFIG_DIR="$HOME/.local/fennec/native/claude"
export PI_CODING_AGENT_DIR="$HOME/.local/fennec/native/pi"
export KIMI_CODE_HOME="$HOME/.local/fennec/native/kimi"
child=
trap 'test -z "$child" || kill "$child" 2>/dev/null; exit 0' TERM INT
while :; do
 "$HOME/.local/bin/magpie" serve >> "$HOME/.local/fennec/logs/magpie.log" 2>&1 &
 child=$!
 wait "$child"
 child=
 sleep 1
done
SUPERVISOR
chmod 700 "$base/magpie-supervisor.sh"
pidfile="$HOME/.config/fennec/magpie-supervisor.pid"
alive=false
if [ -f "$pidfile" ]; then
 pid=$(cat "$pidfile")
 case "$pid" in ''|*[!0-9]*) ;; *)
  if kill -0 "$pid" 2>/dev/null && tr '\000' ' ' < "/proc/$pid/cmdline" | grep -Fq "$base/magpie-supervisor.sh"; then alive=true; fi ;;
 esac
fi
if [ "$alive" = false ]; then
 setsid nohup "$base/magpie-supervisor.sh" </dev/null >/dev/null 2>&1 &
 echo $! > "$pidfile"
fi
attempt=0
while [ "$attempt" -lt 30 ]; do
 if curl --fail --silent --max-time 2 http://127.0.0.1:3425/v1/models > "$HOME/.config/fennec/magpie-models.json.tmp"; then
  mv "$HOME/.config/fennec/magpie-models.json.tmp" "$HOME/.config/fennec/magpie-models.json"
  exit 0
 fi
 attempt=$((attempt + 1))
 sleep 1
done
echo 'Magpie gateway did not become ready' >&2
exit 70
`
