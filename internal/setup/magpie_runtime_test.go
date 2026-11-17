package setup

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMagpieRuntimeInstall(t *testing.T) {
	for _, arch := range []string{"x86_64", "aarch64"} {
		t.Run(arch, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(home, "artifact")
			good := []byte("verified downstream runtime\n")
			write := func(path string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			write(payload, good, 0600)
			write(filepath.Join(bin, "uname"), []byte("#!/bin/sh\necho "+arch+"\n"), 0700)
			write(filepath.Join(bin, "curl"), []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$HOME/downloads"
while [ "$#" -gt 0 ]; do
 if [ "$1" = -o ]; then cp "$HOME/artifact" "$2"; exit; fi
 shift
done
exit 1
`), 0700)
			if _, err := exec.LookPath("sha256sum"); err != nil {
				write(filepath.Join(bin, "sha256sum"), []byte("#!/bin/sh\nexec shasum -a 256 \"$@\"\n"), 0700)
			}
			sum := fmt.Sprintf("%x", sha256.Sum256(good))
			script := strings.NewReplacer(MagpieAMD64SHA256, sum, MagpieARM64SHA256, sum).Replace(MagpieSetupScript)
			run := func() error {
				cmd := exec.Command("sh", "-c", script)
				cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":"+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				if err != nil {
					return fmt.Errorf("%w: %s", err, out)
				}
				return nil
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, ".local/bin/magpie")
			installed, err := os.ReadFile(target)
			if err != nil || string(installed) != string(good) {
				t.Fatalf("runtime not installed: %q, %v", installed, err)
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(filepath.Join(home, "downloads"))
			wantArch := "amd64"
			if arch == "aarch64" {
				wantArch = "arm64"
			}
			wantURL := "https://github.com/runta-dev/fennec/releases/download/magpie-v" + MagpieRuntimeVersion + "/magpie-cli-linux-" + wantArch
			if strings.Count(string(calls), "\n") != 1 || !strings.Contains(string(calls), wantURL) {
				t.Fatalf("warm install downloaded again or used the wrong source: %s", calls)
			}
			// A bad download must not replace the old executable.
			write(target, []byte("old runtime"), 0755)
			write(payload, []byte("corrupt download"), 0600)
			if err := run(); err == nil {
				t.Fatal("accepted a corrupt runtime")
			}
			installed, _ = os.ReadFile(target)
			if string(installed) != "old runtime" {
				t.Fatal("failed verification replaced the existing runtime")
			}
			write(payload, good, 0600)
			if err := run(); err != nil {
				t.Fatal(err)
			}
			installed, _ = os.ReadFile(target)
			if string(installed) != string(good) {
				t.Fatal("existing runtime was not upgraded")
			}
		})
	}
}
