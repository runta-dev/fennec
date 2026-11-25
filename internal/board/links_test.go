package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMentionsMatchOnlyWholeReferences(t *testing.T) {
	pattern := mentionPattern("THI")
	got := mentions(pattern, "See THI-42 and (THI-7), not XTHI-9 or THI-42x; THI-42 again.\nTHI-3")
	if len(got) != 3 || got[0] != 42 || got[1] != 7 || got[2] != 3 {
		t.Fatal(got)
	}
}

func TestLinksShowOnBothThreadsAndInTheMirror(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Key: "DEL"})
	a, _ := s.CreateRequest(p.ID, "me", "Sync stalls after restart", "bug", "")
	b, _ := s.CreateRequest(p.ID, "me", "Queue list freezes", "bug", "Looks like DEL-1 again")
	c, _ := s.CreateRequest(p.ID, "me", "Sync stall, again", "bug", "")

	// A mention is a link: DEL-2 mentions DEL-1, DEL-1 shows the backlink.
	related, err := s.Related(p.ID, a.Number)
	if err != nil || len(related) != 1 || related[0].Number != b.Number || related[0].Kind != "mention" || !related[0].Incoming {
		t.Fatal(related, err)
	}

	// Merging DEL-3 into DEL-1 is a duplicate link, seen from both sides.
	if _, err = s.SetLink(p.ID, c.Number, a.Number, LinkDuplicate, "me", "", ""); err != nil {
		t.Fatal(err)
	}
	merged, _ := s.Request(p.ID, c.Number)
	if merged.DuplicateOf() != a.Number {
		t.Fatal(merged.Links)
	}
	related, _ = s.Related(p.ID, a.Number)
	if len(related) != 2 || related[0].Number != c.Number || related[0].Kind != LinkDuplicate || !related[0].Incoming {
		t.Fatal(related)
	}
	// Marking it a duplicate of another request moves the link.
	if _, err = s.SetLink(p.ID, c.Number, b.Number, LinkDuplicate, "me", "", ""); err != nil {
		t.Fatal(err)
	}
	if merged, _ = s.Request(p.ID, c.Number); len(merged.Links) != 1 || merged.DuplicateOf() != b.Number {
		t.Fatal(merged.Links)
	}
	if _, err = s.SetLink(p.ID, c.Number, c.Number, LinkRelated, "me", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("linked a request to itself", err)
	}
	if _, err = s.SetLink(p.ID, c.Number, 99, LinkRelated, "me", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("linked a missing request", err)
	}

	// The mirror follows every board write.
	dir := s.ThreadsPath(p.ID)
	thread, err := os.ReadFile(filepath.Join(dir, "DEL-2.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id: DEL-2\n", "status: queued\n", "links: [DEL-1 (mention)]\n", "backlinks: [DEL-3 (duplicate)]\n", "- has duplicate DEL-3 [queued] Sync stall, again\n", "Looks like DEL-1 again"} {
		if !strings.Contains(string(thread), want) {
			t.Fatalf("DEL-2.md lacks %q:\n%s", want, thread)
		}
	}
	index, _ := os.ReadFile(filepath.Join(dir, "INDEX.md"))
	if !strings.Contains(string(index), "- DEL-3 [queued] [bug] Sync stall, again\n") {
		t.Fatal(string(index))
	}

	// Removing the link clears both sides; a stray file is cleaned up.
	os.WriteFile(filepath.Join(dir, "DEL-9.md"), []byte("old"), 0600)
	if _, err = s.SetLink(p.ID, c.Number, b.Number, "", "me", "", ""); err != nil {
		t.Fatal(err)
	}
	if merged, _ = s.Request(p.ID, c.Number); merged.Links != nil {
		t.Fatal(merged.Links)
	}
	thread, _ = os.ReadFile(filepath.Join(dir, "DEL-2.md"))
	if strings.Contains(string(thread), "DEL-3") {
		t.Fatal(string(thread))
	}
	if _, err := os.Stat(filepath.Join(dir, "DEL-9.md")); !os.IsNotExist(err) {
		t.Fatal("stale mirror file kept", err)
	}
}

func TestMirrorIsRebuiltWhenMissing(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Key: "DEL"})
	s.CreateRequest(p.ID, "me", "First", "bug", "")
	os.RemoveAll(s.ThreadsPath(p.ID))
	// A write to another project still rebuilds this one's missing mirror.
	q, _ := s.CreateProject(Project{Name: "Other"})
	s.CreateRequest(q.ID, "me", "Elsewhere", "bug", "")
	if _, err := os.Stat(filepath.Join(s.ThreadsPath(p.ID), "DEL-1.md")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentLinksAndMergesItsRequest(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Key: "DEL"})
	r, _ := s.CreateRequest(p.ID, "me", "Sync stalls again", "bug", "")
	old, _ := s.CreateRequest(p.ID, "me", "Sync stalls", "bug", "")
	if _, err := s.Claim(p.ID, r.Number, "calm-fox", "codex"); err != nil {
		t.Fatal(err)
	}
	claimed, _ := s.Request(p.ID, r.Number)
	prompt := RunPrompt(p, claimed, "fennec-terminal queue")
	if !strings.Contains(prompt, "projects/"+p.ID+"/threads/") || !strings.Contains(prompt, "fennec-terminal queue merge "+p.ID+" 1 codex calm-fox DEL-<n>") {
		t.Fatal(prompt)
	}

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := AgentCommand(s.Home, args, strings.NewReader(""), &out)
		return out.String(), err
	}
	if _, err := run("link", p.ID, "1", "codex", "blue-bird", "DEL-2"); err == nil {
		t.Fatal("another run linked the request")
	}
	if _, err := run("link", p.ID, "1", "codex", "calm-fox", "FOO-2"); err == nil {
		t.Fatal("linked a request of another queue")
	}
	out, err := run("link", p.ID, "1", "codex", "calm-fox", "del-2")
	var related []Related
	if err != nil || json.Unmarshal([]byte(out), &related) != nil || len(related) != 1 || related[0].Kind != LinkRelated {
		t.Fatal(out, err)
	}
	if out, err = run("merge", p.ID, "1", "codex", "calm-fox", "#2"); err != nil || !strings.Contains(out, `"kind":"duplicate"`) {
		t.Fatal(out, err)
	}
	if got, _ := s.Related(p.ID, old.Number); len(got) != 1 || got[0].Number != 1 || got[0].Kind != LinkDuplicate || !got[0].Incoming {
		t.Fatal(got)
	}
	if out, err = run("threads", p.ID); err != nil || strings.TrimSpace(out) != s.ThreadsPath(p.ID) {
		t.Fatal(out, err)
	}
}

func TestThreadAPICarriesRelatedAndSetsLinks(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Key: "DEL"})
	s.CreateRequest(p.ID, "me", "One", "bug", "")
	s.CreateRequest(p.ID, "me", "Two", "bug", "")
	h := s.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "http://board.fennec"+path, strings.NewReader(body)))
		return w
	}
	if w := call("POST", "/v1/projects/"+p.ID+"/requests/2/links", `{"number":1,"kind":"related","author":"me"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("GET", "/v1/projects/"+p.ID+"/requests/1", "")
	var thread struct {
		Number  int       `json:"number"`
		Related []Related `json:"related"`
	}
	if json.Unmarshal(w.Body.Bytes(), &thread) != nil || thread.Number != 1 || len(thread.Related) != 1 || thread.Related[0].Number != 2 || !thread.Related[0].Incoming || thread.Related[0].Title != "Two" {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/v1/projects/"+p.ID+"/requests/2/links", `{"number":1,"kind":"blocks","author":"me"}`); w.Code != 400 {
		t.Fatal("unknown kind accepted", w.Code)
	}
}
