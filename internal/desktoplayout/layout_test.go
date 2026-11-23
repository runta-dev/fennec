package desktoplayout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixture() []Tab {
	return []Tab{{"local-a", "t1", "one"}, {"local-b", "local:t2", "one"}, {"cloud-c", "cloud:t1", "two"}}
}
func move(t *testing.T, s *Store, tabs []Tab, rev uint64, op, tab, target, before string) State {
	t.Helper()
	out, e := s.Apply(Input{Tabs: tabs, Revision: rev, Operation: op, Tab: tab, Target: target, Before: before})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestMergeReorderDetachRestore(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	tabs := fixture()
	out := move(t, s, tabs, 0, "move", "cloud:t1", "one", "local:t2")
	if len(out.Groups) != 1 || !reflect.DeepEqual(out.Groups[0].Tabs, []string{"t1", "cloud:t1", "local:t2"}) {
		t.Fatal(out)
	}
	out = move(t, s, tabs, out.Revision, "move", "t1", "one", "")
	if !reflect.DeepEqual(out.Groups[0].Tabs, []string{"cloud:t1", "local:t2", "t1"}) {
		t.Fatal(out)
	}
	out = move(t, s, tabs, out.Revision, "detach", "cloud:t1", "", "")
	if len(out.Groups) != 2 || out.Groups[1].Active != "cloud:t1" {
		t.Fatal(out)
	}
	restored, e := New(home).Apply(Input{Tabs: tabs})
	if e != nil || !reflect.DeepEqual(out, restored) {
		t.Fatal(out, restored, e)
	}
}
func TestDisconnectedMembersAndStaleRevision(t *testing.T) {
	s := New(t.TempDir())
	tabs := fixture()
	out := move(t, s, tabs, 0, "move", "cloud:t1", "one", "")
	out = move(t, s, tabs[:2], out.Revision, "detach", "local:t2", "", "")
	out = move(t, s, tabs, out.Revision, "", "", "", "")
	if !reflect.DeepEqual(out.Groups[0].Tabs, []string{"t1", "cloud:t1"}) {
		t.Fatal(out)
	}
	if _, e := s.Apply(Input{Tabs: tabs, Operation: "move", Tab: "t1", Target: out.Groups[1].ID, Revision: 0}); e == nil {
		t.Fatal("stale mutation accepted")
	}
}
func TestMissingFirstTabAndCollidingAddresses(t *testing.T) {
	s := New(t.TempDir())
	tabs := fixture()
	out := move(t, s, tabs, 0, "move", "cloud:t1", "one", "")
	out = move(t, s, tabs[1:], out.Revision, "", "", "", "")
	if len(out.Groups) != 1 || out.Groups[0].Active != "cloud:t1" {
		t.Fatal(out)
	}
	replacement := append([]Tab{}, tabs...)
	replacement[0].ID = "new-local-a"
	replacement[0].Group = "new-one"
	out = move(t, s, replacement, out.Revision, "", "", "", "")
	if len(out.Groups) != 2 {
		t.Fatal("replacement inherited old placement", out)
	}
}
func TestInvalidDropDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	if _, e := s.Apply(Input{Tabs: fixture(), Operation: "move", Tab: "t1", Target: "missing"}); e == nil {
		t.Fatal("invalid target accepted")
	}
	if _, e := os.Stat(filepath.Join(home, ".config/fennec/desktop-layout.json")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestSavedLayoutStoresIdentitiesNotTransportKeys(t *testing.T) {
	home := t.TempDir()
	s := New(home)
	move(t, s, fixture(), 0, "activate", "local:t2", "", "")
	b, e := os.ReadFile(filepath.Join(home, ".config/fennec/desktop-layout.json"))
	if e != nil {
		t.Fatal(e)
	}
	var saved State
	if json.Unmarshal(b, &saved) != nil || saved.Groups[0].Active != "local-b" {
		t.Fatal(string(b))
	}
}

func TestRepeatedDetachHasUniqueGroups(t *testing.T) {
	s := New(t.TempDir())
	tabs := fixture()
	out := move(t, s, tabs, 0, "detach", "local:t2", "", "")
	old := out.Groups[len(out.Groups)-1].ID
	out = move(t, s, tabs, out.Revision, "move", "t1", old, "")
	out = move(t, s, tabs, out.Revision, "detach", "local:t2", "", "")
	seen := map[string]bool{}
	for _, g := range out.Groups {
		if seen[g.ID] {
			t.Fatal("duplicate group", out)
		}
		seen[g.ID] = true
	}
}

func TestOfflineGroupKeepsOrderAndActiveTab(t *testing.T) {
	s := New(t.TempDir())
	tabs := fixture()
	out := move(t, s, tabs, 0, "activate", "cloud:t1", "", "")
	out = move(t, s, tabs[:2], out.Revision, "activate", "local:t2", "", "")
	out = move(t, s, tabs, out.Revision, "", "", "", "")
	if len(out.Groups) != 2 || out.Groups[1].Active != "cloud:t1" {
		t.Fatal(out)
	}
}
