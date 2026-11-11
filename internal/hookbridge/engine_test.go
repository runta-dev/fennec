package hookbridge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisteredCommandRequiresPermissionAndPreservesResult(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat\nprintf 'diagnostic' >&2\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	e, err := NewCommand(Definition{Name: "example", Agent: "codex", Event: "PreToolUse", Command: "fixture", Location: "local", Executable: "/bin/sh", Arguments: []string{"hook.sh"}, Directory: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := Execute(context.Background(), "owner", "runtime", e, nil, nil); r.Code != 2 {
		t.Fatal(r)
	}
	authorize := func(context.Context, string, string) (context.Context, func(), error) {
		return context.Background(), func() {}, nil
	}
	r := Execute(context.Background(), "owner", "runtime", e, []byte("raw input"), authorize)
	if string(r.Stdout) != "raw input" || string(r.Stderr) != "diagnostic" || r.Code != 7 || CodexExit(e.Binding(), r) != 2 {
		t.Fatal(r)
	}
	if err := os.WriteFile(script, []byte("exit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if r := Execute(context.Background(), "owner", "runtime", e, nil, authorize); r.Status != "blocked" {
		t.Fatal("changed relative script executed", r)
	}
}
func TestRequiredHookCannotAcceptSkippedResult(t *testing.T) {
	if r := Normalize(Binding{Policy: Required}, Result{Status: "skipped"}); r.Code != 2 {
		t.Fatal(r)
	}
	if r := Normalize(Binding{Policy: Observer}, Unregistered()); r.Code != 0 || r.Status != "skipped" {
		t.Fatal(r)
	}
}
