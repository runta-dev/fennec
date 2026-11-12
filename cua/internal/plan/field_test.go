package plan

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/protocol"
)

func TestBackgroundFieldReadConcurrency(t *testing.T) {
	// Mail's window holds ten fields with no value in the tree: each is
	// read, never more than four at once.
	r := behind(t, "background")
	field(r, "")
	for i := 0; i < 10; i++ {
		r.d.Add("cg:2", "f"+itoa(i), "textbox", "Field "+itoa(i), fakedesk.Box(120, 120+30*i, 200, 20), "cg:2")
	}
	var mu sync.Mutex
	inFlight, most, reads := 0, 0, 0
	r.d.onRead = func(id string, full, tail bool) {
		if !strings.HasPrefix(id, "f") {
			return
		}
		mu.Lock()
		inFlight++
		reads++
		most = max(most, inFlight)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
	}
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("the app did not report the text")}
	r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("hello")})
	if reads != 20 || most > ReadsAtOnce || most < 2 {
		t.Fatalf("%d reads, at most %d at once", reads, most)
	}
	// Text that shows in one of them confirms the background typing.
	r = behind(t, "background")
	field(r, "")
	r.d.Add("cg:2", "subject", "textbox", "Subject", fakedesk.Box(120, 120, 200, 20), "cg:2")
	r.d.onDriver = func(tool string, _ map[string]any) {
		if tool == "type_text" {
			v := "hello"
			r.d.Node("subject").Value = &v
		}
	}
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("the app did not report the text")}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("hello")})
	mustPass(t, res)
	if res.Steps[1].Status != protocol.StatusConfirmed || !strings.Contains(res.Steps[1].Evidence, "shows in") {
		t.Fatalf("%s", jsonOf(res.Steps[1]))
	}
}

func TestTailReadForTruncatedField(t *testing.T) {
	long := strings.Repeat("output line\n", 30) + "$ "
	setup := func(input string) (*rig, *int) {
		r := behind(t, input)
		field(r, "")
		r.d.Add("cg:2", "term", "textarea", "Log", fakedesk.Box(120, 120, 600, 400), "cg:2")
		r.d.Node("term").Value = &long
		tails := 0
		r.d.onRead = func(id string, full, tail bool) {
			if id == "term" && full && tail {
				tails++
			}
		}
		return r, &tails
	}
	// Unconfirmed: the end is read once before and once after.
	r, tails := setup("background")
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("the app did not report the text")}
	r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("ls")})
	if *tails != 2 {
		t.Fatalf("%d tail reads", *tails)
	}
	// Confirmed by the driver: only the read before.
	r, tails = setup("background")
	mustPass(t, r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("ls")}))
	if *tails != 1 {
		t.Fatalf("%d tail reads", *tails)
	}
	// Under auto input a field read only in part never brings a retype in
	// the foreground.
	r, _ = setup("auto")
	r.d.cut = map[string]bool{"term": true}
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("the app did not report the text"), fakedesk.OK}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Type: str("ls")})
	mustStop(t, res, 1, protocol.ErrNotShown)
	if len(typeCalls(r.d)) != 1 || len(r.d.Raised) != 0 || !strings.Contains(res.Error.Message, "read only in part") {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestBackgroundClickImagePixels(t *testing.T) {
	r := behind(t, "background")
	r.d.Add("cg:2", "reply", "button", "Reply", fakedesk.Box(300, 200, 40, 20), "cg:2")
	// Another app's window with the same title, at another scale.
	r.d.Win("cg:7", "Notes", "Inbox", 700, [4]int{100, 100, 900, 700}, false)
	var shots []string
	r.e.WindowImage = func(ctx context.Context, pid int, key string) (int, int, error) {
		shots = append(shots, itoa(pid)+" "+key)
		if pid == 200 && key == "cg:2" {
			return 1800, 1400, nil // Retina: two pixels per desktop unit
		}
		return 900, 700, nil
	}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Reply"`), Button: "right"})
	mustPass(t, res)
	var click *fakedesk.Call
	for i := range r.d.Calls {
		if r.d.Calls[i].Tool == "click" {
			click = &r.d.Calls[i]
		}
	}
	// The centre (320, 210) is (220, 110) in the window, (440, 220) in its
	// image.
	if click == nil || click.Arg("x") != "440" || click.Arg("y") != "220" || click.Arg("window_id") != "2" || click.Arg("pid") != "200" || click.Arg("button") != "right" || click.Arg("delivery_mode") != "background" {
		t.Fatalf("%s", r.d)
	}
	if strings.Join(shots, ",") != "200 cg:2" || len(r.d.Raised) != 0 {
		t.Fatalf("captured %v, raised %v", shots, r.d.Raised)
	}
	// A plan window that cannot be captured gets no click.
	r = behind(t, "background")
	r.d.Add("cg:2", "reply", "button", "Reply", fakedesk.Box(300, 200, 40, 20), "cg:2")
	r.e.WindowImage = func(context.Context, int, string) (int, int, error) { return 0, 0, errors.New("no capture") }
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Reply"`), Count: 2})
	mustStop(t, res, 1, protocol.ErrUnavailable)
	for _, c := range r.d.Calls {
		if c.Tool == "click" {
			t.Fatalf("clicked: %s", r.d)
		}
	}
}

func TestBackgroundPixelToggleRefuses(t *testing.T) {
	r := behind(t, "background")
	menus(r, "cg:2")
	r.d.Add("cg:2", "view", "menubaritem", "View", nil, "bar")
	r.d.Add("cg:2", "viewmenu", "menu", "View", nil, "view")
	r.d.Add("cg:2", "preview", "menuitem", "Show Preview", nil, "viewmenu")
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Menu: "View > Show Preview", Value: raw("true")})
	mustStop(t, res, 1, protocol.ErrUnavailable)
	if len(r.d.Performs) != 0 || len(keyCalls(r.d)) != 0 || !strings.Contains(res.Error.Message, "screen") || !strings.Contains(res.Error.Message, "no menu was opened") {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}
