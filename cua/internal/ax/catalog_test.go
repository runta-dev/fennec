package ax

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestAXRolesToARIA(t *testing.T) {
	cases := []struct {
		k    Kind
		want string
		ok   bool
	}{
		{Kind{Role: "AXButton"}, "button", true},
		{Kind{Role: "AXRadioButton", Subrole: "AXTabButton"}, "tab", true},
		{Kind{Role: "AXRadioButton"}, "radio", true},
		{Kind{Role: "AXTextField", Subrole: "AXSecureTextField"}, "textbox", true},
		{Kind{Role: "AXTextField", Subrole: "AXSearchField"}, "searchbox", true},
		{Kind{Role: "AXSheet"}, "dialog", true},
		{Kind{Role: "AXWindow", Subrole: "AXDialog"}, "dialog", true},
		{Kind{Role: "AXWindow", Subrole: "AXStandardWindow"}, "window", true},
		{Kind{Role: "AXMenuItem", Submenu: true}, "menu", true},
		{Kind{Role: "AXMenuItem", Marked: true}, "menuitemcheckbox", true},
		{Kind{Role: "AXMenuItem"}, "menuitem", true},
		{Kind{Role: "AXCheckBox", Subrole: "AXSwitch"}, "switch", true},
		{Kind{Role: "AXCheckBox", Subrole: "AXToggle"}, "button", true},
		{Kind{Role: "AXCheckBox"}, "checkbox", true},
		{Kind{Role: "AXMenu"}, "generic", true},
		{Kind{Role: "AXGroup"}, "generic", true},
		{Kind{Role: "AXGroup", Named: true}, "group", true},
		{Kind{Role: "AXValueIndicator"}, "", false},
		{Kind{Role: "AXSomethingNew"}, "", false},
	}
	for _, c := range cases {
		got, ok := ARIARole(c.k)
		if got != c.want || ok != c.ok {
			t.Errorf("%+v: %q %v, want %q %v", c.k, got, ok, c.want, c.ok)
		}
	}
}

func TestCheckValuesToStates(t *testing.T) {
	val := func(v int) Facts { return Facts{Enabled: true, Value: v, HasValue: true} }
	cases := []struct {
		role string
		f    Facts
		want []string
	}{
		{"checkbox", val(1), []string{"checkable", "checked"}},
		{"checkbox", val(2), []string{"checkable", "mixed"}},
		{"checkbox", val(0), []string{"checkable"}},
		{"switch", val(1), []string{"checkable", "checked"}},
		{"radio", val(1), []string{"checkable", "checked"}},
		{"menuitemcheckbox", val(2), []string{"checkable", "mixed"}},
		{"button", val(1), []string{"pressed"}},
		{"button", Facts{Enabled: true}, nil},
		{"textbox", Facts{Enabled: true, Secure: true}, []string{"protected"}},
		{"textbox", Facts{}, []string{"disabled"}},
		{"textbox", val(1), nil},
	}
	for _, c := range cases {
		if got := ToggleStates(c.role, c.f); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %+v: %v, want %v", c.role, c.f, got, c.want)
		}
	}
}

func TestDriverWindowsHidden(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cua-driver")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "via-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	paths := map[int]string{10: link, 11: "/Applications/Other.app/Contents/MacOS/cua-driver"}
	f := DriverFilter{Path: bin, PathOf: func(pid int) (string, error) {
		if p, ok := paths[pid]; ok {
			return p, nil
		}
		return "", errors.New("no path")
	}}
	if !f.Is(ServerWindow{PID: 10, Owner: "anything"}) {
		t.Error("the driver at its canonical path was not recognised")
	}
	if f.Is(ServerWindow{PID: 11, Owner: "cua-driver"}) {
		t.Error("another binary named cua-driver was taken for the bundled one")
	}
	if f.Is(ServerWindow{PID: 12, Owner: "cua-driver"}) {
		t.Error("a process whose path cannot be resolved was treated as the driver")
	}
	// Hit tests pass the driver over too.
	ws := []ServerWindow{
		{ID: 1, PID: 10, Layer: 0, Alpha: 1, Bounds: Rect{0, 0, 500, 500}},
		{ID: 2, PID: 20, Layer: 0, Alpha: 1, Bounds: Rect{0, 0, 500, 500}},
	}
	skip := map[int]bool{}
	for _, w := range ws {
		if f.Is(w) {
			skip[w.PID] = true
		}
	}
	if w, ok := TopWindowAt(ws, 10, 10, skip); !ok || w.ID != 2 {
		t.Errorf("hit test found %+v %v", w, ok)
	}
	// The real lookup resolves this test binary.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProcessPath(os.Getpid())
	if err != nil || canonical(got) != canonical(self) {
		t.Errorf("ProcessPath = %q %v, want %q", got, err, self)
	}
	if !(DriverFilter{Path: self}).Is(ServerWindow{PID: os.Getpid()}) {
		t.Error("the default path lookup did not recognise the process")
	}
}

func TestFocusFallsBackToTreeRead(t *testing.T) {
	type n struct {
		name    string
		focused bool
		window  string
	}
	tree := []n{{name: "root"}, {name: "field", focused: true}, {name: "later", focused: true}}
	reads := 0
	src := func(direct *n) FocusSource[n] {
		return FocusSource[n]{
			Direct: func() (n, bool) {
				if direct == nil {
					return n{}, false
				}
				return *direct, true
			},
			InWindow: func(x n) bool { return x.window == "w1" },
			Tree:     func() ([]n, error) { reads++; return tree, nil },
			Focused:  func(x n) bool { return x.focused },
		}
	}
	got, ok, err := FocusedElement(src(&n{name: "direct", window: "w1"}))
	if err != nil || !ok || got.name != "direct" || reads != 0 {
		t.Errorf("direct match: %+v %v %v reads %d", got, ok, err, reads)
	}
	got, ok, _ = FocusedElement(src(nil))
	if !ok || got.name != "field" || reads != 1 {
		t.Errorf("nothing direct: %+v %v reads %d", got, ok, reads)
	}
	got, ok, _ = FocusedElement(src(&n{name: "elsewhere", window: "w2"}))
	if !ok || got.name != "field" || reads != 2 {
		t.Errorf("direct in another window: %+v %v reads %d", got, ok, reads)
	}
	tree = []n{{name: "root"}}
	if _, ok, err := FocusedElement(src(nil)); ok || err != nil {
		t.Errorf("no focus anywhere: %v %v", ok, err)
	}
}

func TestFrameShownWithinOnePoint(t *testing.T) {
	bounds := []Rect{{100, 100, 400, 300}, {0, 0, 50, 50}}
	cases := []struct {
		r    Rect
		want bool
	}{
		{Rect{100, 100, 400, 300}, true},
		{Rect{101, 99, 399, 301}, true},
		{Rect{0.5, 0.5, 50, 50}, true},
		{Rect{102, 100, 400, 300}, false},
		{Rect{100, 100, 402, 300}, false},
		{Rect{300, 300, 400, 300}, false},
	}
	for _, c := range cases {
		if got := FrameShown(bounds, c.r, true); got != c.want {
			t.Errorf("%+v: %v", c.r, got)
		}
	}
	if !FrameShown(nil, Rect{}, false) {
		t.Error("a window whose frame could not be read is not shown")
	}
}

func TestFrontAppWithoutAXMessage(t *testing.T) {
	// The front app comes from the process manager, not an accessibility
	// message, so it answers off the main thread and promptly even when an
	// app hangs.
	got := make(chan int, 1)
	go func() { got <- FrontmostPID() }()
	select {
	case pid := <-got:
		if pid < 0 {
			t.Errorf("pid %d", pid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the frontmost app read blocked")
	}
	// A hung front app's stale window is still chosen as active from it.
	ws := []StaleCandidate{{PID: 7, Stale: true}}
	if StaleActive(ws, 7) != 0 {
		t.Error("the stale window of the front app is not active")
	}
}

func hungClock() (*Hung[string], *time.Time) {
	now := time.Unix(1000, 0)
	return &Hung[string]{}, &now
}

func TestHungAfterLaunchGrace(t *testing.T) {
	h, now := hungClock()
	h.Skipping(1, *now) // first listed
	h.Answer(1, *now, []string{"Doc"})
	*now = now.Add(LaunchGrace)
	h.Miss(1, *now)
	*now = now.Add(HungAfter - time.Millisecond)
	h.Miss(1, *now)
	if h.IsHung(1) {
		t.Fatal("hung before 2 s of misses")
	}
	*now = now.Add(time.Millisecond)
	h.Miss(1, *now)
	if !h.IsHung(1) {
		t.Fatal("not hung after 2 s of misses past the launch grace")
	}
	if _, skip := h.Skipping(1, now.Add(HungSkip-time.Millisecond)); !skip {
		t.Error("not skipped within 10 s")
	}
	if _, skip := h.Skipping(1, now.Add(HungSkip)); skip {
		t.Error("still skipped after 10 s")
	}
	// The first miss after the skip marks it hung again at once.
	*now = now.Add(HungSkip)
	h.Miss(1, *now)
	if _, skip := h.Skipping(1, now.Add(time.Second)); !skip {
		t.Error("not hung again at the first miss after the skip")
	}
	// An answer clears it.
	h.Answer(1, *now, []string{"Doc"})
	if h.IsHung(1) {
		t.Error("hung after answering")
	}
}

func TestHungAppKeepsStaleWindows(t *testing.T) {
	h, now := hungClock()
	h.Skipping(1, *now)
	h.Answer(1, *now, []string{"Doc", "Prefs"})
	*now = now.Add(time.Minute)
	if got := h.Miss(1, *now); !reflect.DeepEqual(got, []string{"Doc", "Prefs"}) {
		t.Errorf("a miss dropped the windows: %v", got)
	}
	*now = now.Add(3 * time.Second)
	h.Miss(1, *now)
	last, skip := h.Skipping(1, now.Add(time.Second))
	if !skip || !reflect.DeepEqual(last, []string{"Doc", "Prefs"}) {
		t.Errorf("a skipped app lost its windows: %v %v", last, skip)
	}
}

func TestLaunchGraceKeepsWindow(t *testing.T) {
	h, now := hungClock()
	h.Skipping(1, *now)
	h.Answer(1, *now, []string{"Splash"})
	// It keeps timing out for 9 s right after launch.
	for i := 0; i < 90; i++ {
		*now = now.Add(100 * time.Millisecond)
		if got := h.Miss(1, *now); len(got) != 1 {
			t.Fatalf("the launching app's window was dropped at %d", i)
		}
		if h.IsHung(1) {
			t.Fatalf("written off within the launch grace at %d", i)
		}
	}
	*now = now.Add(time.Second)
	h.Miss(1, *now)
	if !h.IsHung(1) {
		t.Error("not hung after the grace with 2 s of misses")
	}
	if got := h.Last(1); len(got) != 1 {
		t.Error("windows dropped once hung")
	}
}

func TestListedTopLevelByNumberOrTitle(t *testing.T) {
	listed := []Listed{{ID: 5, Title: "Main"}, {ID: 0, Title: "Inspector"}}
	cases := []struct {
		id    uint32
		title string
		want  bool
	}{
		{5, "Renamed", true},      // by number
		{9, "Inspector", true},    // by title
		{9, "Popup", false},       // not listed: the target's own
		{5, "", false},            // untitled: the target's own
		{0, "Main", true},         // no number, by title
		{0, "  ", false},          // blank title
		{9, "inspector", false},   // exact title only
		{7, "Main Window", false}, // a title merely containing one
	}
	for _, c := range cases {
		if got := ListedTopLevel(c.id, c.title, listed); got != c.want {
			t.Errorf("%d %q: %v", c.id, c.title, got)
		}
	}
}

func TestMenuShortcutCombo(t *testing.T) {
	cases := []struct {
		char string
		mods any
		want string
	}{
		{"S", 0.0, "cmd+s"},
		{"S", float64(ModShift), "shift+cmd+s"},
		{"Q", float64(ModControl | ModOption), "ctrl+alt+cmd+q"},
		{"F", float64(ModNoCommand), "f"},
		{"F", float64(ModNoCommand | ModControl), "ctrl+f"},
		{" ", nil, "cmd+space"},
		{"", 0.0, ""},
		{"", 0.0, ""}, // F1's private-use character
		{"", 0.0, ""},
	}
	for _, c := range cases {
		if got := MenuShortcut(c.char, c.mods); got != c.want {
			t.Errorf("%q %v: %q, want %q", c.char, c.mods, got, c.want)
		}
	}
}

func TestOtherSpaceWindowsListed(t *testing.T) {
	spaces := []Space{
		{ID: 1, Type: SpaceUser, Current: true},
		{ID: 2, Type: SpaceUser},
		{ID: 3, Type: SpaceFullscreen},
		{ID: 4, Type: 2}, // a system Space
	}
	of := map[uint32][]uint64{10: {2}, 11: {3}, 12: {4}, 13: {1}, 14: {2}, 15: {2}, 16: nil, 17: {1, 2}}
	all := []ServerWindow{
		{ID: 10, PID: 1, Alpha: 1, Title: "on desktop 2"},
		{ID: 11, PID: 1, Alpha: 1, Title: "full screen"},
		{ID: 12, PID: 1, Alpha: 1, Title: "system space"},
		{ID: 13, PID: 1, Alpha: 1, Title: "minimized here"},
		{ID: 14, PID: 2, Alpha: 1, Layer: 3, Title: "panel"},
		{ID: 15, PID: 3, Alpha: 1, Title: "skipped"},
		{ID: 16, PID: 1, Alpha: 1, Title: "no space"},
		{ID: 17, PID: 1, Alpha: 1, Title: "on every space"},
		{ID: 18, PID: 1, Alpha: 1, OnScreen: true, Title: "shown"},
	}
	got := OtherSpace(all, spaces, func(id uint32) []uint64 { return of[id] }, map[uint32]bool{}, func(w ServerWindow) bool { return w.PID == 3 })
	var titles []string
	for _, w := range got {
		titles = append(titles, w.Title)
	}
	if strings.Join(titles, ",") != "on desktop 2,full screen" {
		t.Errorf("listed %v", titles)
	}
}

func TestPidScopedReads(t *testing.T) {
	listed := []int{1, 2, 3}
	if got := ScopedPIDs(listed, 2, false); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("pid without allowlist: %v", got)
	}
	if got := ScopedPIDs(listed, 2, true); !reflect.DeepEqual(got, listed) {
		t.Errorf("pid with allowlist: %v", got)
	}
	if got := ScopedPIDs(listed, 0, false); !reflect.DeepEqual(got, listed) {
		t.Errorf("no pid: %v", got)
	}
	if got := ScopedPIDs(listed, 9, false); len(got) != 0 {
		t.Errorf("unlisted pid: %v", got)
	}
}

func TestReadMissAtTimeout(t *testing.T) {
	if MessagingTimeout != 500*time.Millisecond {
		t.Errorf("messaging timeout %s", MessagingTimeout)
	}
	cases := []struct {
		took time.Duration
		got  bool
		miss bool
	}{
		{449 * time.Millisecond, false, false},
		{450 * time.Millisecond, false, true},
		{600 * time.Millisecond, false, true},
		{600 * time.Millisecond, true, false},
		{10 * time.Millisecond, false, false}, // an app with no windows that answered fast
	}
	for _, c := range cases {
		if IsMiss(c.took, c.got) != c.miss {
			t.Errorf("%s windows=%v: want miss %v", c.took, c.got, c.miss)
		}
	}
}

func TestSkipsDriverWindows(t *testing.T) {
	server := []ServerWindow{
		{ID: 1, PID: 50, Owner: "cua-driver", Layer: 0, Alpha: 1, Bounds: Rect{0, 0, 1000, 800}},
		{ID: 2, PID: 60, Owner: "TextEdit", Layer: 0, Alpha: 1, Bounds: Rect{10, 10, 300, 200}},
		{ID: 3, PID: 70, Owner: "Dock", Layer: 20, Alpha: 1},
		{ID: 4, PID: 80, Owner: "Ghost", Layer: 0, Alpha: 0},
	}
	l := ListApps(server, DriverFilter{}.Is)
	if !reflect.DeepEqual(l.PIDs, []int{60}) {
		t.Errorf("apps read %v", l.PIDs)
	}
	if _, ok := l.Rank[1]; ok || l.Shown[50] != nil {
		t.Error("the driver's overlay is listed")
	}
	if l.Rank[2] != 1 || l.Names[60] != "TextEdit" || l.Shown[60][2] != (Rect{10, 10, 300, 200}) {
		t.Errorf("listing %+v", l)
	}
}

// Dialogs another process raises (osascript's display dialog sits on the
// modal panel layer) and the Spotlight window are listed; the Dock, the menu
// bar and overlays are not.
func TestListsPanelsAndSpotlight(t *testing.T) {
	server := []ServerWindow{
		{ID: 1, PID: 90, Owner: "osascript", Title: "Keyboard Settings", Layer: 8, Alpha: 1},
		{ID: 2, PID: 91, Owner: "Spotlight", Layer: 25, Alpha: 1},
		{ID: 3, PID: 60, Owner: "TextEdit", Layer: 0, Alpha: 1},
		{ID: 4, PID: 92, Owner: "Inspector", Layer: 3, Alpha: 1},
		{ID: 5, PID: 70, Owner: "Dock", Layer: 20, Alpha: 1},
		{ID: 6, PID: 71, Owner: "Window Server", Layer: 24, Alpha: 1},
		{ID: 7, PID: 72, Owner: "Control Center", Layer: 25, Alpha: 1},
		{ID: 8, PID: 91, Owner: "Spotlight", Layer: OverlayLayer, Alpha: 1},
		{ID: 9, PID: 93, Owner: "osascript", Layer: 8, Alpha: 0},
	}
	l := ListApps(server, nil)
	if !reflect.DeepEqual(l.PIDs, []int{90, 91, 60, 92}) {
		t.Errorf("apps read %v", l.PIDs)
	}
	if _, ok := l.Rank[8]; ok {
		t.Error("a Spotlight overlay is listed")
	}
	if l.Rank[1] != 0 || l.Names[90] != "osascript" {
		t.Errorf("listing %+v", l)
	}
}

func TestSpaceSymbolsOptional(t *testing.T) {
	if Symbol("fennecCuaNoSuchSymbol") != nil {
		t.Error("a missing symbol resolved")
	}
	if Symbol("CFRelease") == nil {
		t.Error("a present symbol did not resolve")
	}
	full := func(name string) unsafe.Pointer { return Symbol("CFRelease") }
	if resolveSpaces(full) == nil {
		t.Error("all symbols present but no API")
	}
	for _, missing := range []string{"MainConnectionID", "CopyManagedDisplaySpaces", "CopySpacesForWindows"} {
		lookup := func(name string) unsafe.Pointer {
			if strings.HasSuffix(name, missing) {
				return nil
			}
			return Symbol("CFRelease")
		}
		if resolveSpaces(lookup) != nil {
			t.Errorf("resolved without %s", missing)
		}
	}
	// The SkyLight name is preferred, the CoreGraphics one is a fallback.
	var asked []string
	resolveSpaces(func(name string) unsafe.Pointer {
		asked = append(asked, name)
		if strings.HasPrefix(name, "CGS") {
			return Symbol("CFRelease")
		}
		return nil
	})
	if len(asked) != 6 || asked[0] != "SLSMainConnectionID" || asked[1] != "CGSMainConnectionID" {
		t.Errorf("lookups %v", asked)
	}
	// With a symbol missing, the listing degrades to the current Space.
	old := SpaceLookup
	SpaceLookup = func(string) unsafe.Pointer { return nil }
	defer func() { SpaceLookup = old }()
	ws, err := OtherSpaceWindows(nil, nil)
	if err != nil || ws != nil {
		t.Errorf("degraded listing %v %v", ws, err)
	}
}

func TestStaleWindowActiveOnlyWhenFront(t *testing.T) {
	cases := []struct {
		name  string
		ws    []StaleCandidate
		front int
		want  int
	}{
		{"front app's first stale window", []StaleCandidate{{PID: 2}, {PID: 1, Stale: true}, {PID: 1, Stale: true}}, 1, 1},
		{"last seen active wins", []StaleCandidate{{PID: 1, Stale: true}, {PID: 1, Stale: true, WasActive: true}}, 1, 1},
		{"an answering window is active", []StaleCandidate{{PID: 2, Active: true}, {PID: 1, Stale: true}}, 1, -1},
		{"app not in front", []StaleCandidate{{PID: 1, Stale: true, WasActive: true}}, 2, -1},
		{"front app without windows", []StaleCandidate{{PID: 1, Stale: true}, {PID: 2}}, 3, -1},
		{"front unknown", []StaleCandidate{{PID: 1, Stale: true}}, 0, -1},
	}
	for _, c := range cases {
		if got := StaleActive(c.ws, c.front); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTopListedWindowBelowOverlays(t *testing.T) {
	ws := []ServerWindow{
		{ID: 1, PID: 1, Layer: 102, Alpha: 1, Bounds: Rect{0, 0, 1000, 1000}}, // cursor overlay
		{ID: 2, PID: 2, Layer: 500, Alpha: 1, Bounds: Rect{0, 0, 1000, 1000}}, // shield
		{ID: 3, PID: 3, Layer: 3, Alpha: 0, Bounds: Rect{0, 0, 1000, 1000}},   // transparent
		{ID: 4, PID: 4, Layer: 0, Alpha: 1, Bounds: Rect{0, 0, 100, 100}},     // skipped process
		{ID: 5, PID: 5, Layer: 101, Alpha: 1, Bounds: Rect{50, 50, 100, 100}}, // highest normal layer
		{ID: 6, PID: 6, Layer: 0, Alpha: 1, Bounds: Rect{0, 0, 1000, 1000}},
		{ID: 7, PID: 7, Layer: -1, Alpha: 1, Bounds: Rect{0, 0, 1000, 1000}},
	}
	skip := map[int]bool{4: true}
	for _, c := range []struct {
		x, y float64
		want uint32
	}{{10, 10, 6}, {60, 60, 5}, {149, 149, 5}, {150, 150, 6}, {150.5, 10, 6}, {999, 999, 6}} {
		w, ok := TopWindowAt(ws, c.x, c.y, skip)
		if !ok || w.ID != c.want {
			t.Errorf("(%v,%v): %d %v, want %d", c.x, c.y, w.ID, ok, c.want)
		}
	}
	if _, ok := TopWindowAt(ws, 1000, 1000, skip); ok {
		t.Error("a point over no window has an owner")
	}
}

func TestUnseenClassification(t *testing.T) {
	cases := []struct {
		on, min, other bool
		want           string
	}{
		{true, false, false, ""},
		{false, false, true, UnseenOtherSpace},
		{false, true, false, UnseenMinimized},
		{false, false, false, UnseenHidden},
		{false, true, true, UnseenOtherSpace},
	}
	for _, c := range cases {
		if got := Unseen(c.on, c.min, c.other); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

// fakeSwitch is one app's web-tree switch.
type fakeSwitch struct {
	mu        sync.Mutex
	on        bool
	gets      int
	sets      []bool
	slowFirst time.Duration
}

func (f *fakeSwitch) tree(allowed func(int) bool) *WebTree {
	return &WebTree{
		Get: func(int) (bool, error) {
			f.mu.Lock()
			f.gets++
			on, slow := f.on, f.slowFirst
			f.mu.Unlock()
			time.Sleep(slow)
			return on, nil
		},
		Set: func(_ int, on bool) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.on = on
			f.sets = append(f.sets, on)
			return nil
		},
		Allowed: allowed,
	}
}

func TestWebTreeConcurrentFirstHolds(t *testing.T) {
	f := &fakeSwitch{slowFirst: 20 * time.Millisecond}
	w := f.tree(nil)
	var wg sync.WaitGroup
	var wasOn atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			on, err := w.Hold(1, string(rune('a'+i)))
			if err != nil {
				t.Error(err)
			}
			if on {
				wasOn.Add(1)
			}
		}()
	}
	wg.Wait()
	if f.gets != 1 || !reflect.DeepEqual(f.sets, []bool{true}) || wasOn.Load() != 0 {
		t.Fatalf("original state recorded %d times, sets %v, wasOn %d", f.gets, f.sets, wasOn.Load())
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Release(1, string(rune('a'+i))) }()
	}
	wg.Wait()
	if f.on || !reflect.DeepEqual(f.sets, []bool{true, false}) {
		t.Errorf("after all releases on=%v sets %v", f.on, f.sets)
	}
}

func TestWebTreeHolders(t *testing.T) {
	f := &fakeSwitch{}
	w := f.tree(func(pid int) bool { return pid != 9 })
	if on, err := w.Hold(1, "session"); err != nil || on {
		t.Fatalf("first hold %v %v", on, err)
	}
	if on, _ := w.Hold(1, "run"); on {
		t.Error("the second holder was told it was on before the first hold")
	}
	_ = w.Release(1, "session")
	if !f.on {
		t.Fatal("turned off while a holder remains")
	}
	// A run lets go when it ends, however it ends.
	if err := w.ReleaseHolder("run"); err != nil || f.on {
		t.Fatalf("still on after the last holder: %v %v", f.on, err)
	}
	// Originally on: reported, and never turned off.
	f2 := &fakeSwitch{on: true}
	w2 := f2.tree(nil)
	if on, _ := w2.Hold(1, "a"); !on {
		t.Error("an already-on tree was not reported")
	}
	_ = w2.Release(1, "a")
	if !f2.on || len(f2.sets) != 0 {
		t.Errorf("an originally-on tree was switched: %v", f2.sets)
	}
	// Outside the allowlist: refused, left alone.
	f3 := &fakeSwitch{}
	w3 := f3.tree(func(int) bool { return false })
	if _, err := w3.Hold(9, "a"); !errors.Is(err, ErrWebTreeRefused) || f3.gets != 0 || len(f3.sets) != 0 {
		t.Errorf("refused app touched: %v gets %d sets %v", err, f3.gets, f3.sets)
	}
}

func TestWebTreeHoldRefcount(t *testing.T) {
	f := &fakeSwitch{}
	w := f.tree(nil)
	_, _ = w.Hold(1, "a")
	_, _ = w.Hold(1, "a") // the same caller twice is one hold
	_, _ = w.Hold(1, "b")
	if w.Holders(1) != 2 {
		t.Errorf("holders %d", w.Holders(1))
	}
	_ = w.Release(1, "a")
	_ = w.Release(1, "c") // never held
	if !f.on {
		t.Fatal("off while b holds")
	}
	_ = w.Release(1, "b")
	if f.on || w.Holders(1) != 0 {
		t.Errorf("left on after both released")
	}
	// A new round records the state again.
	_, _ = w.Hold(1, "a")
	_ = w.Release(1, "a")
	if f.on || f.gets != 2 {
		t.Errorf("second round: on=%v gets %d", f.on, f.gets)
	}
}

func TestWindowBounds(t *testing.T) {
	got := ToDesk(Rect{X: -10.25, Y: 34, W: 100.5, H: 20}, 2)
	if got == nil || *got != [4]int{-21, 68, 201, 40} {
		t.Errorf("scaled bounds %v", got)
	}
	if got := ToDesk(Rect{X: 10, Y: 20, W: 300, H: 200}, 1); *got != [4]int{10, 20, 300, 200} {
		t.Errorf("bounds at scale 1 %v", *got)
	}
	if ToDesk(Rect{}, 2) != nil {
		t.Error("an empty rectangle has bounds")
	}
}

func TestWindowFingerprintPolling(t *testing.T) {
	a := []ServerWindow{{ID: 1, PID: 2, Title: "A"}}
	b := []ServerWindow{{ID: 1, PID: 2, Title: "B"}}
	if Fingerprint(a) == Fingerprint(b) || Fingerprint(a) != Fingerprint([]ServerWindow{{ID: 1, PID: 2, Title: "A"}}) {
		t.Fatal("fingerprint does not follow titles")
	}
	run := func(listTook time.Duration, changeAt time.Duration, limit time.Duration) (lists int, firstAfterChange time.Duration) {
		now := time.Unix(0, 0)
		start := now
		firstAfterChange = -1
		p := WindowPoll{
			Now:   func() time.Time { return now },
			Sleep: func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil },
			Fingerprint: func() (uint64, bool) {
				if changeAt > 0 && now.Sub(start) >= changeAt {
					return 2, true
				}
				return 1, true
			},
			List: func(context.Context) (bool, error) {
				lists++
				if changeAt > 0 && now.Sub(start) >= changeAt && firstAfterChange < 0 {
					firstAfterChange = now.Sub(start)
				}
				now = now.Add(listTook)
				return false, nil
			},
		}
		if _, err := p.Run(context.Background(), limit); err != nil {
			t.Fatal(err)
		}
		return lists, firstAfterChange
	}
	// A steady screen: about one list per 100 ms, not one per 10 ms tick.
	if n, _ := run(time.Millisecond, 0, time.Second); n < 8 || n > 11 {
		t.Errorf("steady screen read %d full lists in 1 s", n)
	}
	// A change is read on the next tick.
	if _, at := run(time.Millisecond, 505*time.Millisecond, time.Second); at < 505*time.Millisecond || at > 520*time.Millisecond {
		t.Errorf("change at 505 ms read at %s", at)
	}
	// A slow list read spaces the next ones by its duration.
	if n, _ := run(300*time.Millisecond, 0, 2*time.Second); n < 3 || n > 5 {
		t.Errorf("slow lists read %d times in 2 s", n)
	}
	// The wait ends when the list says so.
	p := WindowPoll{Fingerprint: func() (uint64, bool) { return 1, true }, List: func(context.Context) (bool, error) { return true, nil }}
	if done, err := p.Run(context.Background(), time.Second); !done || err != nil {
		t.Errorf("done %v %v", done, err)
	}
	// Cancelling stops at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Run(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestWindowIDsAreNumbers(t *testing.T) {
	if WindowKey(4242) != "cg:4242" {
		t.Errorf("key %q", WindowKey(4242))
	}
	if id, ok := WindowNumber(WindowKey(4242)); !ok || id != 4242 {
		t.Errorf("number %d %v", id, ok)
	}
	sheet := ProcessScopedKey(77, 0xbeef)
	if sheet != "ax:77:beef" {
		t.Errorf("sheet key %q", sheet)
	}
	if _, ok := WindowNumber(sheet); ok {
		t.Error("a process-scoped key read as a window number")
	}
	if _, ok := WindowNumber("cg:x"); ok {
		t.Error("garbage parsed")
	}
}

func TestWindowIDStableAcrossSpaces(t *testing.T) {
	// The same window listed through accessibility on this Space, and
	// through the window server only once it moved to another Space, keeps
	// one key; a window already listed is not listed twice.
	const id = 314
	here := WindowKey(id)
	spaces := []Space{{ID: 1, Current: true}, {ID: 2}}
	moved := []ServerWindow{{ID: id, PID: 5, Alpha: 1, Title: "Doc"}}
	others := OtherSpace(moved, spaces, func(uint32) []uint64 { return []uint64{2} }, nil, nil)
	if len(others) != 1 || WindowKey(others[0].ID) != here {
		t.Fatalf("other-Space listing %+v", others)
	}
	if again := OtherSpace(moved, spaces, func(uint32) []uint64 { return []uint64{2} }, map[uint32]bool{id: true}, nil); len(again) != 0 {
		t.Errorf("listed twice: %+v", again)
	}
}

type state struct{ n int }

func TestWorkerOrder(t *testing.T) {
	w := &Worker[*state]{Open: func() (*state, error) { return &state{}, nil }}
	defer w.Shutdown()
	var mu sync.Mutex
	var got []int
	var wg sync.WaitGroup
	release := make(chan struct{})
	// The first call blocks the worker so the rest queue up in order.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = w.Do(context.Background(), time.Second, func(*state) error { <-release; return nil })
	}()
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Do(context.Background(), time.Second, func(s *state) error {
				mu.Lock()
				got = append(got, i)
				mu.Unlock()
				s.n++
				return nil
			})
		}()
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()
	if !reflect.DeepEqual(got, []int{0, 1, 2, 3, 4}) {
		t.Errorf("order %v", got)
	}
	var n int
	_ = w.Do(context.Background(), time.Second, func(s *state) error { n = s.n; return nil })
	if n != 5 {
		t.Errorf("calls did not share one state: %d", n)
	}
	want := errors.New("boom")
	if err := w.Do(context.Background(), time.Second, func(*state) error { return want }); err != want {
		t.Errorf("error %v", err)
	}
}

func TestWorkerReplacedAfterHang(t *testing.T) {
	var opens atomic.Int32
	w := &Worker[int]{Open: func() (int, error) { return int(opens.Add(1)), nil }}
	defer w.Shutdown()
	hang := make(chan struct{})
	defer close(hang)
	err := w.Do(context.Background(), 50*time.Millisecond, func(int) error { <-hang; return nil })
	var te *TimeoutError
	if !errors.As(err, &te) || !te.Ran || !strings.Contains(err.Error(), "50 ms") || !strings.Contains(err.Error(), "may still take effect") {
		t.Fatalf("hang answered %v", err)
	}
	var gen int
	if err := w.Do(context.Background(), time.Second, func(s int) error { gen = s; return nil }); err != nil {
		t.Fatal(err)
	}
	if gen != 2 || w.Restarts() != 1 {
		t.Errorf("the next call ran on state %d after %d restarts", gen, w.Restarts())
	}
}

func TestWorkerSingleRestart(t *testing.T) {
	var opens atomic.Int32
	w := &Worker[int]{Open: func() (int, error) { return int(opens.Add(1)), nil }}
	defer w.Shutdown()
	hang := make(chan struct{})
	defer close(hang)
	started := make(chan struct{})
	go func() {
		_ = w.Do(context.Background(), 100*time.Millisecond, func(int) error { close(started); <-hang; return nil })
	}()
	<-started
	var wg sync.WaitGroup
	ran := make([]atomic.Bool, 6)
	timedOut := make([]bool, 6)
	for i := range ran {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := w.Do(context.Background(), 60*time.Millisecond, func(int) error { ran[i].Store(true); return nil })
			var te *TimeoutError
			if errors.As(err, &te) {
				timedOut[i] = !te.Ran
			} else if err != nil {
				t.Errorf("queued call: %v", err)
			}
		}()
	}
	wg.Wait()
	time.Sleep(150 * time.Millisecond)
	if r := w.Restarts(); r != 1 {
		t.Errorf("%d replacements for one hang", r)
	}
	// A queued call that timed out never runs later, even on the fresh
	// worker; the ones still waiting at the replacement run there.
	for i := range ran {
		if timedOut[i] && ran[i].Load() {
			t.Errorf("call %d ran after timing out while queued", i)
		}
	}
	if err := w.Do(context.Background(), time.Second, func(int) error { return nil }); err != nil {
		t.Errorf("fresh worker: %v", err)
	}
}

func TestWorkerOpenFailure(t *testing.T) {
	var opens atomic.Int32
	fail := errors.New("accessibility is not enabled for fennec-cua")
	w := &Worker[int]{Open: func() (int, error) {
		if opens.Add(1) <= 2 {
			return 0, fail
		}
		return 7, nil
	}}
	defer w.Shutdown()
	ran := false
	for i := 0; i < 2; i++ {
		if err := w.Do(context.Background(), time.Second, func(int) error { ran = true; return nil }); !errors.Is(err, fail) {
			t.Errorf("call %d: %v", i, err)
		}
	}
	if ran {
		t.Error("a call ran without a backend")
	}
	var got int
	if err := w.Do(context.Background(), time.Second, func(s int) error { got = s; return nil }); err != nil || got != 7 {
		t.Errorf("after the backend opened: %d %v", got, err)
	}
	if opens.Load() != 3 {
		t.Errorf("opened %d times", opens.Load())
	}
}
