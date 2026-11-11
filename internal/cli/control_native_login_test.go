package cli

import (
	"context"
	"fennec/internal/setup"
	"os"
	"path/filepath"
	"testing"
)

type accountReconcileCounter struct{ Calls int }

func (c *accountReconcileCounter) Reconcile(context.Context) (any, error) { c.Calls++; return nil, nil }
func TestNativeLoginCompletionDoesNotRequireUI(t *testing.T) {
	home := t.TempDir()
	newDesktopSettingsWithMagpie(home, func() bool { return false })
	service := daemonServices{home: home, user: "owner", config: brokerConfiguration{RuntimeID: "runtime"}}
	counter := &accountReconcileCounter{}
	if err := os.WriteFile(checkpointPath(home), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.acceptNativeAccounts(context.Background(), "runtime", map[string]string{"codex": "login-first"}, counter); err != nil {
			t.Fatal(err)
		}
	}
	if counter.Calls != 1 {
		t.Fatalf("completion must reconcile once, got %d", counter.Calls)
	}
	if _, err := os.Stat(checkpointPath(home)); !os.IsNotExist(err) {
		t.Fatal("stale preparation checkpoint retained")
	}
	if setup.NativeCloudLogins(filepath.Join(home, ".config/fennec"), "owner", "runtime")["codex"] != "login-first" {
		t.Fatal("completed cloud identity not retained")
	}
	if err := service.acceptNativeAccounts(context.Background(), "runtime", map[string]string{"codex": "login-second"}, counter); err != nil {
		t.Fatal(err)
	}
	if counter.Calls != 2 {
		t.Fatal("replacement account was not reconciled")
	}
	if err := service.acceptNativeAccounts(context.Background(), "other-runtime", map[string]string{"codex": "login-third"}, counter); err == nil {
		t.Fatal("cross-runtime completion accepted")
	}
}
