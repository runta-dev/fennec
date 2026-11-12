//go:build scenario

package scenario

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/desk"
	"fennec/cua/internal/macos"
	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/view"
)

// State is the fixture's state.
type State map[string]any

// Get follows a dotted path; keys may hold '@' and '-'.
func (s State) Get(path string) any {
	var cur any = map[string]any(s)
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

// Str is a string value, or its JSON-ish rendering.
func (s State) Str(path string) string {
	switch v := s.Get(path).(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// Num is a number value, 0 when missing.
func (s State) Num(path string) float64 {
	switch v := s.Get(path).(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

// Bool is a bool value.
func (s State) Bool(path string) bool { b, _ := s.Get(path).(bool); return b }

// List is a list value.
func (s State) List(path string) []any { l, _ := s.Get(path).([]any); return l }

// Window returns the fixture window with the id.
func (s State) Window(id string) map[string]any {
	for _, w := range s.List("windows") {
		if m, ok := w.(map[string]any); ok && m["id"] == id {
			return m
		}
	}
	return nil
}

// WindowTitled returns the first fixture window with the title.
func (s State) WindowTitled(title string) map[string]any {
	for _, w := range s.List("windows") {
		if m, ok := w.(map[string]any); ok && m["title"] == title {
			return m
		}
	}
	return nil
}

// Commands lists the menu commands run, as "name/via".
func (s State) Commands() []string {
	var out []string
	for _, c := range s.List("commands") {
		if m, ok := c.(map[string]any); ok {
			out = append(out, fmt.Sprintf("%v/%v", m["name"], m["via"]))
		}
	}
	return out
}

// Events lists events of a kind.
func (s State) Events(kind string) []map[string]any {
	var out []map[string]any
	for _, e := range s.List("events." + kind) {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// checks combines failures.
type checks []string

func (c *checks) eq(what string, got, want any) {
	if !reflect.DeepEqual(norm(got), norm(want)) {
		*c = append(*c, fmt.Sprintf("%s = %v, want %v", what, got, want))
	}
}

func (c *checks) that(ok bool, format string, args ...any) {
	if !ok {
		*c = append(*c, fmt.Sprintf(format, args...))
	}
}

func (c checks) err() error {
	if len(c) == 0 {
		return nil
	}
	return errors.New(strings.Join(c, "; "))
}

func norm(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case nil:
		return nil
	}
	return v
}

func sleep(ctx context.Context, ms int) error {
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func firstImage(r mcpclient.ToolResult) ([]byte, error) {
	for _, c := range r.Content {
		if c.Type == "image" {
			return base64.StdEncoding.DecodeString(c.Data)
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// The harness's own reads of the desktop. The test binary holds the
// Accessibility grant (cua-vm test grants it), so it reads what the screen
// shows independent of either backend.

var dk = desk.New()

// windows lists the desktop's windows.
func windows(ctx context.Context) ([]view.RawWindow, error) { return dk.Windows(ctx) }

// window finds a window of an app by title (exact), or the app's first
// window when title is empty.
func window(ctx context.Context, app, title string) (view.RawWindow, error) {
	ws, err := windows(ctx)
	if err != nil {
		return view.RawWindow{}, err
	}
	for _, w := range ws {
		if (app == "" || w.App == app) && (title == "" || w.Title == title) {
			return w, nil
		}
	}
	var seen []string
	for _, w := range ws {
		seen = append(seen, fmt.Sprintf("%s/%q", w.App, w.Title))
	}
	return view.RawWindow{}, fmt.Errorf("no window %q of %q among %v", title, app, seen)
}

// waitWindow polls for a window.
func waitWindow(ctx context.Context, app, title string, d time.Duration) (view.RawWindow, error) {
	deadline := time.Now().Add(d)
	for {
		w, err := window(ctx, app, title)
		if err == nil || time.Now().After(deadline) {
			return w, err
		}
		if err := sleep(ctx, 100); err != nil {
			return w, err
		}
	}
}

// tree reads a window's elements.
func tree(ctx context.Context, w view.RawWindow) ([]view.RawNode, error) {
	t, err := dk.Tree(ctx, w, view.TreeOptions{URLs: true, Web: true})
	return t.Nodes, err
}

// element finds an element by role (empty for any) and name in a window.
func element(ctx context.Context, w view.RawWindow, role, name string) (view.RawNode, error) {
	nodes, err := tree(ctx, w)
	if err != nil {
		return view.RawNode{}, err
	}
	for _, n := range nodes {
		if (role == "" || n.Role == role) && (n.Name == name || (n.Name == "" && n.Desc == name)) {
			return n, nil
		}
	}
	return view.RawNode{}, fmt.Errorf("no %s %q in %q", role, name, w.Title)
}

// boundsOf is the desktop bounds of an element of a fixture window.
func boundsOf(ctx context.Context, title, role, name string) ([4]int, error) {
	w, err := window(ctx, "CUAFixture", title)
	if err != nil {
		return [4]int{}, err
	}
	n, err := element(ctx, w, role, name)
	if err != nil {
		return [4]int{}, err
	}
	if n.Bounds == nil {
		return [4]int{}, fmt.Errorf("%s %q has no bounds", role, name)
	}
	return *n.Bounds, nil
}

// center is the middle of bounds.
func center(b [4]int) (int, int) { return b[0] + b[2]/2, b[1] + b[3]/2 }

// padCell is the desktop point at the centre of a click pad cell such as
// "C2" (5 columns A-E, 4 rows 1-4).
func padCell(ctx context.Context, title, pad, cell string) (int, int, error) {
	b, err := boundsOf(ctx, title, "", pad)
	if err != nil {
		return 0, 0, err
	}
	col := int(cell[0] - 'A')
	row := int(cell[1] - '1')
	x := b[0] + (2*col+1)*b[2]/10
	y := b[1] + (2*row+1)*b[3]/8
	return x, y, nil
}

// sh runs a shell command in the VM and returns its output.
func sh(ctx context.Context, command string) (string, error) {
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", command).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", command, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// openApp starts or fronts an app with open(1).
func openApp(ctx context.Context, args ...string) error {
	_, err := sh(ctx, "open "+strings.Join(quoteAll(args), " "))
	return err
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return out
}

// frontFinder makes Finder the active app, so the fixture is behind.
func frontFinder(ctx context.Context) error {
	if err := openApp(ctx, "-a", "Finder"); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, err := h.fx.RawState(ctx)
		if err == nil && !State(st).Bool("active") {
			return nil
		}
		if err := sleep(ctx, 100); err != nil {
			return err
		}
	}
	return errors.New("the fixture stayed active after Finder was opened")
}

// quitApp quits an app by name and waits for it to end.
func quitApp(ctx context.Context, name string) {
	_, _ = sh(ctx, "pkill -x "+quoteAll([]string{name})[0])
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("pgrep", "-x", name).Run() != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = exec.Command("pkill", "-9", "-x", name).Run()
}

// scratch returns a fresh file path under the scratch directory.
func scratch(name string) string {
	p := filepath.Join(scratchDir, name)
	_ = os.Remove(p)
	return p
}

// waitFile polls until a file exists and returns its content.
func waitFile(ctx context.Context, path string, d time.Duration) (string, bool) {
	deadline := time.Now().Add(d)
	for {
		if b, err := os.ReadFile(path); err == nil {
			return string(b), true
		}
		if time.Now().After(deadline) || sleep(ctx, 100) != nil {
			return "", false
		}
	}
}

// waitIdle waits until no input event came for d.
func waitIdle(ctx context.Context, d time.Duration) error {
	for {
		idle := policy.IdleTime()
		if idle >= d {
			return nil
		}
		if err := sleep(ctx, int((d-idle).Milliseconds())+20); err != nil {
			return err
		}
	}
}

// frontApp names the active app.
func frontApp() string {
	pid := ax.FrontmostPID()
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return strconv.Itoa(pid)
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

// cancelMenus closes every menu the fixture shows, so a menu a backend left
// open (it swallows later presses and hides the focused window) does not
// spoil the next scenario.
func cancelMenus(ctx context.Context) {
	act := desk.NewActions(dk, nil)
	ws, err := windows(ctx)
	if err != nil {
		return
	}
	for _, w := range ws {
		if w.App != "CUAFixture" {
			continue
		}
		t, err := dk.Tree(ctx, w, view.TreeOptions{Menus: true})
		if err != nil {
			continue
		}
		for _, n := range t.Nodes {
			if n.Role == "menu" {
				_ = act.Perform(ctx, n.Handle, "AXCancel")
			}
		}
		return
	}
}

// mainDisplay is the main display's size in desktop pixels.
func mainDisplay() (int, int, bool) { return macos.MainDisplayPixels() }

// waitScene waits until the accessibility API lists the windows a reset
// made: the fixture answers once its scene is built, a moment before AX
// shows it.
func waitScene(ctx context.Context) {
	st, err := h.fx.RawState(ctx)
	if err != nil {
		return
	}
	want := 0
	for _, w := range State(st).List("windows") {
		if m, ok := w.(map[string]any); ok && m["visible"] == true && m["on_active_space"] == true {
			want++
		}
	}
	if want == 0 {
		return
	}
	// A scene activates the app, but macOS may keep another app in front
	// (one a backend or a scenario left there): bring the fixture forward
	// as a user would, through its accessibility element.
	if !State(st).Bool("active") {
		if pid := int(State(st).Num("volatile.pid")); pid > 0 {
			app := ax.App(pid)
			_ = app.SetBool("AXFrontmost", true)
			app.Release()
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ws, err := windows(ctx)
		if err == nil {
			n := 0
			for _, w := range ws {
				if w.App == "CUAFixture" && w.Space == "" {
					n++
				}
			}
			if n >= want {
				return
			}
		}
		if sleep(ctx, 50) != nil {
			return
		}
	}
}

// resultText is the answer part of a kept tool call ("tool args -> answer").
func resultText(kept string) string {
	if i := strings.Index(kept, " -> "); i >= 0 {
		return kept[i+4:]
	}
	return kept
}

// allowGuestAgentCapture answers macOS's screen-capture consent alert for
// tart's guest agent, which every process the VM runs for the harness is
// attributed to. It is the VM's own test plumbing, not an app under test.
func allowGuestAgentCapture() int {
	out, _ := exec.Command("pgrep", "-x", "UserNotificationCenter").Output()
	n := 0
	for _, f := range strings.Fields(string(out)) {
		pid, _ := strconv.Atoi(f)
		app := ax.App(pid)
		ws, _ := app.Attr("AXWindows")
		list, _ := ws.([]any)
		for _, w := range list {
			el, ok := w.(*ax.Element)
			if !ok {
				continue
			}
			kids, _ := el.Attr("AXChildren")
			ks, _ := kids.([]any)
			about := false
			var allow *ax.Element
			for _, k := range ks {
				ke, ok := k.(*ax.Element)
				if !ok {
					continue
				}
				v, _ := ke.Attrs([]string{"AXRole", "AXTitle", "AXValue"})
				if len(v) == 3 {
					if s, _ := v[2].(string); strings.Contains(s, "tart-guest-agent") {
						about = true
					}
					if v[0] == "AXButton" && v[1] == "Allow" {
						allow = ke
					}
				}
			}
			if about && allow != nil && allow.Perform("AXPress") == nil {
				n++
			}
		}
		app.Release()
	}
	return n
}
