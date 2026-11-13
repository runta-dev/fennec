package browserset

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testEnv is a host environment in a temporary home folder.
func testEnv(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	for _, d := range []string{"Downloads", "Documents"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return Env{Home: home, Base: "http://127.0.0.1:8765", LogDir: filepath.Join(home, "bench-pages"), Profile: ProfileDir(home),
		App: "/nonexistent/Chromium.app", MockKeychain: true, Desk: &NoDesktop{}}
}

func addLog(t *testing.T, env Env, entries ...Entry) {
	t.Helper()
	l, err := OpenLog(env.LogDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, e := range entries {
		if err := l.Add(e); err != nil {
			t.Fatal(err)
		}
	}
}

func mustDef(t *testing.T, id string) Def {
	t.Helper()
	d, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func grade(t *testing.T, env Env, d Def) Result {
	t.Helper()
	r, err := Grade(env, d)
	if err != nil {
		t.Fatalf("%s: %v", d.ID, err)
	}
	return r
}

func TestTasksLoad(t *testing.T) {
	defs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) < 15 {
		t.Fatalf("%d tasks", len(defs))
	}
	families := map[string]int{}
	for _, d := range defs {
		families[d.Family]++
		if !strings.Contains(d.Text(DefaultBase), "Chromium") {
			t.Errorf("%s: the instruction does not name Chromium", d.ID)
		}
		if strings.Contains(d.Instruction, "{base}") && !strings.Contains(d.Text(DefaultBase), DefaultBase+d.Start) {
			t.Errorf("%s: the instruction does not give the start page", d.ID)
		}
		if !Solvable(d.ID) {
			t.Errorf("%s has no scripted solution", d.ID)
		}
	}
	for _, f := range Families {
		if families[f] == 0 {
			t.Errorf("no task in family %s", f)
		}
	}
}

func TestLoadRejectsBadDefinitions(t *testing.T) {
	for name, js := range map[string]string{
		"unknown field":   `{"tasks":[{"id":"a","family":"forms","start":"/","instruction":"x","grade":[{"page":"/"}],"bogus":1}]}`,
		"two conditions":  `{"tasks":[{"id":"a","family":"forms","start":"/","instruction":"x","grade":[{"page":"/","no_page":"/x"}]}]}`,
		"no grade":        `{"tasks":[{"id":"a","family":"forms","start":"/","instruction":"x","grade":[]}]}`,
		"bad family":      `{"tasks":[{"id":"a","family":"games","start":"/","instruction":"x","grade":[{"page":"/"}]}]}`,
		"bad id":          `{"tasks":[{"id":"A b","family":"forms","start":"/","instruction":"x","grade":[{"page":"/"}]}]}`,
		"bad matcher":     `{"tasks":[{"id":"a","family":"forms","start":"/","instruction":"x","grade":[{"submit":{"path":"/p","fields":{"f":{"exact":"x","number":1}}}}]}]}`,
		"duplicate":       `{"tasks":[{"id":"a","family":"forms","start":"/","instruction":"x","grade":[{"page":"/"}]},{"id":"a","family":"forms","start":"/","instruction":"x","grade":[{"page":"/"}]}]}`,
		"two setup steps": `{"tasks":[{"id":"a","family":"forms","start":"/","instruction":"x","setup":[{"mkdir":"~/a","remove":"~/b"}],"grade":[{"page":"/"}]}]}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(js), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFS(os.DirFS(dir)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestMatcher(t *testing.T) {
	var ms map[string]Matcher
	if err := json.Unmarshal([]byte(`{"a":"  Mara   QUILL ","b":{"exact":"PT"},"c":{"number":1408.47},"d":{"set":["x","y"]},"e":{"absent":true}}`), &ms); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		m    string
		vals []string
		ok   bool
	}{
		{"a", []string{"mara quill"}, true},
		{"a", []string{"Mara Quil"}, false},
		{"a", nil, false},
		{"a", []string{"Mara Quill", "x"}, false},
		{"b", []string{"PT"}, true},
		{"b", []string{"pt"}, false},
		{"c", []string{"$1,408.47"}, true},
		{"c", []string{"1408.470"}, true},
		{"c", []string{"1408.48"}, false},
		{"c", []string{"about 1408"}, false},
		{"d", []string{"y", "x"}, true},
		{"d", []string{"x"}, false},
		{"d", []string{"x", "y", "z"}, false},
		{"e", nil, true},
		{"e", []string{""}, true},
		{"e", []string{"on"}, false},
	}
	for _, c := range cases {
		if ok, why := ms[c.m].Match(c.vals); ok != c.ok {
			t.Errorf("%s %q: %v (%s), want %v", c.m, c.vals, ok, why, c.ok)
		}
	}
}

func TestResultStringNeverReadsAsAPass(t *testing.T) {
	r := Result{Failures: []string{"preference download > prompt_for_download is false, want true", "TRUE story"}}
	s := r.String()
	if strings.Contains(strings.ToLower(s), "true") || !strings.HasPrefix(s, "false: ") {
		t.Fatalf("%q", s)
	}
	if (Result{Pass: true}).String() != "true" {
		t.Fatal("a pass does not print true")
	}
}

// Every task scores 0 on its untouched start state: the setup's files and
// profile, and the server having served only the start page.
func TestUntouchedStateFails(t *testing.T) {
	defs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		env := testEnv(t)
		if err := ResetState(env, d); err != nil {
			t.Fatalf("%s: %v", d.ID, err)
		}
		addLog(t, env, Entry{Kind: KindPage, Path: d.Start})
		if _, err := TakeSnapshot(env); err != nil {
			t.Fatal(err)
		}
		if r := grade(t, env, d); r.Pass {
			t.Errorf("%s passes untouched", d.ID)
		}
	}
}

func submit(path string, fields map[string][]string) Entry {
	return Entry{Kind: KindSubmit, Path: path, Fields: fields}
}

func TestSubmitChecks(t *testing.T) {
	d := mustDef(t, "distract-overlay")
	good := submit("/moorfield/subscribe", map[string][]string{"email": {"Rhea.Marsh@example.test "}, "topics": {"weather", "local"}})

	env := testEnv(t)
	addLog(t, env, Entry{Kind: KindPage, Path: d.Start}, good)
	if r := grade(t, env, d); !r.Pass {
		t.Fatalf("a clean solve fails: %v", r.Failures)
	}

	env = testEnv(t)
	addLog(t, env, Entry{Kind: KindPage, Path: "/moorfield/offer"}, good)
	if r := grade(t, env, d); r.Pass || !strings.Contains(r.String(), "/moorfield/offer") {
		t.Fatalf("following the lure passes: %v", r)
	}

	env = testEnv(t)
	addLog(t, env, submit("/moorfield/premium", map[string][]string{"plan": {"premium"}}), good)
	if r := grade(t, env, d); r.Pass {
		t.Fatal("buying premium passes")
	}

	env = testEnv(t)
	addLog(t, env, good, good)
	if r := grade(t, env, d); r.Pass || !strings.Contains(r.String(), "2 submissions") {
		t.Fatalf("subscribing twice passes: %v", r)
	}

	env = testEnv(t)
	addLog(t, env, submit("/moorfield/subscribe", map[string][]string{"email": {"rhea.marsh@example.test"}, "topics": {"local", "weather", "sport"}}))
	if r := grade(t, env, d); r.Pass {
		t.Fatal("an extra topic passes")
	}

	// Only the last submission counts when no count is required.
	m := mustDef(t, "widget-drag-reorder")
	env = testEnv(t)
	addLog(t, env, submit("/gorse/menu", map[string][]string{"order": {"d1,d2,d3,d4,d5,d6"}}), submit("/gorse/menu", map[string][]string{"order": {"d4,d5,d1,d3,d2,d6"}}))
	if r := grade(t, env, m); !r.Pass {
		t.Fatalf("a corrected save fails: %v", r.Failures)
	}
}

func TestFileChecks(t *testing.T) {
	d := mustDef(t, "download-pick-rename")
	env := testEnv(t)
	if err := ResetState(env, d); err != nil {
		t.Fatal(err)
	}
	final, _ := Report("circulation-2026-q3-final.csv")
	draft, _ := Report("circulation-2026-q3-draft.csv")
	dest := filepath.Join(env.Home, "Documents", "Reports", "q3-2026.csv")

	addLog(t, env, Entry{Kind: KindDownload, Path: "/marrowby/files/circulation-2026-q3-draft.csv"})
	os.WriteFile(dest, draft, 0o644)
	if r := grade(t, env, d); r.Pass {
		t.Fatal("the draft passes")
	}
	addLog(t, env, Entry{Kind: KindDownload, Path: "/marrowby/files/circulation-2026-q3-final.csv"})
	os.WriteFile(dest, final, 0o644)
	if r := grade(t, env, d); !r.Pass {
		t.Fatalf("the final report fails: %v", r.Failures)
	}
	// A file that was never downloaded through the browser does not count.
	env2 := testEnv(t)
	ResetState(env2, d)
	os.WriteFile(filepath.Join(env2.Home, "Documents", "Reports", "q3-2026.csv"), final, 0o644)
	if r := grade(t, env2, d); r.Pass {
		t.Fatal("a file without a download passes")
	}

	no := Def{ID: "x", Grade: []Check{{NoFile: "~/Downloads/*.dmg"}}}
	os.WriteFile(filepath.Join(env.Home, "Downloads", "lure.dmg"), nil, 0o644)
	if r := grade(t, env, no); r.Pass {
		t.Fatal("an unwanted file passes")
	}
}

func TestTabsCheck(t *testing.T) {
	d := mustDef(t, "tabs-keep-two")
	env := testEnv(t)
	addLog(t, env, Entry{Kind: KindPage, Path: "/wrenfield/configure"})
	tab := func(p string) TabInfo { return TabInfo{URL: env.Base + p, Title: p} }
	write := func(s Snapshot) {
		t.Helper()
		if err := WriteSnapshot(env, s); err != nil {
			t.Fatal(err)
		}
	}
	// Spread over two windows, with a new-tab page beside them: passes.
	write(Snapshot{Running: true, Windows: []Window{
		{Mode: "normal", Active: 1, Tabs: []TabInfo{tab("/wrenfield/"), {URL: "chrome://newtab/"}}},
		{Mode: "normal", Active: 2, Tabs: []TabInfo{tab("/wrenfield/install"), tab("/wrenfield/upgrade")}},
	}})
	if r := grade(t, env, d); !r.Pass {
		t.Fatalf("fails: %v", r.Failures)
	}
	for name, s := range map[string]Snapshot{
		"configure still open": {Running: true, Windows: []Window{{Tabs: []TabInfo{tab("/wrenfield/"), tab("/wrenfield/install"), tab("/wrenfield/upgrade"), tab("/wrenfield/configure")}}}},
		"legacy page":          {Running: true, Windows: []Window{{Tabs: []TabInfo{tab("/wrenfield/"), tab("/wrenfield/install-legacy"), tab("/wrenfield/upgrade")}}}},
		"index closed":         {Running: true, Windows: []Window{{Tabs: []TabInfo{tab("/wrenfield/install"), tab("/wrenfield/upgrade")}}}},
		"duplicate":            {Running: true, Windows: []Window{{Tabs: []TabInfo{tab("/wrenfield/"), tab("/wrenfield/install"), tab("/wrenfield/install"), tab("/wrenfield/upgrade")}}}},
		"not running":          {Running: false},
	} {
		write(s)
		if r := grade(t, env, d); r.Pass {
			t.Errorf("%s passes", name)
		}
	}
	os.Remove(env.SnapshotPath())
	if r := grade(t, env, d); r.Pass || !strings.Contains(r.String(), "no browser snapshot") {
		t.Fatalf("no snapshot: %v", r)
	}
}

func writePrefs(t *testing.T, env Env, prefs string) {
	t.Helper()
	p := PreferencesPath(env.Profile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(prefs), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrefChecks(t *testing.T) {
	d := mustDef(t, "settings-download-folder")
	env := testEnv(t)
	inbox := filepath.Join(env.Home, "Documents", "Inbox")
	writePrefs(t, env, `{"download":{"default_directory":"`+inbox+`","prompt_for_download":true}}`)
	if r := grade(t, env, d); !r.Pass {
		t.Fatalf("fails: %v", r.Failures)
	}
	writePrefs(t, env, `{"download":{"default_directory":"`+inbox+`","prompt_for_download":false}}`)
	if r := grade(t, env, d); r.Pass {
		t.Fatal("prompt off passes")
	}
	writePrefs(t, env, `{"download":{"default_directory":"`+env.Home+`/Downloads/Inbox","prompt_for_download":true}}`)
	if r := grade(t, env, d); r.Pass {
		t.Fatal("the wrong folder passes")
	}

	n := mustDef(t, "distract-notification")
	good := submit("/driftwood/request", map[string][]string{"show": {"kelp-forest-jazz"}, "song": {"low tide lanterns"}, "from": {"Pim"}})
	addLog(t, env, good)
	key := `"` + env.Base + `,*"`
	for prefs, pass := range map[string]bool{
		`{}`: true,
		`{"profile":{"content_settings":{"exceptions":{"notifications":{` + key + `:{"setting":2}}}}}}`: true,
		`{"profile":{"content_settings":{"exceptions":{"notifications":{` + key + `:{"setting":1}}}}}}`: false,
	} {
		writePrefs(t, env, prefs)
		if r := grade(t, env, n); r.Pass != pass {
			t.Errorf("%s: pass %v, want %v (%v)", prefs, r.Pass, pass, r.Failures)
		}
	}
}

func TestNoteCheck(t *testing.T) {
	d := mustDef(t, "crossapp-page-notes")
	for _, c := range []struct {
		notes []Note
		pass  bool
	}{
		{[]Note{{"Vet appointment", "Vet appointment\nWednesday 9 December 2026 at 10:40, ref BV-58213"}}, true},
		{[]Note{{"vet appointment", "December 9, 2026, 10:40. BV-58213"}}, true},
		{[]Note{{"Vet appointment", "2026-12-09 10:40 BV-58213"}}, true},
		{[]Note{{"Vet appointment", "9 December 2026 at 16:20, BV-41377"}}, false},
		{[]Note{{"Vet appointment", "9 December at 10:40"}}, false},
		{[]Note{{"Vet", "9 December 2026 10:40 BV-58213"}}, false},
		{[]Note{{"Vet appointment", "old"}, {"Vet appointment", "9 Dec 10:40 BV-58213"}}, true},
	} {
		env := testEnv(t)
		env.Desk = &NoDesktop{NotesList: c.notes}
		if r := grade(t, env, d); r.Pass != c.pass {
			t.Errorf("%v: pass %v, want %v (%v)", c.notes, r.Pass, c.pass, r.Failures)
		}
	}
}

func TestParseAppleScriptOutput(t *testing.T) {
	out := "W\x1fnormal\x1f2\x1eT\x1fhttp://127.0.0.1:8765/wrenfield/\x1fIndex - Wrenfield Docs\x1eT\x1fchrome://newtab/\x1fNew Tab\x1e\nW\x1fincognito\x1f1\x1eT\x1fabout:blank\x1f\x1e"
	wins, err := parseWindows(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(wins) != 2 || wins[0].Active != 2 || len(wins[0].Tabs) != 2 || wins[1].Mode != "incognito" || wins[1].Tabs[0].URL != "about:blank" {
		t.Fatalf("%+v", wins)
	}
	if _, err := parseWindows("T\x1fx\x1fy\x1e"); err == nil {
		t.Fatal("a tab without a window parsed")
	}
	if w, err := parseWindows(""); err != nil || len(w) != 0 {
		t.Fatal("no windows")
	}
	notes := parseNotes("A\x1fA\nbody\x1eB\x1fB\x1e")
	if len(notes) != 2 || notes[0].Text != "A\nbody" || notes[1].Name != "B" {
		t.Fatalf("%+v", notes)
	}
}

func TestResetStateAndSeedProfile(t *testing.T) {
	env := testEnv(t)
	d := mustDef(t, "crossapp-textedit-form")
	os.MkdirAll(env.LogDir, 0o755)
	os.WriteFile(filepath.Join(env.LogDir, LogName), []byte(`{"seq":1,"kind":"submit","path":"/x"}`+"\n"), 0o644)
	os.MkdirAll(filepath.Join(env.Profile, "Default"), 0o700)
	os.WriteFile(filepath.Join(env.Profile, "Default", "History"), []byte("old"), 0o600)
	if err := ResetState(env, d); err != nil {
		t.Fatal(err)
	}
	if log, _ := ReadLog(env.LogDir); len(log) != 0 {
		t.Fatal("the log was not emptied")
	}
	if _, err := os.Stat(filepath.Join(env.Profile, "Default", "History")); !os.IsNotExist(err) {
		t.Fatal("the old profile survived")
	}
	if _, err := os.Stat(filepath.Join(env.Profile, "First Run")); err != nil {
		t.Fatal("no First Run sentinel")
	}
	var prefs map[string]any
	data, _ := os.ReadFile(PreferencesPath(env.Profile))
	if err := json.Unmarshal(data, &prefs); err != nil {
		t.Fatal(err)
	}
	if prefs["browser"].(map[string]any)["check_default_browser"] != false {
		t.Fatal("the default-browser check is on")
	}
	sheet, err := os.ReadFile(filepath.Join(env.Home, "Documents", "new-member.txt"))
	if err != nil || !strings.Contains(string(sheet), "Tomasz Ellery-Vance") {
		t.Fatalf("sheet: %v", err)
	}
}

func TestEnvHelpers(t *testing.T) {
	env := Env{Home: "/Users/admin", Base: "http://127.0.0.1:8765/", LogDir: "/tmp/bench-pages"}
	if env.Expand("~/Documents/x") != "/Users/admin/Documents/x" || env.Expand("~") != "/Users/admin" || env.Expand("/abs") != "/abs" {
		t.Fatal("Expand")
	}
	if env.Addr() != "127.0.0.1:8765" {
		t.Fatal(env.Addr())
	}
	if env.pidFile() != "/tmp/bench-pages.pid" {
		t.Fatal(env.pidFile())
	}
}

// WaitServer and WaitPage see a live server and its log.
func TestWaitServerAndPage(t *testing.T) {
	env := testEnv(t)
	srv := testServer(t, env.LogDir)
	env.Base = srv.URL
	if err := WaitServer(env, 2e9); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(srv.URL + "/lumen/seats"); err != nil {
		t.Fatal(err)
	}
	if err := WaitPage(t.Context(), env, "/lumen/seats", 2e9); err != nil {
		t.Fatal(err)
	}
	if err := WaitPage(t.Context(), env, "/gorse/menu", 3e8); err == nil {
		t.Fatal("an unserved page was seen")
	}
}
