package cloudstate

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDeletionSurvivesRestartAndRequiresExplicitRecreation(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	target := Record{Owner: "owner", Runtime: "original", Request: "confirmed-plan"}
	if err := s.Check(target.Owner); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(target); err != nil {
		t.Fatal(err)
	}
	// A newly constructed store represents a process restart after a lost reply.
	restarted := New(home)
	if !errors.Is(restarted.Check("owner"), ErrDisabled) {
		t.Fatal("pending deletion allowed automatic recreation")
	}
	if err := restarted.Check("other-owner"); err != nil {
		t.Fatal("another account was blocked", err)
	}
	if restarted.AllowCreation("owner", "original", "confirmed-plan") == nil {
		t.Fatal("unknown deletion outcome allowed creation")
	}
	if restarted.Confirm("owner", "replacement", "confirmed-plan") == nil {
		t.Fatal("replacement runtime accepted")
	}
	if restarted.Confirm("owner", "original", "different-plan") == nil {
		t.Fatal("stale confirmation accepted")
	}
	if err := restarted.Confirm("owner", "original", "confirmed-plan"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(New(home).Check("owner"), ErrDisabled) {
		t.Fatal("confirmed deletion allowed automatic recreation")
	}
	if err := restarted.Begin(target); err != nil {
		t.Fatal(err)
	}
	record, err := restarted.Read("owner")
	if err != nil || !record.Deleted {
		t.Fatal("retry downgraded confirmed deletion", record, err)
	}
	if err := restarted.AllowCreation("owner", "original", "confirmed-plan"); err != nil {
		t.Fatal(err)
	}
	if err := New(home).Check("owner"); err != nil {
		t.Fatal(err)
	}
}
func TestCorruptAndForeignRecordsFailClosed(t *testing.T) {
	s := New(t.TempDir())
	target := Record{Owner: "owner", Runtime: "original", Request: "plan"}
	if err := s.Begin(target); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `null`, `{"owner":"other","runtime":"original","request":"plan"}`, `not-json`} {
		if err := os.WriteFile(s.path("owner"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if s.Check("owner") == nil {
			t.Fatal("corrupt record allowed recreation")
		}
	}
}
func TestConflictingDeletionPlansCannotOverwrite(t *testing.T) {
	s := New(t.TempDir())
	var succeeded atomic.Int32
	var workers sync.WaitGroup
	for _, id := range []string{"first", "second"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			if s.Begin(Record{Owner: "owner", Runtime: id, Request: id}) == nil {
				succeeded.Add(1)
			}
		}(id)
	}
	workers.Wait()
	if succeeded.Load() != 1 {
		t.Fatal("conflicting plans both persisted")
	}
}

func TestLocalCleanupDoesNotEraseRemoteDeletionIntent(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	target := Record{Owner: "owner", Runtime: "original", Request: "plan", Revision: 7}
	if err := s.Begin(target); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".config/fennec", ".local/share/fennec"} {
		if err := os.MkdirAll(filepath.Join(home, path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(home, path)); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(New(home).Check("owner"), ErrDisabled) {
		t.Fatal("local cleanup allowed an unconfirmed cloud replacement")
	}
	if err := s.Confirm("owner", "original", "plan"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(New(home).Check("owner"), ErrDisabled) {
		t.Fatal("reinstall silently recreated an intentionally deleted computer")
	}
	if err := s.AllowCreation("owner", "original", "plan"); err != nil {
		t.Fatal(err)
	}
	if err := New(home).Check("owner"); err != nil {
		t.Fatal("explicit recreation did not clear the guard", err)
	}
}
