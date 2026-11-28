package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/board"
	"fennec/internal/control"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueueReplicasAreIsolatedAndSwitchBackRestoresOriginal(t *testing.T) {
	s := &daemonServices{home: t.TempDir(), user: "owner"}
	s.config.RuntimeID = "machine-a"
	a := s.localBoard()
	if _, err := a.AddStarterProject("A", ""); err != nil {
		t.Fatal(err)
	}
	client := boardClient{s: s, local: true, scope: a.Namespace}
	path := "/v1/projects/" + board.StarterProjectID + "/requests"
	if _, err := client.call(context.Background(), "POST", path, map[string]string{"title": "Only A", "author": "me"}); err != nil {
		t.Fatal(err)
	}
	oldJournal, err := os.ReadFile(filepath.Join(a.Root(), "replica.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.config.RuntimeID = "machine-b"
	b := s.localBoard()
	if a.Root() == b.Root() {
		t.Fatal("machines share a replica")
	}
	projects, err := b.Projects()
	if err != nil || len(projects) != 0 {
		t.Fatalf("new computer is not empty: %v %v", projects, err)
	}
	if _, err := b.AddStarterProject("B", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.call(context.Background(), "POST", path, map[string]string{"title": "Late A UI write", "author": "me"}); err == nil {
		t.Fatal("stale UI mutated B")
	} else {
		var fault *control.Fault
		if !errors.As(err, &fault) || fault.Code != "board_changed" {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	// An already running local agent keeps its captured store, even after a switch.
	late := boardClient{s: s, local: true, store: &a}
	if _, err := late.call(context.Background(), "POST", path, map[string]string{"title": "Late A result", "author": "agent"}); err != nil {
		t.Fatal(err)
	}
	requests, err := b.Requests(board.StarterProjectID)
	if err != nil || len(requests) != 0 {
		t.Fatalf("A's reply leaked to B: %v %v", requests, err)
	}
	s.config.RuntimeID = "machine-a"
	restored := s.localBoard()
	requests, err = restored.Requests(board.StarterProjectID)
	if err != nil || len(requests) != 2 || requests[0].Title != "Only A" {
		t.Fatalf("switch back lost A: %v %v", requests, err)
	}
	var before, after board.ReplicaState
	if err := json.Unmarshal(oldJournal, &before); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(restored.Root(), "replica.json"))
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	if before.ID != after.ID || len(after.Events) <= len(before.Events) {
		t.Fatal("switching replaced the original journal")
	}
	if b.SyncStatus() == board.SyncBlockedOtherComputer {
		t.Fatal("fresh replica shows migration warning")
	}
}

func TestQueueNamespaceIncludesEnvironmentOwnerAndComputer(t *testing.T) {
	seen := map[string]bool{}
	for _, parts := range [][3]string{{"production", "alice", "one"}, {"integration", "alice", "one"}, {"production", "bob", "one"}, {"production", "alice", "two"}} {
		id := boardNamespace(parts[0], parts[1], parts[2])
		if !boardScopeID.MatchString(id) || seen[id] {
			t.Fatal("scope collision", parts)
		}
		seen[id] = true
	}
}

func TestLegacyQueueIsPreservedWithoutJoiningNewComputer(t *testing.T) {
	home := t.TempDir()
	legacy := board.Store{Home: home, Role: "local"}
	if _, err := legacy.AddStarterProject("Old computer", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy.Root(), "paired-account"), []byte("old-account"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &daemonServices{home: home, user: "owner"}
	s.config.RuntimeID = "new-computer"
	current := s.localBoard()
	if rows, err := current.Projects(); err != nil || len(rows) != 0 {
		t.Fatalf("legacy data copied to new machine: %v %v", rows, err)
	}
	if rows, err := legacy.Projects(); err != nil || len(rows) != 1 {
		t.Fatal("legacy replica lost", rows, err)
	}
	if got, err := os.ReadFile(filepath.Join(legacy.Root(), "paired-account")); err != nil || string(got) != "old-account" {
		t.Fatal("legacy pairing changed")
	}
}

// FEN-231: after a restart, before the cloud verifies the account again, the
// window reads the board this same credential verified to last time, so it
// opens on its projects. Replication and the scheduler wait for verification,
// and another credential (or none) never sees the remembered board.
func TestBoardScopeRememberedForTheSameCredential(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	daemon := func(token string) *daemonServices {
		manager := account.New(ctx, &tokenCredentials{token: token}, "https://runta.example")
		_ = manager.Restore(ctx)
		return &daemonServices{home: home, account: manager}
	}
	first := daemon("token-a")
	_, _, epoch, ok := first.account.Session()
	if !ok {
		t.Fatal("not signed in")
	}
	first.epoch, first.user, first.config.RuntimeID = epoch, "owner", "machine-a"
	verified := first.localBoard().Namespace
	if verified == "local" || first.verifiedBoard().Namespace != verified {
		t.Fatalf("verified scope = %q", verified)
	}

	restarted := daemon("token-a")
	if got := restarted.localBoard().Namespace; got != verified {
		t.Fatalf("restart before verification reads %q, want %q", got, verified)
	}
	if got := restarted.verifiedBoard().Namespace; got != "local" {
		t.Fatalf("replication used the remembered scope %q", got)
	}
	for _, token := range []string{"token-b", ""} {
		if got := daemon(token).localBoard().Namespace; got != "local" {
			t.Fatalf("credential %q reads remembered scope %q", token, got)
		}
	}
	data, err := os.ReadFile(boardScopePath(home))
	if err != nil || strings.Contains(string(data), "token-a") {
		t.Fatalf("board-scope.json = %s, %v", data, err)
	}
}
