package browserset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// SnapshotName is the browser snapshot in the log folder.
const SnapshotName = "browser.json"

// Snapshot is Chromium's windows and tabs when the agent stopped, taken
// through Chromium's AppleScript dictionary (or, for a scripted solve,
// through the solver's own connection) before the backend shuts down,
// since fennec-cua closes the agent tabs it opened when its session ends.
type Snapshot struct {
	Running bool      `json:"running"`
	Source  string    `json:"source"` // "applescript" or "solver"
	Taken   time.Time `json:"taken"`
	Windows []Window  `json:"windows,omitempty"`
}

// WriteSnapshot saves a snapshot.
func WriteSnapshot(env Env, s Snapshot) error {
	if s.Taken.IsZero() {
		s.Taken = time.Now().UTC()
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(env.LogDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(env.SnapshotPath(), data, 0o644)
}

// TakeSnapshot records Chromium's windows through the desktop, then quits
// Chromium so that its Preferences file is written before grading.
func TakeSnapshot(env Env) (Snapshot, error) {
	wins, running, err := env.Desk.ChromiumWindows()
	if err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Running: running, Source: "applescript", Windows: wins}
	if err := WriteSnapshot(env, s); err != nil {
		return s, err
	}
	if running {
		if err := env.Desk.QuitChromium(env.App); err != nil {
			return s, err
		}
	}
	return s, nil
}

// ReadSnapshot reads the snapshot; ok is false when none was taken.
func ReadSnapshot(env Env) (s Snapshot, ok bool, err error) {
	data, err := os.ReadFile(env.SnapshotPath())
	if errors.Is(err, os.ErrNotExist) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	return s, true, json.Unmarshal(data, &s)
}

// Result is a task's grade.
type Result struct {
	Pass     bool
	Failures []string
}

// String is what bench-pages grade prints: "true" for a pass (the
// macOSWorld convention cua-bench grades by), else "false" and why. The
// reasons never contain the word "true", which would read as a pass.
func (r Result) String() string {
	if r.Pass {
		return "true"
	}
	msg := "false: " + strings.Join(r.Failures, "; ")
	return replaceFold(msg, "true", "yes")
}

func replaceFold(s, old, repl string) string {
	var b strings.Builder
	lower := strings.ToLower(s)
	for {
		i := strings.Index(lower, old)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i] + repl)
		s, lower = s[i+len(old):], lower[i+len(old):]
	}
}

// Grade checks every condition of a task. An error means the grader could
// not look (an evaluation error), not that the task failed.
func Grade(env Env, d Def) (Result, error) {
	log, err := ReadLog(env.LogDir)
	if err != nil {
		return Result{}, err
	}
	var fails []string
	for _, c := range d.Grade {
		msg, err := check(env, log, c)
		if err != nil {
			return Result{}, err
		}
		if msg != "" {
			fails = append(fails, msg)
		}
	}
	return Result{Pass: len(fails) == 0, Failures: fails}, nil
}

// check returns "" when the condition holds, else what is wrong.
func check(env Env, log []Entry, c Check) (string, error) {
	switch {
	case c.Submit != nil:
		return checkSubmit(log, *c.Submit), nil
	case c.NoSubmit != "":
		if n := count(log, KindSubmit, c.NoSubmit); n > 0 {
			return fmt.Sprintf("%d submission(s) to %s", n, c.NoSubmit), nil
		}
	case c.Page != "":
		if count(log, KindPage, c.Page) == 0 {
			return "page " + c.Page + " was never opened", nil
		}
	case c.NoPage != "":
		if n := count(log, KindPage, c.NoPage); n > 0 {
			return fmt.Sprintf("page %s was opened %d time(s)", c.NoPage, n), nil
		}
	case c.Download != "":
		if count(log, KindDownload, c.Download) == 0 {
			return c.Download + " was never downloaded", nil
		}
	case c.File != nil:
		return checkFile(env, *c.File)
	case c.NoFile != "":
		m, err := filepath.Glob(env.Expand(c.NoFile))
		if err != nil {
			return "", err
		}
		if len(m) > 0 {
			return fmt.Sprintf("unwanted file(s): %s", strings.Join(m, ", ")), nil
		}
	case c.Tabs != nil:
		return checkTabs(env, *c.Tabs)
	case c.Pref != nil:
		return checkPref(env, *c.Pref, true)
	case c.PrefNot != nil:
		return checkPref(env, *c.PrefNot, false)
	case c.Note != nil:
		return checkNote(env, *c.Note)
	default:
		return "", errors.New("empty check")
	}
	return "", nil
}

func count(log []Entry, kind, path string) int {
	n := 0
	for _, e := range log {
		if e.Kind == kind && e.Path == path {
			n++
		}
	}
	return n
}

func checkSubmit(log []Entry, sc SubmitCheck) string {
	var last *Entry
	n := 0
	for i := range log {
		if log[i].Kind == KindSubmit && log[i].Path == sc.Path {
			last = &log[i]
			n++
		}
	}
	if last == nil {
		return "nothing was submitted to " + sc.Path
	}
	var bad []string
	if sc.Count > 0 && n != sc.Count {
		bad = append(bad, fmt.Sprintf("%d submissions to %s, want %d", n, sc.Path, sc.Count))
	}
	names := make([]string, 0, len(sc.Fields))
	for k := range sc.Fields {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if ok, why := sc.Fields[k].Match(last.Fields[k]); !ok {
			bad = append(bad, sc.Path+" "+k+": "+why)
		}
	}
	return strings.Join(bad, "; ")
}

// Served is the content the server serves at a download path.
func Served(p string) ([]byte, bool) {
	if name, ok := strings.CutPrefix(p, "/marrowby/files/"); ok {
		return Report(name)
	}
	return nil, false
}

func checkFile(env Env, fc FileCheck) (string, error) {
	want, ok := Served(fc.Content)
	if !ok {
		return "", fmt.Errorf("nothing is served at %s", fc.Content)
	}
	got, err := os.ReadFile(env.Expand(fc.Path))
	if errors.Is(err, os.ErrNotExist) {
		return fc.Path + " does not exist", nil
	}
	if err != nil {
		return "", err
	}
	if !bytes.Equal(got, want) {
		return fc.Path + " is not the file served at " + fc.Content, nil
	}
	return "", nil
}

func checkTabs(env Env, tc TabsCheck) (string, error) {
	s, ok, err := ReadSnapshot(env)
	if err != nil {
		return "", err
	}
	if !ok {
		return "no browser snapshot was taken", nil
	}
	if !s.Running {
		return "Chromium was not running when the agent stopped", nil
	}
	base, err := url.Parse(env.Base)
	if err != nil {
		return "", err
	}
	var got []string
	for _, w := range s.Windows {
		for _, t := range w.Tabs {
			u, err := url.Parse(t.URL)
			if err != nil || u.Host != base.Host {
				continue
			}
			p := u.Path
			if p == "" {
				p = "/"
			}
			got = append(got, p)
		}
	}
	want := append([]string(nil), tc.Exact...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		return fmt.Sprintf("open tabs %v, want %v", got, want), nil
	}
	return "", nil
}

// PreferencesPath is the default profile's Preferences file.
func PreferencesPath(profile string) string { return filepath.Join(profile, "Default", "Preferences") }

func checkPref(env Env, pc PrefCheck, want bool) (string, error) {
	data, err := os.ReadFile(PreferencesPath(env.Profile))
	if errors.Is(err, os.ErrNotExist) {
		if want {
			return "Chromium's Preferences file does not exist", nil
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var prefs any
	if err := json.Unmarshal(data, &prefs); err != nil {
		return "", fmt.Errorf("Preferences: %w", err)
	}
	var expected any
	if err := json.Unmarshal(pc.Equals, &expected); err != nil {
		return "", err
	}
	if s, ok := expected.(string); ok {
		expected = env.Expand(s)
	}
	cur, found := prefs, true
	keys := make([]string, len(pc.Key))
	for i, k := range pc.Key {
		keys[i] = strings.ReplaceAll(k, "{base}", strings.TrimSuffix(env.Base, "/"))
	}
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			found = false
			break
		}
		if cur, ok = m[k]; !ok {
			found = false
			break
		}
	}
	key := strings.Join(keys, " > ")
	equal := found && reflect.DeepEqual(cur, expected)
	switch {
	case want && !found:
		return "preference " + key + " is not set", nil
	case want && !equal:
		return fmt.Sprintf("preference %s is %v, want %v", key, cur, expected), nil
	case !want && equal:
		return fmt.Sprintf("preference %s is %v", key, cur), nil
	}
	return "", nil
}

func checkNote(env Env, nc NoteCheck) (string, error) {
	notes, err := env.Desk.Notes()
	if err != nil {
		return "", err
	}
	named := 0
	for _, n := range notes {
		if fold(n.Name) != fold(nc.Name) {
			continue
		}
		named++
		text := fold(n.Text)
		missing := ""
		for _, c := range nc.Contains {
			if !containsAny(text, c) {
				missing = c
				break
			}
		}
		if missing == "" {
			return "", nil
		}
	}
	if named == 0 {
		return fmt.Sprintf("no note named %q", nc.Name), nil
	}
	return fmt.Sprintf("the note %q lacks some of %q", nc.Name, nc.Contains), nil
}

// containsAny reports whether text (folded) contains one of the
// alternatives in want, which are separated by " | ".
func containsAny(text, want string) bool {
	for _, alt := range strings.Split(want, " | ") {
		if a := fold(alt); a != "" && strings.Contains(text, a) {
			return true
		}
	}
	return false
}
