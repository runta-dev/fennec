//go:build linux

package queuescheduler

import (
	"fennec/internal/runtime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestServiceEnvironmentUsesExistingCloudLaunchLookup(t *testing.T) {
	home := t.TempDir()
	unit := string(queueServiceUnit(home, "/bin/fennec-terminal"))
	var path string
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "Environment=\"") {
			value, err := strconv.Unquote(strings.TrimPrefix(line, "Environment="))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(value, "PATH=") {
				path = strings.TrimPrefix(value, "PATH=")
			}
		}
	}
	if path == "" {
		t.Fatal("service has no agent PATH")
	}
	t.Setenv("PATH", path)
	for _, name := range []string{"claude", "codex", "pi"} {
		binary := filepath.Join(home, ".local/fennec/agents/bin", name)
		if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
		got, err := runtime.NewLocalRuntime(home).Lookup(name)
		if err != nil || got != binary {
			t.Fatal(name, got, err)
		}
	}
}
