package syncengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type migrationControl struct {
	sessions              map[string]Session
	calls                 []bool
	failPause, failResume bool
}

func (c *migrationControl) Inspect(_ context.Context, name string) ([]Session, error) {
	s, ok := c.sessions[name]
	if !ok {
		return nil, errors.New("unexpected session")
	}
	return []Session{s}, nil
}
func (c *migrationControl) SetPaused(ctx context.Context, name string, paused bool) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	c.calls = append(c.calls, paused)
	if paused && c.failPause || !paused && c.failResume {
		return Session{}, errors.New("interrupted transition")
	}
	s := c.sessions[name]
	s.Paused = paused
	c.sessions[name] = s
	return s, nil
}

func migrationEntry(t *testing.T) (string, Registration) {
	t.Helper()
	root := t.TempDir()
	return filepath.Join(root, "registry"), Registration{Root: root, Session: "fennec-test", DataDirectory: filepath.Join(root, "state/data"), SSHDirectory: filepath.Join(root, "state/ssh")}
}

func registeredTransport(t *testing.T, registry string) Registration {
	t.Helper()
	entries, err := ListProjects(registry)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	return entries[0]
}

func TestManagedMigrationPreservesPausedStateAndScope(t *testing.T) {
	registry, entry := migrationEntry(t)
	if err := Register(registry, entry); err != nil {
		t.Fatal(err)
	}
	other := entry
	other.Session = "fennec-other"
	other.DataDirectory = filepath.Join(entry.Root, "other/data")
	other.SSHDirectory = filepath.Join(entry.Root, "other/ssh")
	if err := Register(registry, other); err != nil {
		t.Fatal(err)
	}
	c := &migrationControl{sessions: map[string]Session{entry.Session: {Name: entry.Session, Mode: "two-way-safe", Paused: true}}}
	m := Manager{DataDirectory: entry.DataDirectory, SSHDirectory: entry.SSHDirectory}
	if err := m.migrateManagedSessions(context.Background(), registry, "/tmp/fixture-fen", "owner", managedTestRuntime, c); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 0 {
		t.Fatal("paused synchronization resumed", c.calls)
	}
	entries, err := ListProjects(registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Session == entry.Session && e.Transport != ManagedTransport || e.Session == other.Session && e.Transport != "" {
			t.Fatal("scope or marker changed incorrectly", e)
		}
	}
	if _, err := os.Stat(other.SSHDirectory); !os.IsNotExist(err) {
		t.Fatal("another scope's wrapper changed", err)
	}
}

func TestManagedMigrationRecoversInterruptedResume(t *testing.T) {
	registry, entry := migrationEntry(t)
	c := &migrationControl{sessions: map[string]Session{entry.Session: {Name: entry.Session, Mode: "two-way-safe"}}, failResume: true}
	if err := migrateManagedSession(context.Background(), registry, entry, c); err == nil {
		t.Fatal("interruption hidden")
	}
	pending := registeredTransport(t, registry)
	if pending.Transport != managedResume || !c.sessions[entry.Session].Paused {
		t.Fatal("lost original active intent", pending)
	}
	c.failResume = false
	if err := migrateManagedSession(context.Background(), registry, pending, c); err != nil {
		t.Fatal(err)
	}
	if c.sessions[entry.Session].Paused || registeredTransport(t, registry).Transport != ManagedTransport {
		t.Fatal("active session left paused")
	}
}

func TestUserPauseOverridesPendingMigrationResume(t *testing.T) {
	registry, entry := migrationEntry(t)
	entry.Transport = managedResume
	if err := Register(registry, entry); err != nil {
		t.Fatal(err)
	}
	c := &migrationControl{sessions: map[string]Session{entry.Session: {Name: entry.Session, Mode: "two-way-safe", Paused: true}}, failPause: true}
	if _, err := setRegisteredPaused(context.Background(), registry, entry.DataDirectory, entry.Session, true, c); err == nil {
		t.Fatal("failed pause hidden")
	}
	pending := registeredTransport(t, registry)
	if pending.Transport != managedPause {
		t.Fatal("explicit user intent lost", pending.Transport)
	}
	c.failPause = false
	c.calls = nil
	if err := migrateManagedSession(context.Background(), registry, pending, c); err != nil {
		t.Fatal(err)
	}
	if !c.sessions[entry.Session].Paused || len(c.calls) != 1 || !c.calls[0] {
		t.Fatal("migration overrode user pause", c.calls)
	}
}
