package view

import (
	"fmt"
	"strconv"
	"strings"

	"fennec/cua/internal/protocol"
)

// TitleTiers is how many title tiers TitleMatches has.
const TitleTiers = 3

// TitleMatches says whether title matches want at tier: 0 equal, 1 equal
// ignoring case and outer spaces, 2 containing it ignoring case.
func TitleMatches(tier int, title, want string) bool {
	switch tier {
	case 0:
		return title == want
	case 1:
		return strings.EqualFold(strings.TrimSpace(title), strings.TrimSpace(want))
	default:
		w := strings.ToLower(strings.TrimSpace(want))
		return w != "" && strings.Contains(strings.ToLower(title), w)
	}
}

// Shown says whether a window is on a display: not minimized, not of a
// hidden app, not on another Space.
func Shown(w RawWindow) bool {
	return w.Hidden == "" && w.Space == ""
}

// focusable says whether a window may stand for the front one: a plain
// window needs a title.
func focusable(w RawWindow) bool {
	return w.Title != "" || (w.Role != "" && w.Role != "window")
}

// ListWindows is the window list an observe shows under an input mode:
// windows on another Space are listed (never active) only under background
// or auto input.
func ListWindows(ws []RawWindow, input string) []RawWindow {
	out := make([]RawWindow, 0, len(ws))
	for _, w := range ws {
		if w.Space == "other" {
			if input != protocol.InputBackground && input != protocol.InputAuto {
				continue
			}
			w.Active = false
		}
		out = append(out, w)
	}
	return out
}

// WindowOf is a window's entry in the agent's list; optional fields are
// omitted when unknown.
func WindowOf(w RawWindow, ref, ownerRef string) protocol.Window {
	return protocol.Window{
		Ref:    ref,
		App:    StripObjects(w.App),
		Title:  StripObjects(w.Title),
		Role:   orDefault(w.Role, "window"),
		PID:    w.PID,
		Active: w.Active,
		Bounds: w.Bounds,
		Owner:  ownerRef,
		Space:  w.Space,
		Hidden: w.Hidden,
		Stale:  w.Stale,
	}
}

// Actionable refuses reads and actions aimed at a window that cannot take
// them: a stale window (its app did not answer) and, under pure background
// input, a window on another Space.
func Actionable(w RawWindow, input string) error {
	if w.Stale {
		return &protocol.Error{
			Code:    protocol.ErrNotResponding,
			Message: fmt.Sprintf("%s (%s) is busy or hung: its window %q is listed from an earlier read and was not touched", orDefault(w.App, "the app"), pidText(w.PID), w.Title),
			Hint:    "wait for the app to answer, then observe again",
		}
	}
	if w.Space == "other" && input == protocol.InputBackground {
		return &protocol.Error{
			Code:    protocol.ErrOffSpace,
			Message: fmt.Sprintf("window %q is on another Space, where accessibility does not reach it", w.Title),
			Hint:    "switch to its Space or use foreground or auto input",
		}
	}
	return nil
}

func pidText(pid int) string {
	if pid == 0 {
		return "pid unknown"
	}
	return "pid " + strconv.Itoa(pid)
}

// PickWindow picks a window by title (want) and pid, from a list in front
// to back order.
//
// With want, titles match by tiers (exact, then trimmed case-insensitive,
// then contained); at the first tier that matches, the active window wins,
// then among equal titles the front-most shown one, and a lone hidden or
// minimized window is still taken. Matches with different titles and none
// active are ambiguous, and so are twins on different Spaces. A stale title
// ("inbox - Thunar") falls back to its app's window ("images - Thunar") when
// that is the only one or the active one. With pid only that app's windows
// count, and a pid alone takes that app's front window. Without either, the
// active window.
func PickWindow(ws []RawWindow, want string, pid int) (RawWindow, error) {
	var pool []RawWindow
	for _, w := range ws {
		if pid == 0 || w.PID == pid {
			pool = append(pool, w)
		}
	}
	if strings.TrimSpace(want) == "" {
		var keep []RawWindow
		for _, w := range pool {
			if focusable(w) && (pid != 0 || w.Active) {
				keep = append(keep, w)
			}
		}
		if len(keep) == 0 {
			msg := "no focused window"
			if pid != 0 {
				msg = fmt.Sprintf("pid %d has no window", pid)
			}
			return RawWindow{}, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: msg, Hint: "name a window by title or ref; observe scope desktop lists them"}
		}
		return best(keep), nil
	}
	for tier := 0; tier < TitleTiers; tier++ {
		var hits []RawWindow
		for _, w := range pool {
			if TitleMatches(tier, w.Title, want) {
				hits = append(hits, w)
			}
		}
		if len(hits) == 0 {
			continue
		}
		return settle(hits, want)
	}
	return appWindow(pool, want)
}

// settle chooses among windows that match at one tier.
func settle(hits []RawWindow, want string) (RawWindow, error) {
	if len(hits) == 1 {
		return hits[0], nil
	}
	for _, w := range hits {
		if w.Active {
			return w, nil
		}
	}
	same, spaces := true, map[string]bool{}
	for _, w := range hits {
		same = same && strings.TrimSpace(w.Title) == strings.TrimSpace(hits[0].Title)
		spaces[w.Space] = true
	}
	if same && len(spaces) == 1 {
		return best(hits), nil
	}
	msg := fmt.Sprintf("%d windows match %q", len(hits), want)
	if same {
		msg = fmt.Sprintf("windows titled %q are on different Spaces", hits[0].Title)
	}
	return RawWindow{}, &protocol.Error{
		Code:    protocol.ErrTargetAmbiguous,
		Message: msg,
		Hint:    "windows: " + titles(hits) + "; name the window by ref or pid",
	}
}

// best is the active window, else the front-most shown one, else the
// front-most.
func best(ws []RawWindow) RawWindow {
	for _, w := range ws {
		if w.Active {
			return w
		}
	}
	for _, w := range ws {
		if Shown(w) {
			return w
		}
	}
	return ws[0]
}

// titleSeparators split a window title from its app part.
var titleSeparators = []string{" - ", " — "}

// appPart is the app part of a title: from its last separator on.
func appPart(want string) string {
	lower := strings.ToLower(strings.TrimSpace(want))
	at := -1
	for _, s := range titleSeparators {
		if i := strings.LastIndex(lower, s); i > at {
			at = i
		}
	}
	if at < 0 {
		return ""
	}
	return lower[at:]
}

// appWindow resolves a stale title to its app's window when that is
// unambiguous.
func appWindow(ws []RawWindow, want string) (RawWindow, error) {
	part := appPart(want)
	var hits []RawWindow
	if part != "" {
		for _, w := range ws {
			if strings.HasSuffix(strings.ToLower(strings.TrimRight(w.Title, " ")), part) {
				hits = append(hits, w)
			}
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	var active []RawWindow
	for _, w := range hits {
		if w.Active {
			active = append(active, w)
		}
	}
	if len(active) == 1 {
		return active[0], nil
	}
	if len(hits) == 0 {
		return RawWindow{}, &protocol.Error{
			Code:    protocol.ErrWindowNotFound,
			Message: fmt.Sprintf("no window %q", want),
			Hint:    "windows: " + titles(ws),
		}
	}
	return RawWindow{}, &protocol.Error{
		Code:    protocol.ErrWindowNotFound,
		Message: fmt.Sprintf("no window %q", want),
		Hint:    "windows of its app: " + titles(hits),
	}
}

func titles(ws []RawWindow) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = Field(w.Title, NameChars*2)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// FullTitles gives windows the driver lists (with titles the window server
// may elide) their full accessibility titles, matched by window key.
func FullTitles(listed, ax []RawWindow) []RawWindow {
	full := make(map[string]string, len(ax))
	for _, w := range ax {
		if w.Key != "" && w.Title != "" {
			full[w.Key] = w.Title
		}
	}
	out := make([]RawWindow, len(listed))
	for i, w := range listed {
		if t, ok := full[w.Key]; ok {
			w.Title = t
		}
		out[i] = w
	}
	return out
}

// contains says whether bounds hold a point.
func contains(b *[4]int, x, y int) bool {
	return b != nil && x >= b[0] && y >= b[1] && x < b[0]+b[2] && y < b[1]+b[3]
}

// WindowAt is the topmost shown window at a desktop point, from a list in
// front to back order. Same-titled twins of one app are told apart by their
// bounds; twins that cover the point with the same bounds cannot be, and are
// refused.
func WindowAt(ws []RawWindow, x, y int) (RawWindow, error) {
	var hits []RawWindow
	for _, w := range ws {
		if Shown(w) && !w.Stale && contains(w.Bounds, x, y) {
			hits = append(hits, w)
		}
	}
	if len(hits) == 0 {
		return RawWindow{}, &protocol.Error{
			Code:    protocol.ErrWindowNotFound,
			Message: fmt.Sprintf("no app window at (%d, %d)", x, y),
			Hint:    "name a window or pid, or observe scope desktop for window bounds",
		}
	}
	top := hits[0]
	for _, w := range hits[1:] {
		if w.PID == top.PID && w.Title == top.Title && *w.Bounds == *top.Bounds {
			return RawWindow{}, &protocol.Error{
				Code:    protocol.ErrTargetAmbiguous,
				Message: fmt.Sprintf("two windows titled %q of pid %d lie at (%d, %d) with the same bounds", top.Title, top.PID, x, y),
				Hint:    "name the window by ref",
			}
		}
	}
	return top, nil
}

// AppWindows are the windows of the app want names: by app name first (see
// AppMatches), else by whole words of the window title.
func AppWindows(ws []RawWindow, want string) []RawWindow {
	var out []RawWindow
	for _, w := range ws {
		if AppMatches(want, w.App) {
			out = append(out, w)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, w := range ws {
		if TitleHasWords(w.Title, want) {
			out = append(out, w)
		}
	}
	return out
}

// PickApp is the front window of the app want names.
func PickApp(ws []RawWindow, want string) (RawWindow, error) {
	if strings.TrimSpace(want) == "" {
		return RawWindow{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "an empty app name names nothing"}
	}
	hits := AppWindows(ws, want)
	if len(hits) == 0 {
		return RawWindow{}, &protocol.Error{
			Code:    protocol.ErrWindowNotFound,
			Message: fmt.Sprintf("no window of an app named %q; nothing was sent", want),
			Hint:    "launch the app first, or observe scope desktop for the open windows",
		}
	}
	return best(hits), nil
}
