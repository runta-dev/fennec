package cli

import (
	"context"
	"fennec/internal/agentcmd"
	"fennec/internal/installation"
	"fennec/internal/launcher"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func launcherBundle(t *testing.T, root, version string) string {
	t.Helper()
	executable := filepath.Join(root, "Contents/MacOS/fen")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Contents/Info.plist"), []byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.runta.fennec</string></dict></plist>`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '"+version+"\\n'\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestBundleUpdateReachesExistingShimsWithoutShellRestart(t *testing.T) {
	home := t.TempDir()
	installed := filepath.Join(home, "Applications/Fennec.app")
	current := launcherBundle(t, installed, "old")
	shims := filepath.Join(home, ".local/share/fennec/shims")
	if err := launcher.InstallShims(shims, current, ""); err != nil {
		t.Fatal(err)
	}
	// The same Install path is used by automatic updates. Existing wrapper paths
	// remain valid when the complete bundle is atomically replaced.
	incoming := filepath.Join(home, "download/Fennec.app")
	launcherBundle(t, incoming, "new")
	ops := installation.Operations{
		Verify:  func(context.Context, string) error { return nil },
		Copy:    func(_ context.Context, from, to string) error { return os.CopyFS(to, os.DirFS(from)) },
		Publish: installation.SystemOperations().Publish,
	}
	if _, err := installation.Install(context.Background(), incoming, installed, true, ops); err != nil {
		t.Fatal(err)
	}
	for id := range launcher.Supported {
		out, err := exec.Command(filepath.Join(shims, agentcmd.BinaryName(id)), "literal $(argument)").Output()
		if err != nil || string(out) != "new\ndispatch-agent\n"+id+"\nliteral $(argument)\n" {
			t.Fatalf("%s retained old code: %s %v", id, out, err)
		}
	}
}

func TestOfflineLauncherRefreshMigratesOldPathsAndPreservesCustomFiles(t *testing.T) {
	home := t.TempDir()
	old := launcherBundle(t, filepath.Join(home, "old/Fennec.app"), "old")
	current := launcherBundle(t, filepath.Join(home, "Applications/Fennec.app"), "new")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(bin, "native-codex")
	if err := os.WriteFile(original, []byte("#!/bin/sh\necho native\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(bin, "codex")
	if err := os.Symlink(original, entry); err != nil {
		t.Fatal(err)
	}
	if err := launcher.InstallEntry(entry, old, "codex"); err != nil {
		t.Fatal(err)
	}
	shims := filepath.Join(home, ".local/share/fennec/shims")
	if err := launcher.InstallShims(shims, old, bin); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(shims, "pi")
	if err := os.WriteFile(custom, []byte("custom user launcher"), 0700); err != nil {
		t.Fatal(err)
	}
	// No account or cloud socket exists. One externally edited wrapper must not
	// prevent the remaining owned wrappers and old entry points from upgrading.
	if err := refreshManagedLaunchers(home, current, bin, []string{entry}); err == nil {
		t.Fatal("custom wrapper conflict was not reported")
	}
	for id := range launcher.Supported {
		if id == "pi" {
			continue
		}
		out, err := exec.Command(filepath.Join(shims, agentcmd.BinaryName(id))).Output()
		if err != nil || string(out) != "new\ndispatch-agent\n"+id+"\n" {
			t.Fatalf("%s: %s %v", id, out, err)
		}
	}
	out, err := exec.Command(entry).Output()
	if err != nil || !strings.HasPrefix(string(out), "new\ndispatch-entry\ncodex\n") {
		t.Fatalf("old managed entry: %s %v", out, err)
	}
	data, _ := os.ReadFile(custom)
	if string(data) != "custom user launcher" {
		t.Fatal("custom wrapper overwritten")
	}
	resolved, err := launcher.OriginalEntry(entry)
	canonical, _ := filepath.EvalSymlinks(original)
	if err != nil || resolved != canonical {
		t.Fatalf("native executable lost: %s %v", resolved, err)
	}
}
