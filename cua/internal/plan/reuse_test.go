package plan

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

func (c *clock2) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// events logs, in order, each window list the desk answered ("L"), each
// element action ("P:id"), close ("C:key") and driver call ("D:tool").
type events struct {
	mu  sync.Mutex
	log []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	e.log = append(e.log, s)
	e.mu.Unlock()
}

// lists counts the list reads after the first event from and before the
// first event to after it.
func (e *events) lists(from, to string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n, on := 0, false
	for _, s := range e.log {
		switch {
		case !on && s == from:
			on = true
		case on && s == to:
			return n
		case on && s == "L":
			n++
		}
	}
	if !on {
		return -1
	}
	return n
}

// after counts the list reads after the first event from.
func (e *events) after(from string) int { return e.lists(from, "\x00") }

func watch(r *rig) *events {
	ev := &events{}
	r.d.AfterList = func(*fakedesk.Desk, int) { ev.add("L") }
	r.d.onPerform = func(id, _ string) { ev.add("P:" + id) }
	r.d.onDriver = func(tool string, _ map[string]any) { ev.add("D:" + tool) }
	r.d.onClose = func(key string) bool { ev.add("C:" + key); return true }
	return ev
}

func TestSettleWindowListReuse(t *testing.T) {
	steps := []protocol.Step{{Press: sel(`button "Save"`)}, {Key: "a"}}
	// The list the press settled on serves the next step: one read between
	// the press and the key.
	r := newRig(t, policy.Config{}).notes()
	ev := watch(r)
	mustPass(t, r.run(steps...))
	if n := ev.lists("P:save", "D:press_key"); n != 1 {
		t.Fatalf("%d list reads between the steps: %v", n, ev.log)
	}
	// A list older than about 25 ms is read again.
	r = newRig(t, policy.Config{}).notes()
	ev = watch(r)
	pressed, aged := false, false
	r.d.onPerform = func(id, _ string) { pressed = true; ev.add("P:" + id) }
	r.d.AfterList = func(*fakedesk.Desk, int) {
		ev.add("L")
		if pressed && !aged {
			aged = true
			r.clk.advance(30 * time.Millisecond)
		}
	}
	mustPass(t, r.run(steps...))
	if n := ev.lists("P:save", "D:press_key"); n < 2 {
		t.Fatalf("an old list was reused: %v", ev.log)
	}
	// A settle that ended with no front window hands no list on.
	r = newRig(t, policy.Config{}).notes()
	ev = watch(r)
	r.d.onPerform = func(id, _ string) { r.d.Window("cg:1").Active = false; ev.add("P:" + id) }
	r.act(protocol.ActRequest{Window: "Notes", Steps: steps})
	if n := ev.lists("P:save", "D:press_key"); n < 2 {
		t.Fatalf("a frontless list was reused: %v", ev.log)
	}
	// A step that fell back to foreground input hands no list on.
	r = behind(t, "auto")
	field(r, "")
	ev = watch(r)
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("the app did not report the text"), fakedesk.OK}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("hello")}, protocol.Step{Key: "tab"})
	mustPass(t, res)
	var last int
	for i, s := range ev.log {
		if s == "D:type_text" {
			last = i
		}
	}
	n := 0
	for _, s := range ev.log[last:] {
		if s == "D:press_key" {
			break
		}
		if s == "L" {
			n++
		}
	}
	if n < 2 {
		t.Fatalf("the foreground fallback handed its list on: %v", ev.log)
	}
	// The end of the plan takes the settled list too.
	r = newRig(t, policy.Config{}).notes()
	ev = watch(r)
	r.e.WindowChange = func(ctx context.Context, _ time.Duration, list func(context.Context) (bool, error)) (bool, error) {
		return list(ctx)
	}
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`)}))
	if n := ev.after("P:save"); n != 1 {
		t.Fatalf("%d list reads after the last step: %v", n, ev.log)
	}
}

func TestSettleListReuse(t *testing.T) {
	// A close step: the list that saw the window gone serves the next step.
	r := newRig(t, policy.Config{}).notes()
	r.d.Window("cg:1").Active = false
	r.d.Win("cg:4", "TextEdit", "Find", 100, [4]int{10, 10, 200, 100}, true).Role = "panel"
	ev := watch(r)
	r.d.onClose = func(key string) bool {
		ev.add("C:" + key)
		r.d.Window("cg:1").Active = true
		return true
	}
	mustPass(t, r.run(protocol.Step{Close: "Find"}, protocol.Step{Key: "a"}))
	if n := ev.lists("C:cg:4", "D:press_key"); n != 1 {
		t.Fatalf("%d list reads between the close and the key: %v", n, ev.log)
	}
	// A click: the same, and another call in between (a tree read) means a
	// new read.
	r = newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "body", "textbox", "Body", fakedesk.Box(10, 300, 400, 200), "cg:1")
	ev = watch(r)
	res := r.run(protocol.Step{Click: sel(`button "Save"`)}, protocol.Step{Key: "a"}, protocol.Step{Press: sel(`textbox "Body"`)})
	mustPass(t, res)
	if n := ev.lists("P:save", "D:press_key"); n != 1 {
		t.Fatalf("%d list reads between the click and the key: %v", n, ev.log)
	}
	if n := ev.lists("D:press_key", "P:body"); n < 2 {
		t.Fatalf("a list was reused across a tree read: %v", ev.log)
	}
}

func TestWindowListReuseWindow(t *testing.T) {
	for _, c := range []struct {
		age   time.Duration
		reads int
	}{{0, 2}, {20 * time.Millisecond, 2}, {30 * time.Millisecond, 3}} {
		r := newRig(t, policy.Config{}).notes()
		r.d.AfterList = func(_ *fakedesk.Desk, n int) {
			if n == 1 {
				r.clk.advance(c.age)
			}
		}
		mustPass(t, r.run(protocol.Step{Key: "a"}))
		// The start's read serves the first step when fresh; the key's
		// settle reads once more.
		if r.d.ListReads != c.reads {
			t.Fatalf("start list %s old: %d reads, want %d", c.age, r.d.ListReads, c.reads)
		}
	}
}

func TestReusesWindowListWithoutAction(t *testing.T) {
	// A wait sends nothing: the key after it takes the same list, and only
	// the key's own settle reads again.
	r := newRig(t, policy.Config{}).notes()
	mustPass(t, r.run(waitFront("Notes"), protocol.Step{Key: "a"}))
	if r.d.ListReads != 2 {
		t.Fatalf("%d list reads", r.d.ListReads)
	}
	if r.lists.Reused == 0 {
		t.Fatal("no list was reused")
	}
	// The type step takes the tree its window's vetting read.
	r = newRig(t, policy.Config{}).notes()
	saveAs(r, false)
	ev := watch(r)
	play := panel(r, ev)
	trees := -1
	r.d.onDriver = func(tool string, args map[string]any) {
		if tool == "type_text" && trees < 0 {
			trees = r.d.trees
		}
		play(tool, args)
	}
	res := r.run(protocol.Step{Key: "cmd+shift+s"}, protocol.Step{Type: str("report.txt\n")})
	mustPass(t, res)
	if trees != 1 {
		t.Fatalf("%d tree reads before typing into the panel: %v", trees, ev.log)
	}
}

func TestRetryNoCapture(t *testing.T) {
	// The press waits for its button through retries; none of them takes a
	// screenshot, asks a model or observes for a report.
	r := newRig(t, policy.Config{}).notes()
	m := r.models()
	scr := newScreen(100, 100)
	r.e.Screen = scr
	ob := &fakeObserver{res: &protocol.ObserveResult{View: "v"}}
	r.e.Observer = ob
	sleeps := 0
	r.clk.onSleep = func() {
		if sleeps++; sleeps == 3 {
			r.d.Add("cg:1", "open", "button", "Open", fakedesk.Box(200, 100, 80, 20), "cg:1")
		}
	}
	res := r.run(protocol.Step{Key: "cmd+o"}, protocol.Step{Press: sel(`button "Open"`)})
	mustPass(t, res)
	if scr.shots != 0 || m.calls() != 0 || len(ob.reqs) != 0 {
		t.Fatalf("a retried step captured: %d shots, %d model calls, %d observes", scr.shots, m.calls(), len(ob.reqs))
	}
	if strings.Join(r.d.Performs, ",") != "open:AXPress" {
		t.Fatal(r.d.Performs)
	}
}

// saveAs puts TextEdit's "Save As" panel (cg:5) in front of Notes, its
// file name field past 1800 characters of text, its Save button disabled
// until a name is typed; hidden keeps it closed until opens.
func saveAs(r *rig, shown bool) {
	w := r.d.Win("cg:5", "TextEdit", "Save As", 100, [4]int{100, 100, 600, 400}, shown)
	w.Role = "dialog"
	r.d.Trees["cg:5"][0].Role = "dialog"
	for i := 0; i < 40; i++ {
		r.d.Add("cg:5", "tip"+itoa(i), "statictext", strings.Repeat("A long line of help text. ", 3), nil, "cg:5")
	}
	r.d.Add("cg:5", "label", "statictext", "Save As:", nil, "cg:5")
	r.d.Add("cg:5", "name", "textbox", "", fakedesk.Box(200, 150, 300, 20), "cg:5", "focused")
	r.d.Add("cg:5", "cancel", "button", "Cancel", fakedesk.Box(400, 450, 80, 20), "cg:5")
	r.d.Add("cg:5", "save5", "button", "Save", fakedesk.Box(500, 450, 80, 20), "cg:5", "disabled")
	if !shown {
		// Listed only once the shortcut opens it.
		r.d.Wins = r.d.Wins[:len(r.d.Wins)-1]
	} else {
		r.d.Window("cg:1").Active = false
	}
}

// panel plays the Save As panel's part for each driver call: a shortcut
// (the first hotkey) opens it, typing enables Save, a Return closes it
// and fronts Notes again.
func panel(r *rig, ev *events) func(tool string, args map[string]any) {
	closed := false
	return func(tool string, args map[string]any) {
		if ev != nil {
			ev.add("D:" + tool)
		}
		switch tool {
		case "hotkey":
			if r.d.Window("cg:5") == nil && !closed {
				r.d.Window("cg:1").Active = false
				r.d.Wins = append([]view.RawWindow{{Key: "cg:5", Handle: "cg:5", App: "TextEdit", Title: "Save As", PID: 100, Role: "dialog", Active: true, Bounds: &[4]int{100, 100, 600, 400}}}, r.d.Wins...)
			}
		case "type_text":
			r.d.Node("save5").States = nil
		case "press_key":
			if args["key"] == "return" && r.d.Window("cg:5") != nil {
				closed = true
				var keep []view.RawWindow
				for _, w := range r.d.Wins {
					if w.Key != "cg:5" {
						keep = append(keep, w)
					}
				}
				r.d.Wins = keep
				r.d.Window("cg:1").Active = true
			}
		}
	}
}
