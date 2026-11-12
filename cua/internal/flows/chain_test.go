package flows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/protocol"
)

// clock is a test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// noGit is a store that never runs git: the Command Line Tools are absent.
func noGit(t *testing.T) (*Store, *clock) {
	t.Helper()
	ck := &clock{t: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	s, err := NewStore(filepath.Join(t.TempDir(), "flows"))
	if err != nil {
		t.Fatal(err)
	}
	s.now = ck.now
	s.devTools = func() bool { return false }
	s.git = func(args ...string) error {
		t.Errorf("git ran without the Command Line Tools: %v", args)
		return nil
	}
	return s, ck
}

func chainsOf(s *Store, ck *clock) *Chains { return &Chains{Store: s, now: ck.now} }

const notepad = "a.txt - Notepad"

func launch(app string) protocol.Step { return protocol.Step{Launch: app} }
func typ(text string) protocol.Step   { return protocol.Step{Type: sp(text)} }
func pwd(text string) protocol.Step {
	return protocol.Step{Type: sp(text), Into: sel("secure"), Intent: "type " + text}
}
func keyStep(k string) protocol.Step { return protocol.Step{Key: k} }
func clickStep2(target string) protocol.Step {
	return protocol.Step{Click: sel(target)}
}

// plan runs a plan the way the executor reports it: Begin, the typing of
// the steps that ran, End. typed collects what it typed.
type plan struct {
	steps  []protocol.Step
	params map[string]string
	flow   string
	save   string
	start  Window
	done   int // -1: all
	end    Window
	ok     bool
	unsave bool
	during func()
}

func run(t *testing.T, c *Chains, p plan, typed *[]string) Ended {
	t.Helper()
	if p.done < 0 || p.done > len(p.steps) {
		p.done = len(p.steps)
	}
	if p.start == (Window{}) {
		p.start = Window{Title: notepad, PID: 100}
	}
	if p.end == (Window{}) {
		p.end = Window{Title: notepad, PID: 100}
	}
	r, err := c.Begin(Plan{Steps: p.steps, Params: p.params, Flow: p.flow, Save: p.save, Front: p.start})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.steps[:p.done] {
		f, err := Fill(s, p.params)
		if err != nil {
			t.Fatal(err)
		}
		if f.Type != nil && typed != nil {
			*typed = append(*typed, *f.Type)
		}
		if p.during != nil {
			p.during()
		}
	}
	e, err := r.End(Outcome{Done: p.done, OK: p.ok, Settled: p.ok, Unsaved: p.unsave, Front: p.end})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func pass(steps ...protocol.Step) plan { return plan{steps: steps, done: -1, ok: true} }

func flowFile(t *testing.T, s *Store, name string) *Saved {
	t.Helper()
	f, err := s.read(name + ".json")
	if err != nil {
		t.Fatalf("flow %s: %v", name, err)
	}
	return f
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNamedFlowNotOverwritten(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	p := pass(launch("Notepad"), keyStep("ctrl+b"), typ("quarterly report"))
	p.save = "notepad"
	if e := run(t, c, p, nil); e.SavedAs != "notepad" {
		t.Fatalf("saved as %q", e.SavedAs)
	}
	before := fileText(t, s.path("notepad.json"))
	ck.add(time.Second)
	if e := run(t, c, pass(launch("Notepad"), typ("scratch")), nil); e.SavedAs != "notepad-2" {
		t.Fatalf("auto save went to %q", e.SavedAs)
	}
	if fileText(t, s.path("notepad.json")) != before {
		t.Error("an automatic save overwrote the flow saved by name")
	}
	ck.add(time.Second)
	if e := run(t, c, pass(launch("Notepad"), typ("draft")), nil); e.SavedAs != "notepad-2" {
		t.Fatalf("a later auto save went to %q", e.SavedAs)
	}
	if got := flowFile(t, s, "notepad-2").Params["text"].Example; got != "draft" {
		t.Errorf("auto saves did not replace each other: text = %q", got)
	}
}

func TestSuffixedAutoNameFits(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	app := "Supercalifragilisticexpialidocious Notebook Editor"
	p := pass(launch(app), typ("quarterly report"))
	p.save = app
	named := run(t, c, p, nil).SavedAs
	if len(named) != MaxSlug {
		t.Fatalf("named %q", named)
	}
	before := fileText(t, s.path(named+".json"))
	ck.add(time.Second)
	later := run(t, c, pass(launch(app), typ("scratch")), nil).SavedAs
	if later == "" || len(later) > MaxSlug || later == named {
		t.Fatalf("auto name %q (named %q)", later, named)
	}
	if fileText(t, s.path(named+".json")) != before {
		t.Error("the suffixed auto name overwrote the named flow")
	}
	if flowFile(t, s, later).Params["text"].Example != "scratch" {
		t.Error("the auto save lost its value")
	}
}

func TestRecordedPlanSaves(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	click := protocol.Step{Click: sel(`"the Green button"`), Recorded: &protocol.Recorded{Text: "the Green button", Crop: crop, Scale: 2, At: &[2]float64{10, 20}, Size: &[2]float64{300, 200}}}
	e := run(t, c, pass(launch("Safari"), click), nil)
	if e.SavedAs != "safari-the-green-button" || e.Chain == "" {
		t.Fatalf("ended %+v", e)
	}
	f := flowFile(t, s, e.SavedAs)
	if r := f.Steps[1].Recorded; r == nil || r.Crop != crop || r.Scale != 2 {
		t.Errorf("the saved flow lost its recorded click: %+v", r)
	}
	if !f.Idempotent || strings.Contains(IndexLine(f), "once") {
		t.Errorf("a click-only flow is marked once: %s", IndexLine(f))
	}
}

func TestChainKeepsUntitledWindowPid(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	p := pass(launch("Notepad"), keyStep("ctrl+s"))
	p.end = Window{Title: "", PID: 4242}
	run(t, c, p, nil)
	if ch := c.Pending(); ch == nil || ch.PID != 4242 || ch.Window != "" {
		t.Fatalf("chain = %+v", ch)
	}
	next := pass(typ("x"))
	next.start = Window{Title: "", PID: 4242}
	run(t, c, next, nil)
	if ch := c.Pending(); len(ch.Steps) != 3 {
		t.Errorf("a plan in the untitled window did not join: %d steps", len(ch.Steps))
	}
}

func TestChainDirtyWhileActing(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	run(t, c, pass(launch("Notepad"), typ("hello")), nil)
	var seen []bool
	next := pass(typ(" world"))
	next.during = func() { seen = append(seen, c.Pending().Dirty) }
	run(t, c, next, nil)
	if !reflect.DeepEqual(seen, []bool{true}) {
		t.Errorf("dirty while acting = %v", seen)
	}
	ch := c.Pending()
	if ch.Dirty || len(ch.Steps) != 3 {
		t.Errorf("after the plan: dirty %v, %d steps", ch.Dirty, len(ch.Steps))
	}
}

func TestSaveNamesLastChainWithoutGit(t *testing.T) {
	t.Setenv("PATH", "")
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	if e := run(t, c, pass(launch("Notepad"), typ("hello")), nil); e.SavedAs != "notepad" {
		t.Fatalf("saved as %q", e.SavedAs)
	}
	f, err := c.SaveLast(context.Background(), "mine", map[string]string{"extra": "x"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "mine" || !f.Named || f.Params["extra"].Example != "x" || f.Params["text"].Example != "hello" {
		t.Errorf("saved %+v", f)
	}
	if _, err := os.Stat(s.path("notepad.json")); !os.IsNotExist(err) {
		t.Error("the automatic save was not renamed")
	}
	if _, err := os.Stat(s.path(".git")); !os.IsNotExist(err) {
		t.Error("a git repository appeared")
	}
}

func TestPasswordNotInFlowOrChain(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	p := pass(launch("Notepad"), pwd("{{password}}"))
	p.params, p.save = map[string]string{"password": "hunter2"}, "login"
	var typed []string
	run(t, c, p, &typed)
	if !reflect.DeepEqual(typed, []string{"hunter2"}) {
		t.Fatalf("typed %v", typed)
	}
	for _, f := range []string{"login.json", PendingFile, IndexFile} {
		if strings.Contains(fileText(t, s.path(f)), "hunter2") {
			t.Errorf("%s holds the password", f)
		}
	}
	if p := flowFile(t, s, "login").Params["password"]; !p.Secret || p.Example != "" {
		t.Errorf("password param = %+v", p)
	}
	lit := pass(launch("Notepad"), pwd("hunter3"))
	lit.save = "login2"
	run(t, c, lit, nil)
	for _, f := range []string{"login2.json", PendingFile, IndexFile} {
		if strings.Contains(fileText(t, s.path(f)), "hunter3") {
			t.Errorf("%s holds the literal password", f)
		}
	}
}

func TestStoppedPasswordNotInChain(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	stopped := plan{steps: []protocol.Step{launch("Notepad"), {Type: sp("{{password}}"), Into: sel(`secure textbox "PIN"`)}}, params: map[string]string{"password": "hunter4"}, done: 1}
	run(t, c, stopped, nil)
	if strings.Contains(fileText(t, s.path(PendingFile)), "hunter4") {
		t.Error("the chain holds the password of the step that stopped")
	}
	run(t, c, pass(launch("Notepad"), pwd("hunter5\n")), nil)
	if txt := fileText(t, s.path(PendingFile)); strings.Contains(txt, "hunter5") {
		t.Errorf("the chain holds a literal password: %s", txt)
	}
	f, err := c.SaveLast(context.Background(), "login3", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	body := fileText(t, s.path("login3.json"))
	if strings.Contains(body, "hunter5") || *f.Steps[1].Type != "{{text}}\n" {
		t.Errorf("saved flow:\n%s", body)
	}
}

func TestUserAndPasswordDistinctParams(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	steps := []protocol.Step{launch("Notepad"), typ("alice"), pwd("pw123")}
	run(t, c, pass(steps...), nil)
	if _, err := c.SaveLast(context.Background(), "login4", nil, false); err != nil {
		t.Fatal(err)
	}
	direct := pass(steps...)
	direct.save = "login5"
	run(t, c, direct, nil)
	for _, name := range []string{"login4", "login5"} {
		f := flowFile(t, s, name)
		var types []string
		for _, st := range f.Steps {
			if st.Type != nil {
				types = append(types, *st.Type)
			}
		}
		if !reflect.DeepEqual(types, []string{"{{text}}", "{{text_2}}"}) {
			t.Errorf("%s types = %v", name, types)
		}
		want := map[string]protocol.FlowParam{"text": {Example: "alice"}, "text_2": {Secret: true}}
		if !reflect.DeepEqual(f.Params, want) {
			t.Errorf("%s params = %+v", name, f.Params)
		}
		if f.Steps[2].Intent != "" {
			t.Errorf("%s keeps the password step's intent %q", name, f.Steps[2].Intent)
		}
	}
}

func TestSaveJustRunNoRetype(t *testing.T) {
	for _, tc := range []struct {
		steps  []protocol.Step
		params map[string]string
	}{
		{[]protocol.Step{launch("Notepad"), pwd("{{password}}")}, map[string]string{"password": "hunter6"}},
		{[]protocol.Step{launch("Notepad"), pwd("hunter7")}, nil},
	} {
		s, ck := noGit(t)
		c := chainsOf(s, ck)
		p := pass(tc.steps...)
		p.params = tc.params
		run(t, c, p, nil)
		res, err := c.Save(context.Background(), "login6", tc.steps, tc.params, "", func(context.Context, []protocol.Step, map[string]string, string) (*protocol.ActResult, error) {
			t.Error("the steps that just passed ran again")
			return nil, errors.New("ran")
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Ran || res.Flow == nil || res.Flow.Name != "login6" {
			t.Errorf("save = %+v", res)
		}
	}
}

func TestSaveChangedStepsRuns(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	run(t, c, pass(launch("Notepad"), pwd("hunter7")), nil)
	var typed []string
	changed := []protocol.Step{launch("Notepad"), pwd("hunter7\n")}
	res, err := c.Save(context.Background(), "login7", changed, nil, "", func(_ context.Context, steps []protocol.Step, params map[string]string, save string) (*protocol.ActResult, error) {
		p := pass(steps...)
		p.params, p.save = params, save
		e := run(t, c, p, &typed)
		return &protocol.ActResult{OK: true, SavedAs: e.SavedAs, Chain: e.Chain}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || !reflect.DeepEqual(typed, []string{"hunter7\n"}) {
		t.Errorf("ran %v typed %q", res.Ran, typed)
	}
	if res.Flow == nil || res.Flow.Name != "login7" {
		t.Errorf("flow = %+v", res.Flow)
	}
}

func TestBaseTemplating(t *testing.T) {
	for _, path := range []string{`C:\x\a.txt`, "/Users/me/x/a.txt"} {
		if got := Expand("{{file:base}} - Notepad", map[string]string{"file": path}); got != "a.txt - Notepad" {
			t.Errorf("%s: %q", path, got)
		}
		s, ck := noGit(t)
		c := chainsOf(s, ck)
		wait := protocol.Step{Wait: &protocol.WaitRequest{Expect: protocol.Expect{Front: notepad}}}
		e := run(t, c, pass(launch("Notepad"), typ(path), wait), nil)
		f := flowFile(t, s, e.SavedAs)
		if got := f.Steps[2].Wait.Front; got != "{{text:base}} - Notepad" {
			t.Errorf("%s: saved wait %q", path, got)
		}
		filled, _ := Fill(f.Steps[2], map[string]string{"text": "/tmp/b.md"})
		if filled.Wait.Front != "b.md - Notepad" {
			t.Errorf("replay waits for %q", filled.Wait.Front)
		}
	}
}

func TestSaveLastPlanChain(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	stopped := plan{steps: []protocol.Step{launch("Notepad"), keyStep("ctrl+b"), clickStep2("Italic")}, done: 2, end: Window{Title: "Untitled - Notepad", PID: 7}}
	run(t, c, stopped, nil)
	// A new daemon session: a fresh Chains over the same store.
	fresh := &Chains{Store: s, now: ck.now, Front: func(context.Context) (Window, error) { return Window{Title: "Untitled - Notepad", PID: 7}, nil }}
	f, err := fresh.SaveLast(context.Background(), "bold", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Steps) != 2 || f.Steps[1].Key != "ctrl+b" {
		t.Errorf("saved steps = %v", ops(f.Steps))
	}
	// Elsewhere in front, a stopped chain is not saved.
	run(t, c, stopped, nil)
	fresh.Front = func(context.Context) (Window, error) { return Window{Title: "Other"}, nil }
	if _, err := fresh.SaveLast(context.Background(), "bold2", nil, false); err == nil || !strings.Contains(err.Error(), `"Other" is in front`) {
		t.Errorf("save with another window in front = %v", err)
	}
}

func seedNote(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Save(&Saved{Name: "note", Named: true, Params: map[string]protocol.FlowParam{"n": {Example: "0"}}, Steps: []protocol.Step{launch("Mousepad"), typ("replay run {{n}}")}}, "seed"); err != nil {
		t.Fatal(err)
	}
}

func repair(t *testing.T, s *Store, c *Chains) Ended {
	t.Helper()
	at := Window{Title: "run-0.txt - Mousepad", PID: 9}
	stopped := plan{steps: []protocol.Step{launch("Mousepad"), typ("replay run {{n}}")}, params: map[string]string{"n": "1"}, flow: "note", done: 1, end: at}
	if e := run(t, c, stopped, nil); e.SavedAs != "" {
		t.Fatalf("a stopped replay saved %q", e.SavedAs)
	}
	fix := pass(keyStep("ctrl+n"), typ("replay run {{n}}"))
	fix.params, fix.start = map[string]string{"n": "1"}, at
	return run(t, c, fix, nil)
}

func TestRepairSavesFixedSteps(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	seedNote(t, s)
	if e := repair(t, s, c); e.SavedAs != "note" {
		t.Fatalf("the fix saved as %q", e.SavedAs)
	}
	f := flowFile(t, s, "note")
	var got []string
	for _, st := range f.Steps {
		got = append(got, st.Launch+st.Key+deref(st.Type))
	}
	if !reflect.DeepEqual(got, []string{"Mousepad", "ctrl+n", "replay run {{n}}"}) {
		t.Errorf("repaired steps = %v", got)
	}
	if f.Params["n"].Example != "1" || !f.Named {
		t.Errorf("params %+v named %v", f.Params, f.Named)
	}
	// Past the repair window the next plan repairs nothing.
	s2, ck2 := noGit(t)
	c2 := chainsOf(s2, ck2)
	seedNote(t, s2)
	stopped := plan{steps: []protocol.Step{launch("Mousepad"), typ("replay run {{n}}")}, params: map[string]string{"n": "1"}, flow: "note", done: 1, end: Window{Title: "w", PID: 9}}
	run(t, c2, stopped, nil)
	ck2.add(ChainAge)
	late := pass(keyStep("ctrl+n"))
	late.start = Window{Title: "w", PID: 9}
	if e := run(t, c2, late, nil); e.SavedAs != "" {
		t.Errorf("a plan past 10 minutes repaired the flow as %q", e.SavedAs)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestContinuationSavedIntoFlow(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	seedNote(t, s)
	repair(t, s, c)
	// The next replay runs the fix and passes.
	f, err := s.Load("note")
	if err != nil {
		t.Fatal(err)
	}
	var typed []string
	replay := plan{steps: f.Steps, params: map[string]string{"n": "2"}, flow: "note", done: -1, ok: true}
	if e := run(t, c, replay, &typed); e.SavedAs != "note" {
		t.Errorf("the replay answered %+v", e)
	}
	if !reflect.DeepEqual(typed, []string{"replay run 2"}) {
		t.Errorf("typed %q", typed)
	}
	if c.Pending() != nil {
		t.Error("a passing replay left a chain to repair")
	}
}

func TestNoSaveAfterKill(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	run(t, c, pass(launch("Notepad"), typ("a")), nil)
	// A continuation is killed after it acted: Begin without End.
	if _, err := c.Begin(Plan{Steps: []protocol.Step{typ("b")}, Front: Window{Title: notepad, PID: 100}}); err != nil {
		t.Fatal(err)
	}
	next := pass(typ("c"))
	if e := run(t, c, next, nil); e.SavedAs != "" {
		t.Errorf("a chain over a killed run was saved as %q", e.SavedAs)
	}
	if _, err := c.SaveLast(context.Background(), "abc", nil, false); !errors.Is(err, ErrNothingToSave) {
		t.Errorf("save over a killed run = %v", err)
	}
	// A plan with a step vision resolved is never saved either.
	v := pass(launch("Notepad"), typ("x"))
	v.unsave = true
	if e := run(t, c, v, nil); e.SavedAs != "" {
		t.Errorf("an unsaved plan was saved as %q", e.SavedAs)
	}
}

func TestSecretNotSaved(t *testing.T) {
	s, ck := noGit(t)
	if gitAvailable() {
		s.git, s.devTools = nil, nil
	}
	c := chainsOf(s, ck)
	steps := []protocol.Step{launch("Terminal"), typ("sudo ls\n"), {Type: sp("s3cret\n"), Secret: true, Intent: "enter s3cret at the sudo prompt"}}
	e := run(t, c, pass(steps...), nil)
	if e.SavedAs == "" {
		t.Fatal("not saved")
	}
	f := flowFile(t, s, e.SavedAs)
	if *f.Steps[2].Type != "{{text_2}}\n" || f.Steps[2].Intent != "" || !f.Params["text_2"].Secret {
		t.Errorf("secret step saved as %+v, params %+v", f.Steps[2], f.Params)
	}
	for _, name := range []string{e.SavedAs + ".json", PendingFile, IndexFile} {
		if strings.Contains(fileText(t, s.path(name)), "s3cret") {
			t.Errorf("%s holds the secret", name)
		}
	}
	if gitAvailable() {
		if out := gitOut(t, s.Dir, "log", "-p", "--all"); strings.Contains(out, "s3cret") || !strings.Contains(out, "sudo ls") {
			t.Errorf("git history:\n%s", out)
		}
	}
	// A replay supplies the secret through params.
	filled, _ := Fill(f.Steps[2], map[string]string{"text_2": "pw"})
	if *filled.Type != "pw\n" {
		t.Errorf("filled %q", *filled.Type)
	}
}

func TestParamStepNotPinned(t *testing.T) {
	rec := &protocol.Recorded{Text: "Alice", Nth: 2, Of: 3}
	pinned := protocol.Step{Click: sel(`button "Alice"`), Recorded: rec}
	param := protocol.Step{Click: sel(`button "{{who}}"`), Recorded: rec}
	inWindow := protocol.Step{Click: &protocol.Target{Select: `button "OK"`, Window: "{{doc}}"}, Recorded: rec}
	params := map[string]string{"who": "Bob", "doc": "b.txt"}
	f, _ := Fill(pinned, params)
	if f.Recorded.Nth != 2 || f.Recorded.Of != 3 {
		t.Errorf("a fixed target lost its fingerprint: %+v", f.Recorded)
	}
	for _, s := range []protocol.Step{param, inWindow} {
		f, _ := Fill(s, params)
		if f.Recorded.Nth != 0 || f.Recorded.Of != 0 {
			t.Errorf("a parameterised step stays pinned: %+v", f.Recorded)
		}
		if f.Recorded.Text != "Alice" {
			t.Errorf("the recorded text was filled: %q", f.Recorded.Text)
		}
	}
	f, _ = Fill(param, params)
	if f.Click.Select != `button "Bob"` {
		t.Errorf("target = %s", f.Click.Select)
	}
}

func TestStoragePrivate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "flows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.json")
	_ = os.WriteFile(old, []byte(`{"name":"old","params":{},"steps":[],"saved_at":"2026-01-01T00:00:00Z"}`), 0o644)
	s, _ := NewStore(dir)
	s.devTools = func() bool { return false }
	// One flow and no model: the find fails, but it tightens the store first.
	_, _ = s.Find(context.Background(), "anything", nil)
	if m := modeOf(t, dir); m != 0o700 {
		t.Errorf("an older readable directory stays %o", m)
	}
	if m := modeOf(t, old); m != 0o600 {
		t.Errorf("an older readable flow stays %o", m)
	}
	ck := &clock{t: time.Now()}
	s.now = ck.now
	c := chainsOf(s, ck)
	run(t, c, pass(launch("Notepad"), typ("hi")), nil)
	if _, err := c.SaveLast(context.Background(), "hi", nil, false); err != nil {
		t.Fatal(err)
	}
	_ = s.Reindex()
	_ = s.LogRun(map[string]any{"ok": true})
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != "flows" {
		t.Errorf("wrote beside the store: %v", entries)
	}
	files, _ := os.ReadDir(dir)
	for _, e := range files {
		if m := modeOf(t, filepath.Join(dir, e.Name())); e.Type().IsRegular() && m != 0o600 {
			t.Errorf("%s is %o", e.Name(), m)
		}
	}
}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestRunLogRotation(t *testing.T) {
	s, _ := noGit(t)
	if err := s.LogRun(map[string]string{"first": "x"}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("y", RunLogMax)
	if err := s.LogRun(map[string]string{"big": big}); err != nil {
		t.Fatal(err)
	}
	if err := s.LogRun(map[string]string{"after": "z"}); err != nil {
		t.Fatal(err)
	}
	body := fileText(t, s.path(RunLogFile))
	if strings.Contains(body, "first") || strings.Contains(body, "yyyy") || !strings.Contains(body, "after") {
		t.Errorf("the run log did not start over past 1 MiB (%d bytes)", len(body))
	}
	if m := modeOf(t, s.path(RunLogFile)); m != 0o600 {
		t.Errorf("run log mode %o", m)
	}
}

func TestGitWithoutCLT(t *testing.T) {
	s, ck := noGit(t) // its git runner fails the test if git runs
	c := chainsOf(s, ck)
	if e := run(t, c, pass(launch("Notepad"), typ("x")), nil); e.SavedAs != "notepad" {
		t.Fatalf("saving without git = %+v", e)
	}
	if err := s.Delete("notepad"); err != nil {
		t.Fatal(err)
	}
	if err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
}

func TestNoStoreFails(t *testing.T) {
	if _, err := NewStore(""); !errors.Is(err, ErrNoStore) {
		t.Errorf("an empty store directory = %v", err)
	}
	var none *Chains
	if _, err := none.Begin(Plan{}); !errors.Is(err, ErrNoStore) {
		t.Errorf("begin without a store = %v", err)
	}
	if _, err := (&Chains{}).SaveLast(context.Background(), "x", nil, false); !errors.Is(err, ErrNoStore) {
		t.Errorf("save without a store = %v", err)
	}
	if _, err := (&Chains{}).Save(context.Background(), "x", nil, nil, "last", nil); !errors.Is(err, ErrNoStore) {
		t.Errorf("save from last without a store = %v", err)
	}
	var s *Store
	if err := s.ensure(); !errors.Is(err, ErrNoStore) {
		t.Errorf("a nil store = %v", err)
	}
}

func TestAutoSaveNaming(t *testing.T) {
	s, ck := noGit(t)
	c := chainsOf(s, ck)
	long := "Extraordinarily Long Application Name For Testing"
	steps := []protocol.Step{launch(long), {Menu: "File > Export as Portable Document Format"}, clickStep2(`button "Save Everything Right Now"`), keyStep("cmd+s")}
	e := run(t, c, pass(steps...), nil)
	if e.SavedAs == "" || len(e.SavedAs) > MaxSlug {
		t.Fatalf("auto name %q", e.SavedAs)
	}
	if !strings.HasPrefix(e.SavedAs, "extraordinarily-long-application") {
		t.Errorf("auto name %q does not start with the app", e.SavedAs)
	}
	p := pass(steps...)
	p.save = e.SavedAs
	ck.add(time.Second)
	run(t, c, p, nil)
	ck.add(time.Second)
	again := run(t, c, pass(steps...), nil).SavedAs
	if again == e.SavedAs || len(again) > MaxSlug {
		t.Errorf("a passing plan overwrote the flow saved by name: %q", again)
	}
	if got := Slug("  Hello, World! ++ Ünïcode…  "); got != "hello-world-n-code" {
		t.Errorf("slug = %q", got)
	}
}

func gitAvailable() bool { return commandLineTools() && gitBinary() }

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}
