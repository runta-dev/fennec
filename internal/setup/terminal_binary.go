package setup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

func InstallTerminalBinary(ctx context.Context, client *ssh.Client, artifacts string) error {
	run := func(command string, stdin []byte) ([]byte, error) {
		session, err := client.NewSession()
		if err != nil {
			return nil, err
		}
		defer session.Close()
		stop := context.AfterFunc(ctx, func() { session.Close() })
		defer stop()
		if stdin != nil {
			session.Stdin = bytes.NewReader(stdin)
		}
		return session.Output(command)
	}
	architecture, err := run("uname -m", nil)
	if err != nil {
		return err
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[strings.TrimSpace(string(architecture))]
	if arch == "" {
		return fmt.Errorf("unsupported terminal service architecture")
	}
	data, err := os.ReadFile(filepath.Join(artifacts, "fennec-terminal-linux-"+arch))
	if err != nil {
		return fmt.Errorf("bundled Go terminal service unavailable: %w", err)
	}
	if len(data) > 32<<20 || len(data) < 4 || !bytes.Equal(data[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return fmt.Errorf("invalid terminal service artifact")
	}
	digest := sha256.Sum256(data)
	version := hex.EncodeToString(digest[:])
	checkScript := fmt.Sprintf(`set -eu
root="$HOME/.local/fennec/terminal/%s"
bin="$root/fennec-terminal"
if [ -x "$bin" ] && printf '%%s  %%s\n' '%s' "$bin" | sha256sum -c - >/dev/null 2>&1; then
 mkdir -p "$HOME/.local/fennec/bin"
 chmod 700 "$HOME/.local/fennec/bin"
 ln -sfn "$bin" "$HOME/.local/fennec/bin/fennec-terminal"
 entry="$HOME/.local/bin/fennec-escalate"
 if [ ! -e "$entry" ] || [ -L "$entry" ]; then mkdir -p "$HOME/.local/bin"; ln -sfn "$HOME/.local/fennec/bin/fennec-terminal" "$entry"; fi
 printf 'already_installed\n'
else
 current=$(readlink "$HOME/.local/fennec/bin/fennec-terminal" 2>/dev/null || true)
 printf 'current=%%s\n' "$(basename "$(dirname "$current")")"
fi
`, version, version)
	chk, chkErr := run(checkScript, nil)
	if chkErr == nil && strings.Contains(string(chk), "already_installed") {
		if cachedTerminalBinary(arch, version) == nil {
			rememberTerminalBinary(arch, data)
		}
		return nil
	}
	current := ""
	for _, line := range strings.Split(string(chk), "\n") {
		if value, ok := strings.CutPrefix(line, "current="); ok {
			current = strings.TrimSpace(value)
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	suffix := hex.EncodeToString(nonce[:])
	// installScript stages the binary produced by decode, verifies it and
	// switches the stable link to it.
	installScript := func(decode string) string {
		return fmt.Sprintf(installTemplate, version, suffix, decode, version, suffix)
	}
	if old := cachedTerminalBinary(arch, current); old != nil && current != version {
		if patch, err := EncodeTerminalPatch(old, data); err == nil && len(patch) < len(data)/2 {
			previous := fmt.Sprintf(`"$HOME/.local/fennec/terminal/%s/fennec-terminal"`, current)
			if _, err := run(installScript(previous+" apply-patch "+previous), patch); err == nil {
				rememberTerminalBinary(arch, data)
				return nil
			}
			// An older cloud binary without apply-patch, or a damaged one: send it whole.
		}
	}
	var compressed bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if _, err := gz.Write(data); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if _, err := run(installScript("gzip -dc"), compressed.Bytes()); err != nil {
		return fmt.Errorf("terminal service upload verification failed: %w", err)
	}
	rememberTerminalBinary(arch, data)
	return nil
}

const installTemplate = `set -eu
umask 077
root="$HOME/.local/fennec/terminal/%s"
bin_dir="$HOME/.local/fennec/bin"
mkdir -p "$root" "$bin_dir"
chmod 700 "$root" "$bin_dir"
staged="$root/.upload-%s"
trap 'rm -f "$staged"' EXIT
%s > "$staged"
printf '%%s  %%s\n' '%s' "$staged" | sha256sum -c - >/dev/null
chmod 700 "$staged"
mv -f "$staged" "$root/fennec-terminal"
trap - EXIT
tmp_link="$bin_dir/.terminal-%s"
ln -s "$root/fennec-terminal" "$tmp_link"
mv -f "$tmp_link" "$bin_dir/fennec-terminal"
# Agents request Mac escalation with fennec-escalate; never replace a user's own file.
entry="$HOME/.local/bin/fennec-escalate"
if [ ! -e "$entry" ] || [ -L "$entry" ]; then mkdir -p "$HOME/.local/bin"; ln -sfn "$HOME/.local/fennec/bin/fennec-terminal" "$entry"; fi
`
