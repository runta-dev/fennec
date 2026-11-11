package launcher

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovedApplicationPreservesNativeAgentInvocation(t *testing.T) {
	for _, kind := range []string{"entry", "shim"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "native tools")
			os.MkdirAll(bin, 0700)
			original := filepath.Join(bin, "codex")
			os.WriteFile(original, []byte("#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\"\nexit 23\n"), 0700)
			daemon := filepath.Join(root, "Fennec.app", "fen")
			os.MkdirAll(filepath.Dir(daemon), 0700)
			os.WriteFile(daemon, []byte("#!/bin/sh\nexit 91\n"), 0700)
			var commandPath string
			if kind == "entry" {
				commandPath = filepath.Join(root, "codex")
				os.Symlink(original, commandPath)
				if err := InstallEntry(commandPath, daemon, "codex"); err != nil {
					t.Fatal(err)
				}
			} else {
				shims := filepath.Join(root, "shims")
				if err := InstallShims(shims, daemon, bin); err != nil {
					t.Fatal(err)
				}
				commandPath = filepath.Join(shims, "codex")
			}
			os.RemoveAll(filepath.Dir(daemon))
			command := exec.Command(commandPath, "one two", "$(not-a-command)")
			command.Dir = bin
			var stderr bytes.Buffer
			command.Stderr = &stderr
			output, err := command.Output()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 23 {
				t.Fatal("original exit status lost", err)
			}
			if string(output) != bin+"\none two\n$(not-a-command)\n" {
				t.Fatalf("arguments or cwd changed: %q", output)
			}
			if !strings.Contains(stderr.String(), "Running locally") {
				t.Fatal("location changed without telling the user")
			}
		})
	}
}
func TestExistingApplicationFailureNeverFallsBackLocally(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "native")
	os.MkdirAll(native, 0700)
	original := filepath.Join(native, "codex")
	os.WriteFile(original, []byte("#!/bin/sh\necho SHOULD_NOT_RUN\n"), 0700)
	daemon := filepath.Join(root, "fen")
	os.WriteFile(daemon, []byte("#!/bin/sh\nexit 72\n"), 0700)
	shims := filepath.Join(root, "shims")
	if err := InstallShims(shims, daemon, native); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(filepath.Join(shims, "codex")).CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 72 || len(output) != 0 {
		t.Fatal("cloud failure silently ran locally", string(output), err)
	}
}
func TestRemovedApplicationWithoutOriginalFailsWithoutRecursion(t *testing.T) {
	root := t.TempDir()
	daemon := filepath.Join(root, "fen")
	os.WriteFile(daemon, []byte("#!/bin/sh\nexit 0\n"), 0700)
	shims := filepath.Join(root, "shims")
	if err := InstallShims(shims, daemon, shims); err != nil {
		t.Fatal(err)
	}
	os.Remove(daemon)
	output, err := exec.Command(filepath.Join(shims, "codex")).CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 127 || !strings.Contains(string(output), "Reinstall") {
		t.Fatal(string(output), err)
	}
}

func TestOriginalDiscoverySkipsOtherStaleFennecShimDirectories(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old-shims")
	native := filepath.Join(root, "native")
	os.MkdirAll(old, 0700)
	os.MkdirAll(native, 0700)
	os.WriteFile(filepath.Join(old, "codex"), []byte("#!/bin/sh\n# Fennec managed launcher v1\nexit 99\n"), 0700)
	target := filepath.Join(native, "codex")
	os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0700)
	resolved, err := ResolveOriginal("codex", old+string(os.PathListSeparator)+native, filepath.Join(root, "current-shims"), filepath.Join(root, "fen"))
	if err != nil || resolved != target {
		t.Fatal("stale shim captured as the original executable", resolved, err)
	}
}
