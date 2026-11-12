package flows

import (
	"strings"

	"fennec/cua/internal/view"
)

// AppWindow is the window an app step with a title waits for: a window of
// that app whose title matches, never just any window of the app. An app
// step without a title takes the app's front window.
func AppWindow(ws []view.RawWindow, app, title string) (view.RawWindow, bool) {
	var mine []view.RawWindow
	for _, w := range ws {
		if view.AppMatches(app, w.App) {
			mine = append(mine, w)
		}
	}
	if strings.TrimSpace(title) == "" {
		for _, w := range mine {
			if w.Active {
				return w, true
			}
		}
		if len(mine) > 0 {
			return mine[0], true
		}
		return view.RawWindow{}, false
	}
	for tier := 0; tier < view.TitleTiers; tier++ {
		for _, w := range mine {
			if view.TitleMatches(tier, w.Title, title) {
				return w, true
			}
		}
	}
	return view.RawWindow{}, false
}

// AfterWindow checks a recorded after window once a step acted. In the
// foreground the window of that title must have come to the front by
// itself; one open behind the front window does not count. In the
// background, where the app's windows open behind, it takes the new
// top-level window of the plan's process that the step opened.
func AfterWindow(before, now []view.RawWindow, title string, pid int, background bool) (view.RawWindow, bool) {
	if !background {
		for _, w := range now {
			if w.Active {
				if w.Title == title {
					return w, true
				}
				return view.RawWindow{}, false
			}
		}
		return view.RawWindow{}, false
	}
	old := map[string]bool{}
	for _, w := range before {
		old[w.Key] = true
	}
	for _, w := range now {
		if w.PID != pid || w.OwnerKey != "" || old[w.Key] || w.Role == "sheet" {
			continue
		}
		if w.Title == title {
			return w, true
		}
	}
	return view.RawWindow{}, false
}
