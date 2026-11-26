package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVendorInstallReusesBinaryWithoutNetwork(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("Linux cloud installer requires flock")
	}
	for _, id := range []string{"cursor", "grok", "agy"} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			name := id
			if id == "cursor" {
				name = "cursor-agent"
			}
			bin := filepath.Join(home, ".local/fennec/agents/bin")
			os.MkdirAll(bin, 0700)
			os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 99\n"), 0700)
			script, err := EnsureAgentScript(Agent{ID: id, Version: "latest"})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "HOME="+home)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatal(string(output), err)
			}
			managed, err := os.Stat(filepath.Join(bin, name))
			if err != nil {
				t.Fatal(err)
			}
			alias, err := os.Stat(filepath.Join(home, ".local/bin", name))
			if err != nil || !os.SameFile(managed, alias) {
				t.Fatal("managed installation did not repair its alias", err)
			}
			if strings.Contains(script, "| bash") {
				t.Fatal("vendor installer runs a vendor script against the home")
			}
			short, err := os.Stat(filepath.Join(home, ".local/bin/agent"))
			if (id == "cursor") != (err == nil) || id == "cursor" && !os.SameFile(managed, short) {
				t.Fatal("agent alias published for the wrong vendor", err)
			}
			if _, err := EnsureAgentScript(Agent{ID: id, Version: "latest;bad"}); err == nil {
				t.Fatal("accepted injected version")
			}
		})
	}
}

// Another vendor's `agent` stays: Cursor's alias is published only where none exists.
func TestCursorInstallKeepsAnotherAgent(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("Linux cloud installer requires flock")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local/fennec/agents/bin")
	os.MkdirAll(bin, 0700)
	os.MkdirAll(filepath.Join(home, ".local/bin"), 0700)
	os.WriteFile(filepath.Join(bin, "cursor-agent"), []byte("#!/bin/sh\nexit 99\n"), 0700)
	other := filepath.Join(home, ".local/bin/agent")
	os.WriteFile(other, []byte("#!/bin/sh\necho other\n"), 0700)
	script, err := EnsureAgentScript(Agent{ID: "cursor", Version: "latest"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(output), err)
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "#!/bin/sh\necho other\n" {
		t.Fatal("replaced another vendor's agent", err)
	}
}
