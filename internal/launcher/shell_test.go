package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellInitIdempotentAndLiteral(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "space ' and $(not-run)")
	script, err := ShellInit("sh", directory)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", script+script+"printf '%s' \"$PATH\"")
	command.Env = append(os.Environ(), "PATH=/usr/bin:/bin")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	expected := directory + ":/usr/bin:/bin"
	if string(output) != expected {
		t.Fatalf("%q != %q", output, expected)
	}
}
func TestShellInitRejectsUnsupportedInputs(t *testing.T) {
	for _, directory := range []string{"relative", "/tmp/new\nline", "/tmp/colon:path"} {
		if _, err := ShellInit("sh", directory); err == nil {
			t.Fatal(directory)
		}
	}
	if _, err := ShellInit("unsupported-shell", "/tmp/shims"); err == nil {
		t.Fatal("unsupported syntax accepted")
	}
	script, _ := ShellInit("zsh", "/tmp/shims")
	if !strings.Contains(script, "hash -r") {
		t.Fatal("shell command cache not refreshed")
	}
}

func TestShellInitPromotesExistingShimWithoutChangingOtherEntries(t *testing.T) {
	directory := "/tmp/fennec shims"
	for _, shell := range []string{"sh", "bash", "zsh"} {
		binary, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		t.Run(shell, func(t *testing.T) {
			script, err := ShellInit(shell, directory)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(binary, "-c", script+script+"printf '%s' \"$PATH\"")
			command.Env = append(os.Environ(), "PATH=/usr/bin::"+directory+":/bin:"+directory+":")
			output, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			if string(output) != directory+":/usr/bin::/bin:" {
				t.Fatalf("path ordering/empty entries changed: %q", output)
			}
		})
	}
}
