package board

import "testing"

// A Mac replica that belongs to another cloud computer says so instead of
// looking merely offline (THI-188).
func TestSyncBlockedOverridesStatus(t *testing.T) {
	store := Store{Home: t.TempDir(), Role: "local"}
	if err := store.SetSyncBlocked(SyncBlockedOtherComputer); err != nil {
		t.Fatal(err)
	}
	if got := store.SyncStatus(); got != SyncBlockedOtherComputer {
		t.Fatalf("status %q", got)
	}
	if err := store.SetSyncBlocked(""); err != nil {
		t.Fatal(err)
	}
	if got := store.SyncStatus(); got != "offline" {
		t.Fatalf("status after clearing %q", got)
	}
	if err := store.SetSyncBlocked(""); err != nil {
		t.Fatal("clearing twice:", err)
	}
	if (Store{Home: store.Home, Role: "cloud"}).SyncStatus() != "synced" {
		t.Fatal("the cloud is never blocked")
	}
}
