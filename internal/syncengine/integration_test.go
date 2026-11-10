package syncengine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMutagenManagerIntegration(t *testing.T) {
	if os.Getenv("FENNEC_MUTAGEN_INTEGRATION") != "1" {
		t.Skip("explicit isolated Mutagen integration")
	}
	binary, err := exec.LookPath("mutagen")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "fennec-sync-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	a := filepath.Join(root, "alpha")
	b := filepath.Join(root, "beta")
	os.MkdirAll(a, 0700)
	os.MkdirAll(b, 0700)
	os.WriteFile(filepath.Join(a, "source.txt"), []byte("baseline"), 0600)
	os.WriteFile(filepath.Join(a, ".env"), []byte("test-secret"), 0600)
	os.WriteFile(filepath.Join(a, ".gitignore"), []byte("artifacts/\n*.tmp\n"), 0600)
	os.MkdirAll(filepath.Join(a, "artifacts"), 0700)
	os.WriteFile(filepath.Join(a, "artifacts", "initial.bin"), []byte("ignored"), 0600)
	if err := exec.Command("git", "-C", a, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"src/deep", "src/cache", "other"} {
		os.MkdirAll(filepath.Join(a, dir), 0700)
	}
	os.WriteFile(filepath.Join(a, "src/.gitignore"), []byte("*.generated\n/cache/\n"), 0600)
	for _, name := range []string{"src/one.generated", "src/deep/two.generated", "src/cache/output", "other/keep.generated"} {
		os.WriteFile(filepath.Join(a, name), []byte("fixture"), 0600)
	}
	manager := Manager{Binary: binary, DataDirectory: filepath.Join(root, "state")}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		manager.run(cleanup, "sync", "terminate", "--all")
		manager.run(cleanup, "daemon", "stop")
	}()
	session, err := manager.Ensure(ctx, "test-user", a, b)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := manager.Ensure(ctx, "test-user", a, b)
	if err != nil || repeated.Identifier != session.Identifier {
		t.Fatalf("session not reused: %v", err)
	}
	ready, err := manager.Flush(ctx, session.Name)
	if err != nil || !ready.Ready() {
		t.Fatalf("not ready: %+v %v", ready, err)
	}
	data, err := os.ReadFile(filepath.Join(b, "source.txt"))
	if err != nil || string(data) != "baseline" {
		t.Fatal("source not synchronized", err)
	}
	if _, err = os.Stat(filepath.Join(b, ".env")); !os.IsNotExist(err) {
		t.Fatal("excluded credential synchronized")
	}
	for _, name := range []string{"artifacts/initial.bin"} {
		if _, err := os.Stat(filepath.Join(b, name)); !os.IsNotExist(err) {
			t.Fatal("initial artifact synchronized")
		}
	}
	os.WriteFile(filepath.Join(a, "later.tmp"), []byte("ignored later"), 0600)
	os.WriteFile(filepath.Join(a, "artifacts", "later.bin"), []byte("ignored later"), 0600)
	os.WriteFile(filepath.Join(b, "remote.tmp"), []byte("ignored remote"), 0600)
	if _, err := manager.Flush(ctx, session.Name); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"later.tmp", "artifacts/later.bin"} {
		if _, err := os.Stat(filepath.Join(b, name)); !os.IsNotExist(err) {
			t.Fatal("new artifact synchronized", name)
		}
	}
	if _, err := os.Stat(filepath.Join(a, "remote.tmp")); !os.IsNotExist(err) {
		t.Fatal("remote artifact synchronized")
	}
	for _, name := range []string{"src/one.generated", "src/deep/two.generated", "src/cache/output"} {
		if err := exec.Command("git", "-C", a, "check-ignore", "-q", name).Run(); err != nil {
			t.Fatal("Git fixture not ignored", name)
		}
		if _, err := os.Stat(filepath.Join(b, name)); !os.IsNotExist(err) {
			t.Fatal("nested ignored file synchronized", name)
		}
	}
	if _, err := os.Stat(filepath.Join(b, "other/keep.generated")); err != nil {
		t.Fatal("nested rule escaped subtree", err)
	}
	paused, err := manager.SetPaused(ctx, session.Name, true)
	if err != nil || !paused.Paused {
		t.Fatal("pause failed", err)
	}
	os.WriteFile(filepath.Join(b, "offline.txt"), []byte("cloud work"), 0600)
	resumed, err := manager.SetPaused(ctx, session.Name, false)
	if err != nil || resumed.Paused {
		t.Fatal("resume failed", err)
	}
	if _, err = manager.Flush(ctx, session.Name); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(a, "offline.txt"))
	if err != nil || string(content) != "cloud work" {
		t.Fatal("offline changes lost", err)
	}

	if err = manager.Remove(ctx, session.Name); err != nil {
		t.Fatal("removal failed", err)
	}
	for _, endpoint := range []string{a, b} {
		data, err := os.ReadFile(filepath.Join(endpoint, "source.txt"))
		if err != nil || string(data) != "baseline" {
			t.Fatal("removal altered files", err)
		}
	}

}
