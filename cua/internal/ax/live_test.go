//go:build darwin && axlive

// Read-only checks against the real desktop: they list windows and read the
// front app's tree. They send no input. Run with -tags axlive on a machine
// whose terminal has the Accessibility permission.
package ax

import (
	"os"
	"testing"
)

func TestLiveReadOnly(t *testing.T) {
	wins, err := ServerWindows(true)
	if err != nil {
		t.Fatal(err)
	}
	var front *ServerWindow
	for i := range wins {
		if wins[i].Layer == 0 && wins[i].Alpha > 0 && wins[i].PID != os.Getpid() {
			front = &wins[i]
			break
		}
	}
	if front == nil {
		t.Skip("no normal window on screen")
	}
	t.Logf("front window %d %q of %s (pid %d) at %+v; scale %.1f", front.ID, front.Title, front.Owner, front.PID, front.Bounds, BackingScale())
	app := App(front.PID)
	defer app.Release()
	app.SetTimeout(0.5)
	v, err := app.Attrs([]string{"AXRole", "AXTitle", "AXWindows", "AXFocusedWindow"})
	if err != nil {
		t.Fatal(err)
	}
	if v[0] != "AXApplication" {
		t.Errorf("role %v", v[0])
	}
	ws, _ := v[2].([]any)
	if len(ws) == 0 {
		t.Fatalf("no AX windows for %s", front.Owner)
	}
	matched := false
	for _, w := range ws {
		el := w.(*Element)
		id, ok := el.WindowID()
		attrs, err := el.Attrs([]string{"AXRole", "AXSubrole", "AXTitle", "AXPosition", "AXSize", "AXChildren"})
		if err != nil {
			t.Fatal(err)
		}
		kids, _ := attrs[5].([]any)
		t.Logf("  AX window id=%d(%v) %v/%v %q pos=%v size=%v children=%d", id, ok, attrs[0], attrs[1], attrs[2], attrs[3], attrs[4], len(kids))
		if ok && id == front.ID {
			matched = true
		}
	}
	if !matched {
		t.Errorf("no AX window maps to window server id %d", front.ID)
	}
}

func TestLiveSpacesReadOnly(t *testing.T) {
	if resolveSpaces(SpaceLookup) == nil {
		t.Skip("the Space calls are missing on this system")
	}
	ws, err := OtherSpaceWindows(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		t.Logf("other Space: %d %q of %s", w.ID, w.Title, w.Owner)
	}
	t.Logf("front pid %d", FrontmostPID())
}
