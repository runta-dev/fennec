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
	if len(got) != 3 || got[0][0] != 42 || got[1][0] != 7 || got[2][0] != 3 || got[0][1] != 8 {
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
	for _, want := range []string{"id: DEL-2\n", "status: queued\n", "backlinks: [DEL-3 (duplicate)]\n", "- has duplicate DEL-3 [queued] Sync stall, again\n", "Looks like DEL-1 again"} {
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

func TestMentionsReadSlashShorthand(t *testing.T) {
	pattern := mentionPattern("THI")
	text := "正在跑的请求（THI-73/103/107）。THI-5/6x, THI-8/ and THI-9/10/THI-11"
	got := mentions(pattern, text)
	want := []int{73, 103, 107, 5, 8, 9, 10, 11}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i, n := range want {
		if got[i][0] != n {
			t.Fatalf("%v", got)
		}
	}
	head := strings.Index(text, "THI-73") + 4
	if got[1][1] != head || got[2][1] != head {
		t.Fatalf("offsets %v, want %d", got, head)
	}
}

func TestExcerptShowsTheLineAroundAMention(t *testing.T) {
	text := "Intro line\n- **Waiting on you** (`you`): THI-41, 59, 61\nlast"
	if got := excerpt(text, strings.Index(text, "THI-41")); got != "Waiting on you (you): THI-41, 59, 61" {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("a", 100) + " THI-41 " + strings.Repeat("b", 200)
	got := excerpt(long, strings.Index(long, "THI-41"))
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !strings.Contains(got, "THI-41") || len([]rune(got)) > excerptRunes+2 {
		t.Fatalf("%q", got)
	}
}

func TestMentionsShowWhereTheyWereWritten(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Key: "DEL"})
	a, _ := s.CreateRequest(p.ID, "me", "Sync stalls", "bug", "")
	b, _ := s.CreateRequest(p.ID, "me", "Queue freezes", "bug", "Looks like DEL-1")
	s.Reply(p.ID, b.Number, "me", "", "Notes\n- waiting on DEL-1 still")
	// DEL-1 lists DEL-2 once, with both places it was mentioned.
	related, _ := s.Related(p.ID, a.Number)
	if len(related) != 1 || len(related[0].Mentions) != 2 || related[0].Mentions[0].Message != 1 || related[0].Mentions[1].Excerpt != "waiting on DEL-1 still" {
		t.Fatalf("%+v", related)
	}
	// DEL-2's own mention is a link in its text, not a row.
	if related, _ = s.Related(p.ID, b.Number); len(related) != 0 {
		t.Fatalf("%+v", related)
	}
	thread, _ := os.ReadFile(filepath.Join(s.ThreadsPath(p.ID), "DEL-1.md"))
	if !strings.Contains(string(thread), "- mentioned by DEL-2 [queued] Queue freezes\n  - me, ") || !strings.Contains(string(thread), ": waiting on DEL-1 still\n") {
		t.Fatal(string(thread))
	}
}

func TestMentionsReadLists(t *testing.T) {
	pattern := mentionPattern("THI")
	cases := map[string][]int{
		"之后 main 上又进了 4 个提交（THI-107、117、119、121），全是 desktop": {107, 117, 119, 121},
		"THI-37、39、45、47 都停在 t":                              {37, 39, 45, 47},
		"Waiting on you: THI-41, 59, 61":                     {41, 59, 61},
		"THI-39 / 63 / 69 / 73 先在一边":                         {39, 63, 69, 73},
		"THI-69 / 79 / 81，cloud":                             {69, 79, 81},
		"THI-117 和 119 都修了":                                  {117, 119},
		"THI-117和119":                                        {117, 119},
		"THI-41, 59 and 61.":                                 {41, 59, 61},
		"THI-41, #59 or 61":                                  {41, 59, 61},
		"THI-107~121 之间":                                     {107, 121},
		"THI-5、THI-7 和 9":                                    {5, 7, 9},
		"**THI-13、17**":                                      {13, 17},
		// Counts, dates, versions and words after a reference are not requests.
		"THI-41, 3 files":          {41},
		"THI-41、3 个文件":             {41},
		"THI-41和3个提交":              {41},
		"THI-131, 2026-11-25":      {131},
		"THI-41, 0.1.13 (build)":   {41},
		"THI-41, 59, 3 files":      {41, 59},
		"THI-41 and 2 more":        {41},
		"THI-41, 07:50 UTC":        {41},
		"THI-41 59":                {41},
		"see thi-35 and fix/thi-3": {},
	}
	for text, want := range cases {
		got := mentions(pattern, text)
		if len(got) != len(want) {
			t.Errorf("%q: %v, want %v", text, got, want)
			continue
		}
		for i, n := range want {
			if got[i][0] != n {
				t.Errorf("%q: %v, want %v", text, got, want)
			}
		}
	}
	text := "x THI-107、117"
	if got := mentions(pattern, text); got[1][1] != strings.Index(text, "THI-107")+4 {
		t.Fatalf("offsets %v", got)
	}
}

// THI-191: a request linked to n that also mentions n is one row, not a
// Related row plus a Mentioned in row.
func TestALinkAndAMentionOfTheSameRequestAreOneRow(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProject(Project{Name: "Delivery", Key: "DEL"})
	a, _ := s.CreateRequest(p.ID, "me", "Public beta", "feature", "")
	b, _ := s.CreateRequest(p.ID, "me", "Privacy page", "feature", "From DEL-1: needed for review")
	c, _ := s.CreateRequest(p.ID, "me", "Old beta plan", "feature", "Same as DEL-1")
	if _, err := s.SetLink(p.ID, a.Number, b.Number, LinkRelated, "me", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLink(p.ID, a.Number, c.Number, LinkRelated, "me", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLink(p.ID, c.Number, a.Number, LinkDuplicate, "me", "", ""); err != nil {
		t.Fatal(err)
	}
	related, _ := s.Related(p.ID, a.Number)
	if len(related) != 2 {
		t.Fatal(related)
	}
	byNumber := map[int]Related{}
	for _, r := range related {
		byNumber[r.Number] = r
	}
	if r := byNumber[b.Number]; r.Kind != LinkRelated || r.Incoming || len(r.Mentions) != 1 || !strings.Contains(r.Mentions[0].Excerpt, "needed for review") {
		t.Fatal(r)
	}
	// The stronger link wins: DEL-3 was merged into DEL-1.
	if r := byNumber[c.Number]; r.Kind != LinkDuplicate || !r.Incoming {
		t.Fatal(r)
	}
}
