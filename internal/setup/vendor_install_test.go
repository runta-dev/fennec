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
	for _, id := range []string{"cursor", "grok"} {
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
			if strings.Contains(script, "| bash") || strings.Contains(script, "/bin/agent\"") {
				t.Fatal("vendor installer can overwrite shared alias")
			}
			if _, err := EnsureAgentScript(Agent{ID: id, Version: "latest;bad"}); err == nil {
				t.Fatal("accepted injected version")
			}
		})
	}
}
