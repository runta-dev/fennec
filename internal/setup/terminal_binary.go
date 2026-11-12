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
 printf 'already_installed\n'
fi
`, version, version)
	if chk, chkErr := run(checkScript, nil); chkErr == nil && strings.Contains(string(chk), "already_installed") {
		return nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	suffix := hex.EncodeToString(nonce[:])
	var compressed bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if _, err := gz.Write(data); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	installScript := fmt.Sprintf(`set -eu
umask 077
root="$HOME/.local/fennec/terminal/%s"
bin_dir="$HOME/.local/fennec/bin"
mkdir -p "$root" "$bin_dir"
chmod 700 "$root" "$bin_dir"
staged="$root/.upload-%s"
trap 'rm -f "$staged"' EXIT
gzip -dc > "$staged"
printf '%%s  %%s\n' '%s' "$staged" | sha256sum -c - >/dev/null
chmod 700 "$staged"
mv -f "$staged" "$root/fennec-terminal"
trap - EXIT
tmp_link="$bin_dir/.terminal-%s"
ln -s "$root/fennec-terminal" "$tmp_link"
mv -f "$tmp_link" "$bin_dir/fennec-terminal"
`, version, suffix, version, suffix)
	if _, err := run(installScript, compressed.Bytes()); err != nil {
		return fmt.Errorf("terminal service upload verification failed: %w", err)
	}
	return nil
}
