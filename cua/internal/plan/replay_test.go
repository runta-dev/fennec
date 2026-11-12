package plan

import (
	"fmt"
	"strings"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/models"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

// noise fills a frame with a deterministic pattern.
func noise(w, h int, seed uint32) *vision.Frame {
	f := vision.NewFrame(w, h)
	x := seed
	for i := range f.Pix {
		x = x*1664525 + 1013904223
		f.Pix[i] = uint8(x >> 24)
	}
	return f
}

// paste copies p into f at (x, y).
func paste(f, p *vision.Frame, x, y int) {
	for j := 0; j < p.H; j++ {
		for i := 0; i < p.W; i++ {
			r, g, b := p.RGB(i, j)
			f.Set(x+i, y+j, r, g, b)
		}
	}
}

func b64(t *testing.T, f *vision.Frame) string {
	s, err := pngB64(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// cropRig has a 400x300 screen whose patch (40x30) sits at (200, 150), and
// Notes without a "Go" button in its tree.
func cropRig(t *testing.T) (*rig, *vision.Frame) {
	r := newRig(t, policy.Config{}).notes()
	scr := &fakeScreen{shot: Shot{Frame: vision.NewFrame(400, 300), Scale: 1}}
	patch := noise(40, 30, 7)
	paste(scr.shot.Frame, patch, 200, 150)
	r.e.Screen = scr
	r.d.Add("cg:1", "canvas", "image", "", fakedesk.Box(0, 0, 800, 600), "cg:1")
	return r, patch
}

func clickAt(d *desk) string {
	for _, c := range d.Calls {
		if strings.Contains(c.Tool, "click") {
			return c.Arg("x") + "," + c.Arg("y")
		}
	}
	return ""
}

func hitCanvas(r *rig, x, y int) {
	r.d.Hits[[2]int{x, y}] = fakedesk.Hit{Node: "canvas", Path: []string{"cg:1"}, Win: "cg:1"}
}

func goStep(rec *protocol.Recorded) protocol.Step {
	return protocol.Step{Click: sel(`"Go"`), Recorded: rec, Intent: "click Go"}
}

func TestRecordedCropFindsMovedTarget(t *testing.T) {
	r, patch := cropRig(t)
	hitCanvas(r, 220, 165)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", Crop: b64(t, patch), Scale: 1}))
	mustPass(t, res)
	if res.Steps[0].How != "crop" || clickAt(r.d) != "220,165" {
		t.Fatalf("%s %s", jsonOf(res.Steps[0]), r.d)
	}
}

func TestRecordedCropScale(t *testing.T) {
	r, patch := cropRig(t)
	hitCanvas(r, 220, 165)
	big := upscale(patch, 80, 60)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", Crop: b64(t, big), Scale: 2}))
	mustPass(t, res)
	if res.Steps[0].How != "crop" {
		t.Fatal(jsonOf(res.Steps[0]))
	}
}

func TestRecordedCropEdgeOffset(t *testing.T) {
	r, patch := cropRig(t)
	hitCanvas(r, 205, 165)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", Crop: b64(t, patch), Scale: 1, Offset: &[2]float64{-15, 0}}))
	mustPass(t, res)
	if clickAt(r.d) != "205,165" {
		t.Fatal(r.d)
	}
}

func TestRecordedCropFractionalOffset(t *testing.T) {
	r, patch := cropRig(t)
	hitCanvas(r, 221, 166)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", Crop: b64(t, patch), Scale: 1, Offset: &[2]float64{0.5, 0.5}}))
	mustPass(t, res)
	if !strings.Contains(res.Steps[0].Evidence, "220.5, 165.5") {
		t.Fatalf("the click is not at the exact point: %s", jsonOf(res.Steps[0]))
	}
}

// groundOn makes the ground model put any target at (300, 200), where the
// document root is, and the check pass.
func groundOn(r *rig) {
	r.d.Add("cg:1", "doc", "document", "", fakedesk.Box(0, 0, 800, 600), "cg:1")
	r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "doc", Path: []string{"cg:1"}, Win: "cg:1"}
	m := r.models()
	m.ground = func(models.GroundArgs) (*models.Grounded, error) {
		return &models.Grounded{X: 300, Y: 200, Served: protocol.Served{Role: "ground", Model: "g"}}, nil
	}
	m.decide = func(models.DecideArgs) (*models.Decided, error) { return noul(0.97), nil }
}

func TestChangedTargetSkipsCrop(t *testing.T) {
	r, patch := cropRig(t)
	hitCanvas(r, 220, 165)
	groundOn(r)
	scr := r.e.Screen.(*fakeScreen)
	step := protocol.Step{Click: sel(`"{{what}}"`), Recorded: &protocol.Recorded{Text: "Go", Crop: b64(t, patch), Scale: 1, At: &[2]float64{220, 165}, Size: &[2]float64{800, 600}}}
	res := r.act(protocol.ActRequest{Steps: []protocol.Step{step}, Params: map[string]string{"what": "Stop"}})
	mustPass(t, res)
	if res.Steps[0].How != "ground" || clickAt(r.d) != "300,200" || scr.shots != 1 {
		t.Fatalf("%s %s shots %d", jsonOf(res.Steps[0]), r.d, scr.shots)
	}
}

func TestCaseChangeSkipsCrop(t *testing.T) {
	r, patch := cropRig(t)
	hitCanvas(r, 220, 165)
	groundOn(r)
	res := r.run(protocol.Step{Click: sel(`"go"`), Recorded: &protocol.Recorded{Text: "Go", Crop: b64(t, patch), Scale: 1}})
	mustPass(t, res)
	if res.Steps[0].How != "ground" {
		t.Fatal(jsonOf(res.Steps[0]))
	}
}

func TestRecordedPointLast(t *testing.T) {
	r, _ := cropRig(t)
	hitCanvas(r, 50, 60)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", At: &[2]float64{50, 60}, Size: &[2]float64{801, 599}}))
	mustPass(t, res)
	if res.Steps[0].How != "point" || clickAt(r.d) != "50,60" {
		t.Fatalf("%s %s", jsonOf(res.Steps[0]), r.d)
	}
}

func TestRecordedPointWrongSizeStops(t *testing.T) {
	r, _ := cropRig(t)
	hitCanvas(r, 50, 60)
	other := noise(40, 30, 99)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", Crop: b64(t, other), Scale: 1, At: &[2]float64{50, 60}, Size: &[2]float64{1000, 700}}))
	mustStop(t, res, 0, protocol.ErrTargetNotFound)
	msg := res.Error.Message
	if !strings.Contains(msg, "crop: best match scored") || !strings.Contains(msg, "the window is 800x600, recorded at 1000x700") || clicks(r.d) != 0 {
		t.Fatalf("%+v", res.Error)
	}
	if res.Remaining[0].Recorded == nil || res.Remaining[0].Recorded.Crop != "" {
		t.Fatalf("remaining %s", jsonOf(res.Remaining))
	}
}

func TestRecordedClickFallbackOrder(t *testing.T) {
	r, _ := cropRig(t)
	hitCanvas(r, 50, 60)
	m := r.models()
	m.ground = func(models.GroundArgs) (*models.Grounded, error) {
		return &models.Grounded{X: 390, Y: 290}, nil // nothing there
	}
	other := noise(40, 30, 99)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", Crop: b64(t, other), Scale: 1, At: &[2]float64{50, 60}, Size: &[2]float64{800, 600}}))
	mustPass(t, res)
	if res.Steps[0].How != "point" || m.grounds != 1 || r.e.Screen.(*fakeScreen).shots < 2 {
		t.Fatalf("%s grounds %d", jsonOf(res.Steps[0]), m.grounds)
	}
}

func exportOpens(r *rig, active bool) {
	r.d.onPerform = func(id, _ string) {
		if id != "save" {
			return
		}
		if active {
			r.d.Window("cg:1").Active = false
		}
		r.d.Win("cg:5", "TextEdit", "Export", 100, [4]int{0, 0, 300, 200}, active)
	}
}

func TestAfterWindowMissingStops(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	res := r.run(protocol.Step{Press: sel(`button "Save"`), Expect: &protocol.Expect{Front: "Export"}, Intent: "open the export"}, protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	if !strings.Contains(res.Error.Message, `"Export"`) || !strings.Contains(res.Error.Message, `"Notes"`) || len(keyCalls(r.d)) != 0 {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestAfterWindowComes(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	exportOpens(r, true)
	open := r.d.onPerform
	r.d.onPerform = func(id, a string) {
		open(id, a)
		if id == "save" {
			r.d.Add("cg:5", "ok", "button", "OK", nil, "cg:5")
		}
	}
	res := r.run(protocol.Step{Press: sel(`button "Save"`), Expect: &protocol.Expect{Front: "Export"}}, protocol.Step{Press: sel(`button "OK"`)})
	mustPass(t, res)
	_ = view.Norm
}

func TestAfterWindowBehindNotFocused(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	exportOpens(r, false)
	res := r.run(protocol.Step{Press: sel(`button "Save"`), Expect: &protocol.Expect{Front: "Export", TimeoutMs: 1000}})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	if len(r.d.Raised) != 0 {
		t.Fatalf("the window behind was focused to pass: %v", r.d.Raised)
	}
}

// mailOpens makes the next key of the background Mail plan open windows.
func mailOpens(r *rig, ws ...view.RawWindow) {
	done := false
	r.d.onDriver = func(tool string, _ map[string]any) {
		if !done && (tool == "press_key" || tool == "hotkey") {
			done = true
			r.d.Wins = append(r.d.Wins, ws...)
		}
	}
}

func win(key, title, role, owner string) view.RawWindow {
	return view.RawWindow{Key: key, Handle: key, App: "Mail", PID: 200, Title: title, Role: role, OwnerKey: owner}
}

func TestRecordedAfterWindow(t *testing.T) {
	// The exact title wins over a longer one; the new window becomes the
	// target.
	r := behind(t, "background")
	mailOpens(r, win("cg:5", "Save As", "window", ""), win("cg:6", "Save", "window", ""))
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+s", Expect: &protocol.Expect{Front: "Save"}}, protocol.Step{Key: "tab"})
	mustPass(t, res)
	if k := keyCalls(r.d); k[1].Arg("window_id") != "6" {
		t.Fatal(r.d)
	}
	// Two windows that only contain the title give no target.
	r = behind(t, "background")
	mailOpens(r, win("cg:5", "Save A", "window", ""), win("cg:6", "Save B", "window", ""))
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Key: "cmd+s", Expect: &protocol.Expect{Front: "Save"}})
	mustStop(t, res, 1, protocol.ErrTargetAmbiguous)
	// An older window that already held the title is never taken.
	r = behind(t, "background")
	r.d.Win("cg:3", "Mail", "Save", 200, [4]int{0, 0, 10, 10}, false)
	res = r.run(protocol.Step{Launch: "Mail", Window: "Inbox"}, protocol.Step{Window: "Inbox", Key: "cmd+s", Expect: &protocol.Expect{Front: "Save", TimeoutMs: 500}})
	mustStop(t, res, 1, protocol.ErrExpectFailed)
	// A dialog another window owns is never taken.
	r = behind(t, "background")
	r.d.Win("cg:3", "Mail", "Drafts", 200, [4]int{0, 0, 10, 10}, false)
	mailOpens(r, win("cg:5", "Save", "dialog", "cg:3"))
	res = r.run(protocol.Step{Launch: "Mail", Window: "Inbox"}, protocol.Step{Window: "Inbox", Key: "cmd+s", Expect: &protocol.Expect{Front: "Save", TimeoutMs: 500}})
	mustStop(t, res, 1, protocol.ErrExpectFailed)
}

func TestRecordedAfterConfirms(t *testing.T) {
	// A newly opened window confirms an unconfirmed click.
	r := sendRig(t)
	r.d.onDriver = func(tool string, _ map[string]any) {
		if strings.Contains(tool, "click") {
			r.d.Wins = append(r.d.Wins, win("cg:5", "Sent", "window", ""))
		}
	}
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Send"`), Expect: &protocol.Expect{Front: "Sent"}})
	mustPass(t, res)
	if res.Steps[1].Status != protocol.StatusConfirmed {
		t.Fatal(jsonOf(res.Steps[1]))
	}
	// A matching window that was there before does not.
	r = sendRig(t)
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Send"`), Expect: &protocol.Expect{Front: "Inbox"}})
	mustStop(t, res, 1, protocol.ErrNotShown)
}

func TestStopReport(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	res := r.run(protocol.Step{Press: sel(`button "Save"`), Expect: &protocol.Expect{Front: "Export"}, Intent: "open the export"},
		protocol.Step{Click: sel(`"Go"`), Recorded: &protocol.Recorded{Text: "Go", Crop: "AAAA"}})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	if !strings.Contains(res.Error.Hint, "stop report: step 0 (open the export)") || len(res.Remaining) != 2 || res.Remaining[1].Recorded.Crop != "" {
		t.Fatalf("%+v %s", res.Error, jsonOf(res.Remaining))
	}
	// Another page stops a web step with a report.
	r = newRig(t, policy.Config{}).notes()
	r.d.Node("cg:1").Page = "https://example.com/login"
	res = r.run(protocol.Step{Press: sel(`button "Save"`), Web: &protocol.WebCheck{URL: "https://example.com/app"}})
	mustStop(t, res, 0, protocol.ErrPageMismatch)
	if !strings.Contains(res.Error.Hint, "stop report") {
		t.Fatal(res.Error)
	}
	// A stop for any other reason carries none.
	r = newRig(t, policy.Config{}).notes()
	res = r.run(protocol.Step{Press: sel(`button "Missing"`)})
	if res.OK || strings.Contains(res.Error.Hint, "stop report") {
		t.Fatal(res.Error)
	}
}

func TestStopRecord(t *testing.T) {
	r, _ := cropRig(t)
	res := r.run(goStep(&protocol.Recorded{Text: "Go", Crop: b64(t, noise(40, 30, 99)), Scale: 1}), protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 0, protocol.ErrTargetNotFound)
	if !strings.Contains(res.Error.Hint, "stop report: step 0 (click Go)") || len(res.Remaining) != 2 || res.Remaining[0].Recorded.Crop != "" {
		t.Fatalf("%+v", res.Error)
	}
}

func TestFingerprintMatchCount(t *testing.T) {
	rows := func(n int) *rig {
		r := newRig(t, policy.Config{}).notes()
		for i := 1; i <= n; i++ {
			r.d.Add("cg:1", fmt.Sprintf("d%d", i), "button", "Delete", fakedesk.Box(10, 30*i, 50, 20), "cg:1")
		}
		return r
	}
	r := rows(3)
	res := r.run(protocol.Step{Press: sel(`button "Delete"`), Recorded: &protocol.Recorded{Text: "Delete", Nth: 2, Of: 3}})
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "d2:AXPress" || res.Steps[0].How != "fingerprint" {
		t.Fatalf("%v %s", r.d.Performs, jsonOf(res.Steps[0]))
	}
	// The count changed: resolved afresh, never by position.
	r = rows(4)
	res = r.run(protocol.Step{Press: sel(`button "Delete"`), Recorded: &protocol.Recorded{Text: "Delete", Nth: 2, Of: 3}})
	mustStop(t, res, 0, protocol.ErrTargetAmbiguous)
	if len(r.d.Performs) != 0 {
		t.Fatal(r.d.Performs)
	}
	// Recorded on the second of two, one left: by name, not by position.
	r = rows(1)
	res = r.run(protocol.Step{Press: sel(`button "Delete"`), Recorded: &protocol.Recorded{Text: "Delete", Nth: 2, Of: 2}})
	mustPass(t, res)
	if res.Steps[0].How != "name" {
		t.Fatal(jsonOf(res.Steps[0]))
	}
}

func TestWebPageMatch(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Node("cg:1").Page = "https://Example.com/app/?tab=2#top"
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`), Web: &protocol.WebCheck{URL: "https://example.com/app"}}))
	r.d.Node("cg:1").Page = "https://example.com/login?next=/app"
	res := r.run(protocol.Step{Press: sel(`button "Save"`), Web: &protocol.WebCheck{URL: "https://example.com/app"}})
	mustStop(t, res, 0, protocol.ErrPageMismatch)
	if !strings.Contains(res.Error.Message, "https://example.com/login") || !strings.Contains(res.Error.Message, "https://example.com/app") || len(r.d.Performs) != 1 {
		t.Fatalf("%+v %v", res.Error, r.d.Performs)
	}
	// A page that shows no document is waited for, then fails.
	r.d.Node("cg:1").Page = ""
	start := r.clk.now()
	res = r.run(protocol.Step{Press: sel(`button "Save"`), Web: &protocol.WebCheck{URL: "https://example.com/app"}})
	mustStop(t, res, 0, protocol.ErrPageMismatch)
	if !strings.Contains(res.Error.Message, "no address") || r.clk.now().Sub(start) < DocWait {
		t.Fatal(res.Error)
	}
}

func TestWebDomID(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("row%d", i)
		r.d.Add("cg:1", id, "button", "Delete", fakedesk.Box(10, 10+i, 50, 1), "cg:1")
		r.d.Node(id).DOMID = id
	}
	r.d.Node("cg:1").Page = "https://example.com/list"
	res := r.run(protocol.Step{Press: sel(`button "Delete"`), Web: &protocol.WebCheck{URL: "https://example.com/list", DOMID: "row117"}})
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "row117:AXPress" || res.Steps[0].How != "dom_id" {
		t.Fatalf("%v %s", r.d.Performs, jsonOf(res.Steps[0]))
	}
}

func TestWebTreeSwitch(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Node("cg:1").Page = "https://example.com/"
	var held []int
	r.e.WebTree = func(_ ctxT, pid int) (bool, error) { held = append(held, pid); return false, nil }
	r.d.Chromiums[100] = true
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`), Web: &protocol.WebCheck{URL: "https://example.com"}}))
	if len(held) != 1 || held[0] != 100 {
		t.Fatal(held)
	}
	// Not a Chromium browser: no switch.
	held = nil
	r.d.Chromiums[100] = false
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`), Web: &protocol.WebCheck{URL: "https://example.com"}}))
	if len(held) != 0 {
		t.Fatal(held)
	}
}

func TestModifiedClickClicksCentre(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	for _, s := range []protocol.Step{
		{Click: sel(`button "Save"`), Button: "right"},
		{Click: sel(`button "Save"`), Count: 2},
		{Click: sel(`button "Save"`), Mods: []string{"cmd"}},
	} {
		r.d.Calls = nil
		mustPass(t, r.run(s))
		if clickAt(r.d) != "140,110" || len(r.d.Performs) != 0 {
			t.Fatalf("%s %v", r.d, r.d.Performs)
		}
	}
}
