package ax

import (
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// ServerWindow is one entry of the window server's list.
type ServerWindow struct {
	ID       uint32
	PID      int
	Owner    string
	Title    string
	Layer    int
	Alpha    float64
	OnScreen bool
	Bounds   Rect // points, top-left origin
}

// OverlayLayer is the first window server layer of overlays (the cursor,
// screen shields); windows at or above it are never an app's.
const OverlayLayer = 102

// WindowKey is the identity of a window with a window server number. It is
// derived from the number alone, so it is the same whether the window is
// listed through accessibility on the current Space or through the window
// server from another Space.
func WindowKey(id uint32) string { return "cg:" + strconv.FormatUint(uint64(id), 10) }

// ProcessScopedKey is the identity of a window without a window server
// number (a sheet, a minimized window): scoped to its process.
func ProcessScopedKey(pid int, hash uint64) string { return fmt.Sprintf("ax:%d:%x", pid, hash) }

// WindowNumber reads the window server number back from a WindowKey.
func WindowNumber(key string) (uint32, bool) {
	if !strings.HasPrefix(key, "cg:") {
		return 0, false
	}
	n, err := strconv.ParseUint(key[3:], 10, 32)
	return uint32(n), err == nil
}

// ToDesk converts a rectangle in AX points to desktop coordinates (main
// display pixels), [x, y, w, h], rounding each; nil for an empty rectangle.
func ToDesk(r Rect, scale float64) *[4]int {
	if r.W <= 0 && r.H <= 0 {
		return nil
	}
	return &[4]int{int(math.Round(r.X * scale)), int(math.Round(r.Y * scale)), int(math.Round(r.W * scale)), int(math.Round(r.H * scale))}
}

// FrameShown reports whether an accessibility window with frame r counts as
// shown: its frame matches one of its process's on-screen window-server
// bounds within 1 point on every edge. A window whose frame could not be
// read counts as shown.
func FrameShown(bounds []Rect, r Rect, hasFrame bool) bool {
	if !hasFrame {
		return true
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1 }
	for _, b := range bounds {
		if near(r.X, b.X) && near(r.Y, b.Y) && near(r.X+r.W, b.X+b.W) && near(r.Y+r.H, b.Y+b.H) {
			return true
		}
	}
	return false
}

// TopWindowAt scans a front-to-back window-server list for the first window
// that contains the point (in points), sits on a layer from 0 up to but
// excluding OverlayLayer and is not fully transparent; processes in skip are
// passed over. ok is false over no such window.
func TopWindowAt(ws []ServerWindow, x, y float64, skip map[int]bool) (ServerWindow, bool) {
	for _, w := range ws {
		if w.Layer < 0 || w.Layer >= OverlayLayer || w.Alpha <= 0 || skip[w.PID] {
			continue
		}
		if w.Bounds.Contains(x, y) {
			return w, true
		}
	}
	return ServerWindow{}, false
}

// Listing is the app windows of a window-server list.
type Listing struct {
	// PIDs in front-to-back order of their first window.
	PIDs  []int
	Names map[int]string
	// Rank is each window's front-to-back position.
	Rank map[uint32]int
	// Shown holds each process's on-screen window bounds by number.
	Shown map[int]map[uint32]Rect
}

// ListApps keeps the normal app windows (layer 0, visible) of a
// window-server list, dropping every window skip names, such as the bundled
// driver's overlay. Skipped processes are never read.
func ListApps(server []ServerWindow, skip func(ServerWindow) bool) Listing {
	l := Listing{Names: map[int]string{}, Rank: map[uint32]int{}, Shown: map[int]map[uint32]Rect{}}
	for i, w := range server {
		if w.Layer != 0 || w.Alpha <= 0 || (skip != nil && skip(w)) {
			continue
		}
		l.Rank[w.ID] = i
		if l.Shown[w.PID] == nil {
			l.Shown[w.PID] = map[uint32]Rect{}
			l.PIDs = append(l.PIDs, w.PID)
			l.Names[w.PID] = w.Owner
		}
		l.Shown[w.PID][w.ID] = w.Bounds
	}
	return l
}

// DriverName is the bundled driver's process name, used to recognise it
// when its path is not known.
const DriverName = "cua-driver"

// DriverFilter recognises the bundled cua-driver's windows.
type DriverFilter struct {
	// Path is the driver binary beside the daemon; when set, a process is
	// the driver exactly when its canonical executable path is this one's.
	// When empty, the driver is recognised by its process name.
	Path string
	// PathOf resolves a process's executable path; ProcessPath by default.
	PathOf func(pid int) (string, error)
}

// Is reports whether a window-server window belongs to the driver. A
// process whose path cannot be resolved is not the driver.
func (f DriverFilter) Is(w ServerWindow) bool {
	if f.Path == "" {
		return w.Owner == DriverName
	}
	pathOf := f.PathOf
	if pathOf == nil {
		pathOf = ProcessPath
	}
	p, err := pathOf(w.PID)
	if err != nil || p == "" {
		return false
	}
	return canonical(p) == canonical(f.Path)
}

func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

// StaleCandidate is one listed window as StaleActive sees it.
type StaleCandidate struct {
	PID    int
	Stale  bool
	Active bool
	// WasActive is set when the window was active at its app's last answer.
	WasActive bool
}

// StaleActive picks the stale window to report active, or -1. A stale window
// is active only when its app is frontmost and no answering window is
// active; of that app's stale windows the one last seen active wins, else
// its first. A stale window of an app that is not in front is never active.
func StaleActive(ws []StaleCandidate, frontPID int) int {
	if frontPID == 0 {
		return -1
	}
	first := -1
	for i, w := range ws {
		if !w.Stale && w.Active {
			return -1
		}
		if w.Stale && w.PID == frontPID && first < 0 {
			first = i
		}
	}
	for i, w := range ws {
		if w.Stale && w.PID == frontPID && w.WasActive {
			return i
		}
	}
	return first
}

// Unseen kinds of a window.
const (
	UnseenOtherSpace = "other-space"
	UnseenMinimized  = "minimized"
	UnseenHidden     = "hidden"
)

// Unseen classifies a window that is not plainly shown: on another Space,
// or (macOS) not on screen because it is minimized or hidden. It answers ""
// for a window on screen.
func Unseen(onScreen, minimized, otherSpace bool) string {
	switch {
	case otherSpace:
		return UnseenOtherSpace
	case minimized:
		return UnseenMinimized
	case !onScreen:
		return UnseenHidden
	}
	return ""
}

// ScopedPIDs is the set of apps an outline, find, hit test or close reads.
// A call naming a pid reads only that app, unless an allowlist is in force:
// then every listed app is read so the allowlist judges the full picture. A
// pid-less call reads every app.
func ScopedPIDs(listed []int, pid int, allowlist bool) []int {
	if pid == 0 || allowlist {
		return listed
	}
	for _, p := range listed {
		if p == pid {
			return []int{pid}
		}
	}
	return nil
}

// Listed is a top-level window of the desktop listing.
type Listed struct {
	ID    uint32
	Title string
}

// ListedTopLevel reports whether a covering window of the target's app is a
// listed top-level window (and so may be raised past). An untitled cover is
// the target's own (a popup); otherwise it is matched against the listing
// by window number, else by title.
func ListedTopLevel(id uint32, title string, listed []Listed) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}
	if id != 0 {
		for _, l := range listed {
			if l.ID == id {
				return true
			}
		}
	}
	for _, l := range listed {
		if l.Title == title {
			return true
		}
	}
	return false
}

// Fingerprint is a cheap hash of the on-screen windows (owner, number,
// title) that changes when windows open, close or retitle.
func Fingerprint(ws []ServerWindow) uint64 {
	h := fnv.New64a()
	for _, w := range ws {
		fmt.Fprintf(h, "%d\x00%d\x00%s\x01", w.PID, w.ID, w.Title)
	}
	return h.Sum64()
}

// Space kinds the window server reports.
const (
	SpaceUser       = 0
	SpaceFullscreen = 4
)

// Space is one managed Space.
type Space struct {
	ID      uint64
	Type    int
	Current bool
}

// SpaceListed reports whether a Space kind holds app windows: user desktop
// Spaces and full-screen Spaces do; system and other kinds do not.
func SpaceListed(kind int) bool { return kind == SpaceUser || kind == SpaceFullscreen }

// OtherSpace picks, from the full window-server list, the app windows that
// sit on another listed Space: layer 0, visible, not on screen, on at least
// one user or full-screen Space and on no current Space. spacesOf answers a
// window's Spaces; windows in listed (already shown) and those skip names
// are passed over.
func OtherSpace(all []ServerWindow, spaces []Space, spacesOf func(id uint32) []uint64, listed map[uint32]bool, skip func(ServerWindow) bool) []ServerWindow {
	kinds := map[uint64]Space{}
	for _, s := range spaces {
		kinds[s.ID] = s
	}
	var out []ServerWindow
	for _, w := range all {
		if w.Layer != 0 || w.Alpha <= 0 || w.OnScreen || listed[w.ID] || (skip != nil && skip(w)) {
			continue
		}
		other := false
		current := false
		for _, id := range spacesOf(w.ID) {
			s, ok := kinds[id]
			if !ok || !SpaceListed(s.Type) {
				continue
			}
			if s.Current {
				current = true
			} else {
				other = true
			}
		}
		if other && !current {
			out = append(out, w)
		}
	}
	return out
}
