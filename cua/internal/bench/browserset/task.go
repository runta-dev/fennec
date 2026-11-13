// Package browserset is the browser evaluation set (bench/sets/browser.md):
// our own tasks on local pages, the page server that runs inside the VM and
// records what it sees, the setup that readies a VM for a task, and the
// graders. Graders never use a backend's browser connection: they read the
// server's log, files on disk, Chromium's Preferences file, a snapshot of
// Chromium's windows taken through its AppleScript dictionary, and other
// apps' state through osascript.
package browserset

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	browsersite "fennec/cua/bench/browser"
)

// DefaultAddr is where the page server listens in the VM; DefaultBase is
// the URL the task texts use.
const (
	DefaultAddr = "127.0.0.1:8765"
	DefaultBase = "http://" + DefaultAddr
)

// Families of tasks.
var Families = []string{"forms", "extraction", "downloads", "tabs", "widgets", "settings", "cross-app", "distractions"}

// Def is one task as tasks.json defines it.
type Def struct {
	ID     string `json:"id"`
	Family string `json:"family"`
	// Apps the task involves, for reports ("Chromium", "Notes").
	Apps []string `json:"apps"`
	// Start is the page the setup opens in Chromium.
	Start string `json:"start"`
	// Instruction is the task text; {base} stands for the server's URL.
	Instruction string   `json:"instruction"`
	Setup       []Action `json:"setup,omitempty"`
	Grade       []Check  `json:"grade"`
}

// Text is the instruction with the server's base URL filled in.
func (d Def) Text(base string) string { return strings.ReplaceAll(d.Instruction, "{base}", base) }

// Action is one setup step beyond the common ones (server, profile,
// Chromium on the start page). Exactly one field is set.
type Action struct {
	// Mkdir creates a folder ("~/" is the home folder).
	Mkdir string `json:"mkdir,omitempty"`
	// Remove deletes a file or folder if it exists.
	Remove string `json:"remove,omitempty"`
	// Write creates a file with the given text.
	Write *WriteFile `json:"write,omitempty"`
	// OpenTextEdit opens a file in TextEdit (in the VM only).
	OpenTextEdit string `json:"open_textedit,omitempty"`
	// DeleteNotes deletes every Notes note with this name (in the VM only).
	DeleteNotes string `json:"delete_notes,omitempty"`
}

// WriteFile is a file a setup writes.
type WriteFile struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// Check is one grading condition; a task passes when all hold. Exactly
// one field is set.
type Check struct {
	// Submit: the last form submission to Path has these fields; with
	// Count > 0, there were exactly that many submissions to Path.
	Submit *SubmitCheck `json:"submit,omitempty"`
	// NoSubmit: nothing was submitted to this path.
	NoSubmit string `json:"no_submit,omitempty"`
	// Page and NoPage: the server did or did not serve this page.
	Page   string `json:"page,omitempty"`
	NoPage string `json:"no_page,omitempty"`
	// Download: the server served this file.
	Download string `json:"download,omitempty"`
	// File: a file exists with the content the server serves at Content.
	File *FileCheck `json:"file,omitempty"`
	// NoFile: no file matches this glob.
	NoFile string `json:"no_file,omitempty"`
	// Tabs: the tabs open on the server's origin, across all windows.
	Tabs *TabsCheck `json:"tabs,omitempty"`
	// Pref and PrefNot: a value in Chromium's Preferences file.
	Pref    *PrefCheck `json:"pref,omitempty"`
	PrefNot *PrefCheck `json:"pref_not,omitempty"`
	// Note: a Notes note with this name whose text contains every string.
	Note *NoteCheck `json:"note,omitempty"`
}

// SubmitCheck matches a form submission.
type SubmitCheck struct {
	Path   string             `json:"path"`
	Fields map[string]Matcher `json:"fields"`
	Count  int                `json:"count,omitempty"`
}

// FileCheck matches a file on disk against a file the server serves.
type FileCheck struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// TabsCheck matches the server-origin tabs of every Chromium window.
type TabsCheck struct {
	// Exact lists the paths that must be open, each exactly once and
	// nothing else from the server.
	Exact []string `json:"exact"`
}

// PrefCheck compares a value in the Preferences file. Key is the path of
// object keys (keys may contain dots; "{base}" stands for the server URL). A string Equals starting with "~/"
// is relative to the home folder.
type PrefCheck struct {
	Key    []string        `json:"key"`
	Equals json.RawMessage `json:"equals"`
}

// NoteCheck finds a note. Each Contains entry may list alternatives
// separated by " | " (a date written in several ways).
type NoteCheck struct {
	Name     string   `json:"name"`
	Contains []string `json:"contains"`
}

// Matcher compares a submitted form field. In JSON it is a string (equal
// after trimming, folding case and collapsing spaces), or an object:
// {"exact": "..."}, {"number": 12.5} (numerically equal after removing
// "$", "," and spaces), {"set": ["a", "b"]} (the field's values are exactly
// these, in any order), or {"absent": true} (not submitted, or empty).
type Matcher struct {
	Fold   *string  `json:"-"`
	Exact  *string  `json:"exact,omitempty"`
	Number *float64 `json:"number,omitempty"`
	Set    []string `json:"set,omitempty"`
	Absent bool     `json:"absent,omitempty"`
}

// UnmarshalJSON reads either form.
func (m *Matcher) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		m.Fold = &s
		return nil
	}
	type plain Matcher
	var p plain
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("field matcher %s: %w", b, err)
	}
	*m = Matcher(p)
	n := 0
	if m.Exact != nil {
		n++
	}
	if m.Number != nil {
		n++
	}
	if m.Set != nil {
		n++
	}
	if m.Absent {
		n++
	}
	if n != 1 {
		return fmt.Errorf("field matcher %s: want exactly one of exact, number, set, absent", b)
	}
	return nil
}

var spaces = regexp.MustCompile(`\s+`)

// fold trims, collapses whitespace and folds case.
func fold(s string) string {
	return strings.ToLower(spaces.ReplaceAllString(strings.TrimSpace(s), " "))
}

// parseNumber reads "$1,408.47" as 1408.47.
func parseNumber(s string) (float64, bool) {
	s = strings.NewReplacer("$", "", ",", "", " ", "").Replace(strings.TrimSpace(s))
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// Match reports whether a field's submitted values satisfy the matcher,
// and describes a mismatch.
func (m Matcher) Match(vals []string) (bool, string) {
	first := ""
	if len(vals) > 0 {
		first = vals[0]
	}
	switch {
	case m.Absent:
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return false, fmt.Sprintf("want empty, got %q", vals)
			}
		}
		return true, ""
	case m.Set != nil:
		got := append([]string(nil), vals...)
		want := append([]string(nil), m.Set...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			return false, fmt.Sprintf("want %q, got %q", want, got)
		}
		return true, ""
	case len(vals) != 1:
		return false, fmt.Sprintf("want one value, got %q", vals)
	case m.Exact != nil:
		if first != *m.Exact {
			return false, fmt.Sprintf("want %q, got %q", *m.Exact, first)
		}
	case m.Number != nil:
		f, ok := parseNumber(first)
		if !ok || f < *m.Number-0.0001 || f > *m.Number+0.0001 {
			return false, fmt.Sprintf("want %v, got %q", *m.Number, first)
		}
	case m.Fold != nil:
		if fold(first) != fold(*m.Fold) {
			return false, fmt.Sprintf("want %q, got %q", *m.Fold, first)
		}
	}
	return true, ""
}

// Load reads tasks.json and validates every definition.
func Load() ([]Def, error) { return LoadFS(browsersite.FS) }

// LoadFS reads tasks.json from fsys.
func LoadFS(fsys fs.FS) ([]Def, error) {
	data, err := fs.ReadFile(fsys, "tasks.json")
	if err != nil {
		return nil, err
	}
	var f struct {
		Tasks []Def `json:"tasks"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("tasks.json: %w", err)
	}
	seen := map[string]bool{}
	for _, d := range f.Tasks {
		if err := d.validate(); err != nil {
			return nil, err
		}
		if seen[d.ID] {
			return nil, fmt.Errorf("tasks.json: %s defined twice", d.ID)
		}
		seen[d.ID] = true
	}
	return f.Tasks, nil
}

// Find returns one task by id.
func Find(id string) (Def, error) {
	defs, err := Load()
	if err != nil {
		return Def{}, err
	}
	for _, d := range defs {
		if d.ID == id {
			return d, nil
		}
	}
	return Def{}, fmt.Errorf("no browser task %q", id)
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (d Def) validate() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("tasks.json: %s: %s", d.ID, fmt.Sprintf(format, args...))
	}
	if !idPattern.MatchString(d.ID) {
		return fail("the id must be lower-case words joined by hyphens")
	}
	known := false
	for _, f := range Families {
		known = known || f == d.Family
	}
	if !known {
		return fail("unknown family %q", d.Family)
	}
	if !strings.HasPrefix(d.Start, "/") {
		return fail("start must be a path on the server")
	}
	if strings.TrimSpace(d.Instruction) == "" {
		return fail("no instruction")
	}
	if len(d.Grade) == 0 {
		return fail("no grading checks")
	}
	for i, a := range d.Setup {
		n := 0
		for _, set := range []bool{a.Mkdir != "", a.Remove != "", a.Write != nil, a.OpenTextEdit != "", a.DeleteNotes != ""} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fail("setup step %d must set exactly one action", i+1)
		}
	}
	for i, c := range d.Grade {
		n := 0
		for _, set := range []bool{c.Submit != nil, c.NoSubmit != "", c.Page != "", c.NoPage != "", c.Download != "", c.File != nil,
			c.NoFile != "", c.Tabs != nil, c.Pref != nil, c.PrefNot != nil, c.Note != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fail("check %d must set exactly one condition", i+1)
		}
		if c.Submit != nil && (c.Submit.Path == "" || len(c.Submit.Fields) == 0) {
			return fail("check %d: a submit check needs a path and fields", i+1)
		}
		for _, p := range []*PrefCheck{c.Pref, c.PrefNot} {
			if p != nil && (len(p.Key) == 0 || len(p.Equals) == 0) {
				return fail("check %d: a preference check needs a key and a value", i+1)
			}
		}
		if c.File != nil && (c.File.Path == "" || c.File.Content == "") {
			return fail("check %d: a file check needs a path and the served content's path", i+1)
		}
	}
	return nil
}
