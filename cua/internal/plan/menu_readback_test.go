package plan

import (
	"strings"
	"testing"

	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
)

// AppKit updates a menu item's mark only when its menu is validated, as it
// opens: right after the press the item still reads as before. The read-back
// reopens the menu (found live: View > Show Status Bar read "still off" while
// the fixture showed it on).
func TestMenuToggleReadsBackInReopenedMenu(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	viewMenu(r, "Status Bar")
	real := false
	r.d.onPerform = func(id, _ string) {
		item := r.d.Node("item")
		switch id {
		case "item":
			// The command ran; the shown mark stays as it was.
			real = !real
			item.States = nil
			if !real {
				item.States = []string{"checked"}
			}
		case "view":
			item.States = nil
			if real {
				item.States = []string{"checked"}
			}
		}
	}
	res := r.run(protocol.Step{Menu: "View > Status Bar", Value: raw("true")})
	mustPass(t, res)
	if !strings.Contains(res.Steps[0].Evidence, "reads on") || !real {
		t.Fatalf("real=%v %s", real, jsonOf(res.Steps[0]))
	}
	n := 0
	for _, p := range r.d.Performs {
		if p == "item:AXPress" {
			n++
		}
	}
	// Both opens of the View menu are closed again.
	if n != 1 || menusClosed(r.d) != 2 {
		t.Fatalf("item pressed %d times, %d menus closed: %v", n, menusClosed(r.d), r.d.Performs)
	}
}

// A menu opened for accessibility is closed by its own AXCancel, not by an
// Escape key, which does not reliably reach it: a menu left open swallows
// every later press and hides the focused window (found live).
func TestMenuClosedByCancel(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	viewMenu(r, "Status Bar")
	mustPass(t, r.run(protocol.Step{Menu: "View > Status Bar"}))
	if presses(r.d) != "view:AXPress,item:AXPress" || escapes(r.d) != 0 || !strings.Contains(strings.Join(r.d.Performs, ","), "viewmenu:AXCancel") {
		t.Fatal(r.d.Performs, r.d)
	}
}

// presses lists the performed actions other than the AXCancel that closes
// a menu the step opened.
func presses(d *desk) string {
	var out []string
	for _, p := range d.Performs {
		if !strings.HasSuffix(p, ":AXCancel") {
			out = append(out, p)
		}
	}
	return strings.Join(out, ",")
}

// menusClosed counts menus closed by AXCancel or by an Escape key.
func menusClosed(d *desk) int {
	n := escapes(d)
	for _, p := range d.Performs {
		if strings.HasSuffix(p, ":AXCancel") {
			n++
		}
	}
	return n
}

// Under auto input a plan's first step may be a menu step: it runs in the
// front app's menu bar in the foreground (found live: it was refused for
// want of a plan window).
func TestAutoMenuWithoutPlanWindow(t *testing.T) {
	r := newRig(t, policy.Config{Input: protocol.InputAuto}).notes()
	viewMenu(r, "Status Bar")
	res := r.run(protocol.Step{Menu: "View > Status Bar"})
	mustPass(t, res)
	if presses(r.d) != "view:AXPress,item:AXPress" || res.Steps[0].Input != protocol.InputForeground {
		t.Fatalf("%v %s", r.d.Performs, jsonOf(res.Steps[0]))
	}
}
