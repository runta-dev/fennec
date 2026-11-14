package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVendorCommandNames(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0700)
	for _, name := range []string{"cursor", "cursor-agent", "agent", "grok", "fennec"} {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700)
	}
	shims := filepath.Join(home, "shims")
	if err := InstallShims(shims, filepath.Join(bin, "fennec"), bin); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cursor", "agent"} {
		if _, err := os.Stat(filepath.Join(shims, name)); !os.IsNotExist(err) {
			t.Fatal("intercepted editor/shared command", name)
		}
	}
	data, err := os.ReadFile(filepath.Join(shims, "cursor-agent"))
	if err != nil || !strings.Contains(string(data), "dispatch-agent 'cursor'") {
		t.Fatal(string(data), err)
	}
	original, err := ResolveOriginal("cursor", bin, shims, filepath.Join(bin, "fennec"))
	if err != nil || original != filepath.Join(bin, "cursor-agent") {
		t.Fatal(original, err)
	}
}
func TestVendorWorkingDirectories(t *testing.T) {
	for agent, flag := range map[string]string{"cursor": "--workspace", "grok": "--cwd"} {
		t.Run(agent, func(t *testing.T) {
			cwd, args, err := ResolveLaunchDirectory(agent, "/local/project", []string{"--model", flag, flag, "../other", "--", flag, "literal"})
			if err != nil || cwd != "/local/other" || strings.Join(args, "|") != "--model|"+flag+"|--|"+flag+"|literal" {
				t.Fatal(cwd, args, err)
			}
			if _, _, err = ResolveLaunchDirectory(agent, "/local", []string{flag}); err == nil {
				t.Fatal("accepted missing directory")
			}
		})
	}
}
