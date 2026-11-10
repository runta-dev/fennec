package syncengine

import (
	"encoding/json"
	"testing"
)

func TestReadinessRequiresConflictFreeConnectedCycle(t *testing.T) {
	s := Session{Mode: "two-way-safe", Status: "watching", SuccessfulCycles: 1, Alpha: Endpoint{Connected: true, Scanned: true}, Beta: Endpoint{Connected: true, Scanned: true}}
	if !s.Ready() {
		t.Fatal("valid cycle rejected")
	}
	s.Conflicts = []json.RawMessage{json.RawMessage(`{}`)}
	if s.Ready() {
		t.Fatal("conflict marked ready")
	}
	s.Conflicts = nil
	s.Beta.Connected = false
	if s.Ready() {
		t.Fatal("disconnected sync marked current")
	}
}
func TestNamesSeparateUsersAndDestinations(t *testing.T) {
	a, _ := SessionName("alice", "/project", "machine:/project")
	b, _ := SessionName("bob", "/project", "machine:/project")
	c, _ := SessionName("alice", "/project", "other:/project")
	if a == b || a == c {
		t.Fatal("scope collision")
	}
}

func TestConfigurationRejectsEndpointAndPolicyDrift(t *testing.T) {
	s := Session{Mode: "two-way-safe", Alpha: Endpoint{Protocol: "local", Path: "/project"}, Beta: Endpoint{Protocol: "ssh", Host: "fennec-cloud", Path: "/cloud"}, Ignore: IgnorePolicy{VCS: true, Paths: append([]string(nil), defaultIgnores...)}, Symlink: SymlinkPolicy{Mode: "ignore"}}
	if !matchesConfiguration(s, "/project", "fennec-cloud:/cloud") {
		t.Fatal("valid policy rejected")
	}
	changed := s
	changed.Beta.Path = "/wrong"
	if matchesConfiguration(changed, "/project", "fennec-cloud:/cloud") {
		t.Fatal("wrong target accepted")
	}
	changed = s
	changed.Ignore.Paths = []string{".env"}
	if matchesConfiguration(changed, "/project", "fennec-cloud:/cloud") {
		t.Fatal("weaker ignore policy accepted")
	}
	changed = s
	changed.Symlink.Mode = "posix-raw"
	if matchesConfiguration(changed, "/project", "fennec-cloud:/cloud") {
		t.Fatal("symlink policy drift accepted")
	}
}

func TestFlushRejectsKnownBlockedStates(t *testing.T) {
	for _, s := range []Session{{Paused: true}, {LastError: "alpha scan error: exceeded allowed entry count"}, {Alpha: Endpoint{ScanProblems: []json.RawMessage{json.RawMessage(`{}`)}}}} {
		if err := flushBlocker(s); err == nil {
			t.Fatal("blocked flush accepted", s)
		}
	}
	if err := flushBlocker(Session{Status: "scanning"}); err != nil {
		t.Fatal("healthy initial scan rejected", err)
	}
}
