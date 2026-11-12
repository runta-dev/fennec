// Package record records what the user does on the desktop: each click and
// key with what the accessibility tree and the window list showed then, the
// window that came to the front after each action, and the user's notes. A
// listen-only event tap (tap_darwin.go) feeds a Recorder, which is platform
// free; a recording becomes a draft plan in package flows.
package record

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/view"
)

const (
	// Settle is how long input must pause before the recorder relists
	// windows to see what came to the front.
	Settle = 700 * time.Millisecond
	// cropW and cropH are a click crop's size in points.
	cropW, cropH = 200.0, 100.0
	// ownGrace keeps input out for this long after fennec-cua's own action
	// ended, since the tap hears synthetic events a moment after they land.
	ownGrace = 300 * time.Millisecond
)

// Event kinds.
const (
	KindClick    = "click"
	KindKey      = "key"
	KindPassword = "password"
	KindAfter    = "after"
	KindNote     = "note"
	KindSkipped  = "skipped"
)

// Blocked names the apps whose input is never recorded, matched without
// regard to case on the app name or the bundle name.
var Blocked = []string{
	"1Password", "1Password 7", "Bitwarden", "Dashlane", "KeePassXC", "LastPass",
	"Keychain Access", "Passwords", "System Settings",
	"fennec-cua", "Fennec",
}

// rootRoles are roles that name a window or document rather than a control.
var rootRoles = map[string]bool{"window": true, "frame": true, "application": true, "dialog": true, "document": true}

// Errors a recording call answers.
var (
	ErrRunning  = errors.New("a recording is already running: stop it first")
	ErrNone     = errors.New("no recording is running, and none was made since the daemon started")
	ErrNotLive  = errors.New("no recording is running")
	ErrStopped  = errors.New("fennec-cua is stopped: resume it before recording")
	ErrNoTap    = errors.New("recording runs only on macOS")
	ErrNoDesk   = errors.New("the recorder has no desktop to read")
	errNoListen = errors.New("the recorder has no event tap")
)

// ---------------------------------------------------------------------------
// What the tap hears

// InputKind says what an Input is.
type InputKind int

// Input kinds.
const (
	InputClick InputKind = iota + 1
	InputKey
	// InputPassword: a password field of process PID took the keyboard
	// focus, so the keys that follow reach no listener.
	InputPassword
	InputNote
	// InputHotkey: the kill hotkey with key code Code ended the recording.
	InputHotkey
)

// Input is one thing the tap heard, in desktop coordinates, or a note.
type Input struct {
	Kind InputKind
	// At is when the tap heard it; input heard while fennec-cua acts is
	// left out.
	At time.Time

	// Click.
	X, Y  float64
	Right bool
	Count int
	Mods  []string
	// Hit is the element under the click as the tap read it before the app
	// handled the click; nil when it read none.
	Hit *Hit

	// Key and password.
	Code   int
	Text   string
	Secret bool // a key into a password field: Text stays empty
	PID    int  // the process the key went to; 0 when unknown
	Focus  *Field
	// Read marks the first key of a typing run, whose Focus the tap read
	// (Focus nil then means no focus was readable).
	Read bool

	Note string
}

// Field is the element with the keyboard focus.
type Field struct {
	Role   string `json:"role"`
	Name   string `json:"name"`
	Frame  string `json:"frame"` // the role of its window or sheet
	Window string `json:"window,omitempty"`
	Secure bool   `json:"secure,omitempty"`
	URL    string `json:"url,omitempty"`
	ID     string `json:"id,omitempty"` // DOM id
}

// Hit is the element under a click.
type Hit struct {
	Role string
	Name string
	Desc string
	PID  int
	// Path holds the ancestors up to the window, innermost first.
	Path []PathNode
	// Window is the element's window when it could be read directly, for
	// elements deeper than the path reaches.
	Window *PathNode
	Secret bool
	Web    *Web
}

// PathNode is one ancestor of a clicked element.
type PathNode struct {
	Role string `json:"role"`
	Name string `json:"name"`
	// Key is the window's backend identity, when the node is a window and
	// it is known.
	Key string `json:"key,omitempty"`
}

// Web names a web page and an element's DOM id in it.
type Web struct {
	URL string `json:"url"`
	ID  string `json:"id,omitempty"`
}

// ---------------------------------------------------------------------------
// What a recording holds

// Element is a clicked element as a snapshot names it.
type Element struct {
	Role string `json:"role"`
	Name string `json:"name"`
	Desc string `json:"description,omitempty"`
}

// EventWindow is a click's window, or the window an after event saw.
type EventWindow struct {
	Title  string  `json:"title"`
	Bounds *[4]int `json:"bounds,omitempty"`
}

// Event is one entry of a recording.
type Event struct {
	T    int64  `json:"t"` // ms since the recording started
	Kind string `json:"kind"`

	X       *float64     `json:"x,omitempty"`
	Y       *float64     `json:"y,omitempty"`
	Button  string       `json:"button,omitempty"`
	Count   int          `json:"count,omitempty"`
	Mods    []string     `json:"mods,omitempty"`
	App     string       `json:"app,omitempty"`
	Element *Element     `json:"element,omitempty"`
	Path    []PathNode   `json:"path,omitempty"`
	Root    bool         `json:"root,omitempty"`
	Late    bool         `json:"late,omitempty"` // the crop was taken after the click
	Window  *EventWindow `json:"window,omitempty"`
	// Crop is a base64 PNG of the click's window around the point, at
	// CropScale pixels per desktop unit; CropOffset is the point's offset
	// from the crop's centre in desktop units.
	Crop       string      `json:"crop,omitempty"`
	CropScale  float64     `json:"crop_scale,omitempty"`
	CropOffset *[2]float64 `json:"crop_offset,omitempty"`
	Secret     bool        `json:"secret,omitempty"`
	Web        *Web        `json:"web,omitempty"`

	Code *int   `json:"code,omitempty"`
	Text string `json:"text,omitempty"` // a key's text or a note's text
	// Focus is the field the first key of a typing run went to. FocusRead
	// says the key was the first of its run: then a nil Focus is written as
	// an explicit null, meaning no focus was readable.
	Focus     *Field `json:"-"`
	FocusRead bool   `json:"-"`

	Role string `json:"role,omitempty"` // an after window's role
}

type plainEvent Event

// MarshalJSON writes focus as null when the run's focus was unreadable.
func (e Event) MarshalJSON() ([]byte, error) {
	out := struct {
		plainEvent
		Focus json.RawMessage `json:"focus,omitempty"`
	}{plainEvent: plainEvent(e)}
	if e.FocusRead {
		b, err := json.Marshal(e.Focus)
		if err != nil {
			return nil, err
		}
		out.Focus = b
	}
	return json.Marshal(out)
}

// UnmarshalJSON tells a null focus from a missing one.
func (e *Event) UnmarshalJSON(data []byte) error {
	in := struct {
		*plainEvent
		Focus json.RawMessage `json:"focus"`
	}{plainEvent: (*plainEvent)(e)}
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	e.Focus, e.FocusRead = nil, len(in.Focus) > 0
	if e.FocusRead && string(in.Focus) != "null" {
		var f Field
		if err := json.Unmarshal(in.Focus, &f); err != nil {
			return err
		}
		e.Focus = &f
	}
	return nil
}

// Recording is one recording as saved.
type Recording struct {
	ID        string   `json:"id"`
	StartedMs int64    `json:"started_ms"`
	StoppedMs int64    `json:"stopped_ms"`
	Apps      []string `json:"apps"`
	Events    []Event  `json:"events"`
	// Path is the file it was saved to.
	Path string `json:"-"`
}

// ---------------------------------------------------------------------------
// What the recorder reads

// Desk is the desktop the recorder reads besides the tap.
type Desk interface {
	// Windows lists windows front to back.
	Windows(ctx context.Context) ([]view.RawWindow, error)
	// At hit-tests a desktop point, for a click the tap could not read.
	At(ctx context.Context, x, y int) (*Hit, error)
	// Shot captures one window alone as a PNG.
	Shot(ctx context.Context, w view.RawWindow) ([]byte, error)
	// HoldWeb keeps the web page tree of a Chromium app on until
	// ReleaseWeb, which turns off what was off before.
	HoldWeb(ctx context.Context, pid int) error
	ReleaseWeb(ctx context.Context)
	// Bundle is the file name of a process's app bundle without .app.
	Bundle(pid int) string
}

// Acting counts fennec-cua's own actions, so the recorder leaves out the
// input they send, including input heard just after one ended.
type Acting struct {
	mu   sync.Mutex
	n    int
	last time.Time
	now  func() time.Time
}

// Begin marks an action as running; the returned function ends it.
func (a *Acting) Begin() (end func()) {
	a.mu.Lock()
	a.n++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.n--
			a.last = a.clock()
			a.mu.Unlock()
		})
	}
}

// Own reports whether input heard at t may be fennec-cua's own.
func (a *Acting) Own(t time.Time) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n > 0 || (!a.last.IsZero() && t.Before(a.last.Add(ownGrace)))
}

func (a *Acting) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// ---------------------------------------------------------------------------
// Which input is kept

// IsBlocked reports whether an app, by its name or bundle name, is one whose
// input is never recorded.
func IsBlocked(app, bundle string) bool {
	for _, b := range Blocked {
		if strings.EqualFold(b, strings.TrimSpace(app)) || (bundle != "" && strings.EqualFold(b, strings.TrimSpace(bundle))) {
			return true
		}
	}
	return false
}

// keeps reports whether input to app (process pid) belongs in a recording
// of apps (empty: every app).
func keeps(d Desk, app string, pid int, apps []string) bool {
	if app == "" {
		return len(apps) == 0
	}
	bundle := ""
	if pid > 0 && d != nil {
		bundle = d.Bundle(pid)
	}
	if IsBlocked(app, bundle) {
		return false
	}
	if len(apps) == 0 {
		return true
	}
	for _, a := range apps {
		if strings.EqualFold(strings.TrimSpace(a), app) {
			return true
		}
	}
	return false
}

func rooted(h *Hit) bool { return h == nil || rootRoles[h.Role] }

// ---------------------------------------------------------------------------
// Saving

// Save writes a recording to path: the directory 0700, the file 0600.
func Save(path string, r *Recording) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load reads a saved recording.
func Load(path string) (*Recording, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Recording
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	r.Path = path
	return &r, nil
}
