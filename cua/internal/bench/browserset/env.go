package browserset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Paths in the golden image (cua-bench golden -chromium).
const (
	// GuestChromium is where the golden image installs the bundled
	// Chromium.
	GuestChromium = "/Applications/Chromium.app"
	// GuestLogDir holds the server's log, its output and the browser
	// snapshot.
	GuestLogDir = "/tmp/bench-pages"
)

// ProfileDir is Chromium's default user data folder under home: the
// profile a normally launched Chromium uses, and the one fennec-cua is
// pointed at for this set, so every backend works in the same profile.
func ProfileDir(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Chromium")
}

// Env is where a task runs: the VM by default, a temporary folder on the
// host in tests.
type Env struct {
	Home    string // the user's home folder
	Base    string // the server's URL
	LogDir  string // the server's log folder
	Profile string // Chromium's user data folder
	App     string // Chromium.app
	// MockKeychain runs a solver's Chromium with the mock keychain (host
	// tests: never the developer's Keychain).
	MockKeychain bool
	Desk         Desktop
}

// GuestEnv is the VM's environment.
func GuestEnv() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	return Env{Home: home, Base: DefaultBase, LogDir: GuestLogDir, Profile: ProfileDir(home), App: GuestChromium, Desk: OSA{}}, nil
}

// Expand resolves "~/" against the home folder.
func (e Env) Expand(p string) string {
	if p == "~" {
		return e.Home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(e.Home, rest)
	}
	return p
}

// Addr is the server's host:port.
func (e Env) Addr() string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(e.Base, "http://"), "https://"), "/")
}

// SnapshotPath is the browser snapshot file.
func (e Env) SnapshotPath() string { return filepath.Join(e.LogDir, SnapshotName) }

// Window is one Chromium window in a snapshot.
type Window struct {
	Mode   string    `json:"mode"`   // "normal" or "incognito"
	Active int       `json:"active"` // 1-based index of the active tab
	Tabs   []TabInfo `json:"tabs"`
}

// TabInfo is one tab.
type TabInfo struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Note is one Notes note.
type Note struct {
	Name, Text string
}

// Desktop is what setup, snapshot and grading need from the desktop
// session: Chromium through its AppleScript dictionary, Notes, TextEdit and
// launchd. OSA implements it in the VM; tests use a fake.
type Desktop interface {
	// ChromiumWindows lists Chromium's windows; running is false when it
	// is not running (it is never launched to answer).
	ChromiumWindows() (wins []Window, running bool, err error)
	// QuitChromium quits it and waits until it has exited.
	QuitChromium(app string) error
	// OpenChromium opens a URL in Chromium, launching it if needed.
	OpenChromium(app, url string) error
	OpenTextEdit(path string) error
	Notes() ([]Note, error)
	DeleteNotes(name string) error
	MakeNote(name, body string) error
	// SetLaunchEnv sets environment variables for apps launchd starts.
	SetLaunchEnv(vars map[string]string) error
}

// OSA is the VM's desktop, through osascript, open and launchctl.
type OSA struct{}

// Separators in AppleScript output: unit and record separators, which no
// URL, title or note contains.
const (
	unitSep   = "\x1f"
	recordSep = "\x1e"
)

func osascript(lines ...string) (string, error) {
	var args []string
	for _, l := range lines {
		args = append(args, "-e", l)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("osascript: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}

// asString quotes s as an AppleScript string literal.
func asString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// chromiumWindowsScript prints every window as W<us>mode<us>active<rs>
// followed by its tabs as T<us>url<us>title<rs>.
var chromiumWindowsScript = []string{
	`if application "Chromium" is not running then return "NOT RUNNING"`,
	`set us to character id 31`,
	`set rs to character id 30`,
	`set out to ""`,
	`tell application "Chromium"`,
	`repeat with w in windows`,
	`set out to out & "W" & us & (mode of w) & us & (active tab index of w) & rs`,
	`repeat with t in tabs of w`,
	`set out to out & "T" & us & (URL of t) & us & (title of t) & rs`,
	`end repeat`,
	`end repeat`,
	`end tell`,
	`return out`,
}

// ChromiumWindows implements Desktop.
func (OSA) ChromiumWindows() ([]Window, bool, error) {
	out, err := osascript(chromiumWindowsScript...)
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(out) == "NOT RUNNING" {
		return nil, false, nil
	}
	w, err := parseWindows(out)
	return w, true, err
}

// parseWindows reads chromiumWindowsScript's output.
func parseWindows(out string) ([]Window, error) {
	var wins []Window
	for _, rec := range strings.Split(out, recordSep) {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, unitSep)
		switch {
		case f[0] == "W" && len(f) == 3:
			n, err := strconv.Atoi(strings.TrimSpace(f[2]))
			if err != nil {
				return nil, fmt.Errorf("window record %q: %v", rec, err)
			}
			wins = append(wins, Window{Mode: f[1], Active: n})
		case f[0] == "T" && len(f) == 3 && len(wins) > 0:
			w := &wins[len(wins)-1]
			w.Tabs = append(w.Tabs, TabInfo{URL: f[1], Title: f[2]})
		default:
			return nil, fmt.Errorf("unexpected record %q", rec)
		}
	}
	return wins, nil
}

func chromiumRunning() bool {
	out, err := osascript(`return application "Chromium" is running`)
	return err == nil && strings.TrimSpace(out) == "true"
}

// QuitChromium implements Desktop: a normal quit (which writes the
// profile's Preferences), then SIGTERM and SIGKILL to what is left.
func (OSA) QuitChromium(app string) error {
	exe := filepath.Join(app, "Contents", "MacOS", "Chromium")
	alive := func() bool { return exec.Command("/usr/bin/pgrep", "-f", exe).Run() == nil }
	if chromiumRunning() {
		_, _ = osascript(`tell application "Chromium" to quit`)
	}
	for _, sig := range []string{"", "-TERM", "-KILL"} {
		if sig != "" {
			_ = exec.Command("/usr/bin/pkill", sig, "-f", exe).Run()
		}
		for i := 0; i < 40 && alive(); i++ {
			time.Sleep(250 * time.Millisecond)
		}
		if !alive() {
			return nil
		}
	}
	return errors.New("Chromium did not exit")
}

// OpenChromium implements Desktop.
func (OSA) OpenChromium(app, url string) error {
	if out, err := exec.Command("/usr/bin/open", "-a", app, url).CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %v: %s", url, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// OpenTextEdit implements Desktop.
func (OSA) OpenTextEdit(path string) error {
	if out, err := exec.Command("/usr/bin/open", "-a", "TextEdit", path).CombinedOutput(); err != nil {
		return fmt.Errorf("open %s in TextEdit: %v: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Notes implements Desktop.
func (OSA) Notes() ([]Note, error) {
	out, err := osascript(
		`set us to character id 31`, `set rs to character id 30`, `set out to ""`,
		`tell application "Notes"`,
		`repeat with n in notes`,
		`set out to out & (name of n) & us & (plaintext of n) & rs`,
		`end repeat`,
		`end tell`,
		`return out`)
	if err != nil {
		return nil, err
	}
	return parseNotes(out), nil
}

func parseNotes(out string) []Note {
	var notes []Note
	for _, rec := range strings.Split(out, recordSep) {
		rec = strings.TrimLeft(rec, "\n")
		name, text, ok := strings.Cut(rec, unitSep)
		if !ok {
			continue
		}
		notes = append(notes, Note{Name: name, Text: text})
	}
	return notes
}

// DeleteNotes implements Desktop.
func (OSA) DeleteNotes(name string) error {
	_, err := osascript(`tell application "Notes" to delete (every note whose name is ` + asString(name) + `)`)
	return err
}

// MakeNote implements Desktop.
func (OSA) MakeNote(name, body string) error {
	html := "<div>" + htmlEscape(name) + "</div><div>" + htmlEscape(body) + "</div>"
	_, err := osascript(`tell application "Notes" to make new note with properties {name:` + asString(name) + `, body:` + asString(html) + `}`)
	return err
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// SetLaunchEnv implements Desktop.
func (OSA) SetLaunchEnv(vars map[string]string) error {
	for k, v := range vars {
		if out, err := exec.Command("/bin/launchctl", "setenv", k, v).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl setenv %s: %v: %s", k, err, out)
		}
	}
	return nil
}

// NoDesktop is the host's stand-in: no GUI is touched; Notes is a list in
// memory and Chromium is never running.
type NoDesktop struct {
	NotesList []Note
}

// ChromiumWindows implements Desktop.
func (*NoDesktop) ChromiumWindows() ([]Window, bool, error) { return nil, false, nil }

// QuitChromium implements Desktop.
func (*NoDesktop) QuitChromium(string) error { return nil }

// OpenChromium implements Desktop.
func (*NoDesktop) OpenChromium(string, string) error { return nil }

// OpenTextEdit implements Desktop.
func (*NoDesktop) OpenTextEdit(string) error { return nil }

// Notes implements Desktop.
func (d *NoDesktop) Notes() ([]Note, error) { return d.NotesList, nil }

// DeleteNotes implements Desktop.
func (d *NoDesktop) DeleteNotes(name string) error {
	keep := d.NotesList[:0]
	for _, n := range d.NotesList {
		if n.Name != name {
			keep = append(keep, n)
		}
	}
	d.NotesList = keep
	return nil
}

// MakeNote implements Desktop.
func (d *NoDesktop) MakeNote(name, body string) error {
	d.NotesList = append(d.NotesList, Note{Name: name, Text: name + "\n" + body})
	return nil
}

// SetLaunchEnv implements Desktop.
func (*NoDesktop) SetLaunchEnv(map[string]string) error { return nil }
