package plan

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/models"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
)

// groundRig has an unnamed icon button at (290, 190, w, h) in Notes that
// the ground model puts at (300, 200).
func groundRig(t *testing.T, w, h int, probs ...float64) *rig {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "icon", "button", "", fakedesk.Box(290, 190, w, h), "cg:1")
	r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "icon", Path: []string{"cg:1"}, Win: "cg:1"}
	r.e.Screen, r.e.VisionModel = newScreen(1000, 800), true
	m := r.models()
	m.ground = func(models.GroundArgs) (*models.Grounded, error) {
		return &models.Grounded{X: 300, Y: 200, Served: protocol.Served{Role: "ground", Model: "fake-ground"}}, nil
	}
	m.decide = func(a models.DecideArgs) (*models.Decided, error) {
		if strings.Contains(string(a.Questions), `"pick"`) {
			return choice(map[string]float64{"none": 1}), nil
		}
		k := len(m.decides) - 1
		for _, d := range m.decides[:k] {
			if strings.Contains(string(d.Questions), `"pick"`) {
				k--
			}
		}
		if k < len(probs) {
			return noul(probs[k]), nil
		}
		return noul(0), nil
	}
	return r
}

func checks(m *fakeModels) int {
	n := 0
	for _, d := range m.decides {
		if strings.Contains(string(d.Questions), `"hit"`) {
			n++
		}
	}
	return n
}

func clicks(d *desk) int {
	n := len(d.HitCalls)
	for _, c := range d.Calls {
		if strings.Contains(c.Tool, "click") {
			n++
		}
	}
	return n
}

func groundClick(r *rig) *protocol.ActResult {
	return r.run(protocol.Step{Click: sel(`button "Gear"`)})
}

func TestGroundedCheckLadder(t *testing.T) {
	// The crop alone.
	r := groundRig(t, 20, 20, 0.95)
	res := groundClick(r)
	mustPass(t, res)
	if checks(r.m) != 1 || clicks(r.d) != 1 || res.Steps[0].How != "ground" {
		t.Fatalf("%d checks %s %s", checks(r.m), jsonOf(res.Steps[0]), r.d)
	}
	// A small element: the zoom.
	r = groundRig(t, 20, 20, 0.5, 0.7)
	mustPass(t, groundClick(r))
	if checks(r.m) != 2 || clicks(r.d) != 1 {
		t.Fatalf("%d %s", checks(r.m), r.d)
	}
	// A large element skips the zoom; the ringed screen decides.
	r = groundRig(t, 200, 100, 0.5, 0.96)
	mustPass(t, groundClick(r))
	if checks(r.m) != 2 {
		t.Fatal(checks(r.m))
	}
	// The ring stage turned off: refused, nothing clicked.
	r = groundRig(t, 200, 100, 0.5, 0.99)
	r.e.NoRingCheck = true
	res = groundClick(r)
	mustStop(t, res, 0, protocol.ErrHitMismatch)
	if clicks(r.d) != 0 || checks(r.m) != 1 {
		t.Fatal(r.d)
	}
	// Without a vision model the text check decides.
	r = groundRig(t, 20, 20, 0.75)
	r.e.VisionModel = false
	mustPass(t, groundClick(r))
	if r.m.decides[len(r.m.decides)-1].Image {
		t.Fatal("the text check sent an image")
	}
	r = groundRig(t, 20, 20, 0.6)
	r.e.VisionModel = false
	mustStop(t, groundClick(r), 0, protocol.ErrHitMismatch)
	if clicks(r.d) != 0 {
		t.Fatal(r.d)
	}
}

func TestGroundThresholds(t *testing.T) {
	for _, c := range []struct {
		w     int
		probs []float64
		pass  bool
	}{
		{20, []float64{0.9}, true},
		{20, []float64{0.89, 0.65}, true},
		{20, []float64{0.89, 0.64, 0.95}, true},
		{20, []float64{0.89, 0.64, 0.949}, false},
		{200, []float64{0.89, 0.95}, true},
	} {
		r := groundRig(t, c.w, 40, c.probs...)
		res := groundClick(r)
		if res.OK != c.pass || (clicks(r.d) == 1) != c.pass {
			t.Fatalf("%v: %s", c, jsonOf(res))
		}
	}
	if CropBar != 0.9 || ZoomBar != 0.65 || RingBar != 0.95 || TextBar != 0.7 {
		t.Fatal("bars")
	}
}

func TestGroundedCheckTrace(t *testing.T) {
	r := groundRig(t, 20, 20, 0.5, 0.7)
	res := groundClick(r)
	mustPass(t, res)
	trace := strings.Join(res.Steps[0].Notes, "\n")
	if !strings.Contains(trace, "crop 0.50, zoom 0.70") || !strings.Contains(trace, "element button") {
		t.Fatalf("%q", trace)
	}
	if len(res.Steps[0].Served) < 3 {
		t.Fatalf("served %v", res.Steps[0].Served)
	}
}

func TestGroundedPointRefusals(t *testing.T) {
	cases := map[string]func(r *rig){
		"no_element": func(r *rig) { delete(r.d.Hits, [2]int{300, 200}) },
		"root":       func(r *rig) { r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "cg:1", Win: "cg:1"} },
		"other_process": func(r *rig) {
			r.d.Win("cg:8", "Dock", "Dock", 800, [4]int{0, 0, 10, 10}, false)
			r.d.Add("cg:8", "tile", "button", "Tile", nil, "cg:8")
			r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "tile", Path: []string{"cg:8"}, Win: "cg:8"}
		},
		"other_window": func(r *rig) {
			r.d.Win("cg:3", "TextEdit", "Other", 100, [4]int{0, 0, 10, 10}, false)
			r.d.Add("cg:3", "b3", "button", "B", nil, "cg:3")
			r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "b3", Path: []string{"cg:3"}, Win: "cg:3"}
		},
		"covered": func(r *rig) {
			r.d.Win("cg:8", "Alert", "Alert", 800, [4]int{0, 0, 10, 10}, false)
			r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "icon", Path: []string{"cg:1"}, Win: "cg:1", Covered: "cg:8"}
		},
		"read_failed": func(r *rig) { r.d.HitErr = errorString("AX timed out") },
	}
	for code, setup := range cases {
		r := groundRig(t, 20, 20, 0.99)
		setup(r)
		res := groundClick(r)
		mustStop(t, res, 0, protocol.ErrHitMismatch)
		if !strings.Contains(strings.Join(res.Steps[0].Notes, " "), "ground check: "+code) || checks(r.m) != 0 || clicks(r.d) != 0 {
			t.Fatalf("%s: %s %s", code, jsonOf(res.Steps[0]), r.d)
		}
		if code == "covered" && !strings.Contains(res.Error.Message, `"Alert"`) {
			t.Fatalf("the covering window is not named: %+v", res.Error)
		}
		if code == "read_failed" && !strings.Contains(res.Error.Message, "AX timed out") {
			t.Fatalf("the read error is not reported: %+v", res.Error)
		}
	}
	// Out-of-process content hosted in the plan window counts as its own.
	r := groundRig(t, 20, 20, 0.99)
	r.d.Win("cg:8", "WebContent", "", 800, [4]int{0, 0, 10, 10}, false)
	r.d.Add("cg:8", "web", "button", "W", nil, "cg:8")
	r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "web", Path: []string{"cg:8", "cg:1"}, Win: "cg:8"}
	mustPass(t, groundClick(r))
}

func TestGroundRefusesRoot(t *testing.T) {
	r := groundRig(t, 20, 20, 0.99)
	r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "cg:1", Win: "cg:1"}
	res := groundClick(r)
	mustStop(t, res, 0, protocol.ErrHitMismatch)
	if checks(r.m) != 0 || clicks(r.d) != 0 {
		t.Fatal("a root was checked or clicked")
	}
}

func TestUnrecordedRefusesRootGround(t *testing.T) {
	r := groundRig(t, 20, 20, 0.99)
	r.d.Add("cg:1", "doc", "document", "", fakedesk.Box(0, 0, 800, 600), "cg:1")
	r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "doc", Path: []string{"cg:1"}, Win: "cg:1"}
	res := groundClick(r)
	mustStop(t, res, 0, protocol.ErrHitMismatch)
	if clicks(r.d) != 0 || !strings.Contains(res.Error.Message, "root") {
		t.Fatalf("%+v", res.Error)
	}
}

func recheckRig(t *testing.T) *rig {
	r := groundRig(t, 20, 20)
	r.d.Win("cg:8", "Alert", "Alert", 800, [4]int{0, 0, 10, 10}, false)
	m := r.m
	m.decide = func(a models.DecideArgs) (*models.Decided, error) {
		if strings.Contains(string(a.Questions), `"pick"`) {
			return choice(map[string]float64{"none": 1}), nil
		}
		// A window shows over the point while the check runs.
		r.d.Hits[[2]int{300, 200}] = fakedesk.Hit{Node: "icon", Path: []string{"cg:1"}, Win: "cg:1", Covered: "cg:8"}
		return noul(0.99), nil
	}
	return r
}

func TestGroundedPointRecheckedBeforeClick(t *testing.T) {
	r := recheckRig(t)
	res := groundClick(r)
	mustStop(t, res, 0, protocol.ErrHitMismatch)
	if clicks(r.d) != 0 || !strings.Contains(res.Error.Message, "right before the click") {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
}

func TestGroundedPointRecheck(t *testing.T) {
	r := recheckRig(t)
	mustStop(t, groundClick(r), 0, protocol.ErrHitMismatch)
	if clicks(r.d) != 0 {
		t.Fatal(r.d)
	}
}

func TestCropNegativeOrigin(t *testing.T) {
	f := newScreen(3000, 1000).shot.Frame
	shot := &Shot{Frame: f, Origin: [2]float64{-1440, 0}, Scale: 1}
	// A point on the display left of the main one crops around itself,
	// not at zero.
	crop, at := cropAround(shot, -1000, 300, 640, 400)
	if crop.W != 640 || crop.H != 400 || at != [2]int{320, 200} {
		t.Fatalf("%dx%d at %v", crop.W, crop.H, at)
	}
	// At the virtual screen's left edge the crop is narrowed by the clamp,
	// and the point keeps its pixel inside it.
	crop, at = cropAround(shot, -1340, 300, 640, 400)
	if crop.W != 640-220 || at != [2]int{100, 200} {
		t.Fatalf("%dx%d at %v", crop.W, crop.H, at)
	}
	// A retina capture: two pixels per desktop unit.
	shot.Scale = 0.5
	crop, at = cropAround(shot, -1000, 300, 640, 400)
	if crop.W != 1280 || at != [2]int{640, 400} {
		t.Fatalf("%dx%d at %v", crop.W, crop.H, at)
	}
}

// ---------------------------------------------------------------------------
// Model picks

// refOf finds the ref a pick question gives an element whose description
// holds name.
func refOf(t *testing.T, a models.DecideArgs, name string) string {
	var q map[string]struct {
		Criteria map[string]string `json:"criteria"`
	}
	if err := json.Unmarshal(a.Questions, &q); err != nil {
		t.Fatal(err)
	}
	for ref, desc := range q["pick"].Criteria {
		if strings.Contains(desc, name) {
			return ref
		}
	}
	t.Fatalf("no candidate %q in %s", name, a.Questions)
	return ""
}

func pickRig(t *testing.T, pick func(a models.DecideArgs) map[string]float64) *rig {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "send", "button", "Send message", fakedesk.Box(10, 10, 50, 20), "cg:1")
	r.d.Add("cg:1", "cancel", "button", "Cancel", fakedesk.Box(70, 10, 50, 20), "cg:1")
	r.d.Add("cg:1", "tab", "tab", "Send log", fakedesk.Box(130, 10, 50, 20), "cg:1")
	r.d.Add("cg:1", "spin", "spinbutton", "plus value", fakedesk.Box(190, 10, 50, 20), "cg:1")
	m := r.models()
	m.decide = func(a models.DecideArgs) (*models.Decided, error) { return choice(pick(a)), nil }
	return r
}

func TestModelPickMargin(t *testing.T) {
	r := pickRig(t, func(a models.DecideArgs) map[string]float64 {
		return map[string]float64{refOf(t, a, "Send message"): 0.9, refOf(t, a, "Cancel"): 0.1}
	})
	res := r.run(protocol.Step{Press: sel(`"Send it"`)})
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "send:AXPress" || res.Steps[0].How != "model" {
		t.Fatalf("%v %s", r.d.Performs, jsonOf(res.Steps[0]))
	}
	// A runner-up within the lead: ambiguous, nothing pressed.
	r = pickRig(t, func(a models.DecideArgs) map[string]float64 {
		return map[string]float64{refOf(t, a, "Send message"): 0.75, refOf(t, a, "Cancel"): 0.2}
	})
	res = r.run(protocol.Step{Press: sel(`"Send it"`)})
	mustStop(t, res, 0, protocol.ErrTargetAmbiguous)
	if len(r.d.Performs) != 0 || !strings.Contains(res.Error.Message, "Send message") || !strings.Contains(res.Error.Hint, "more exact name") {
		t.Fatalf("%+v %v", res.Error, r.d.Performs)
	}
	// Under the bar: not found.
	r = pickRig(t, func(a models.DecideArgs) map[string]float64 {
		return map[string]float64{refOf(t, a, "Send message"): 0.65}
	})
	mustStop(t, r.run(protocol.Step{Press: sel(`"Send it"`)}), 0, protocol.ErrTargetNotFound)
	if len(r.d.Performs) != 0 || PickBar != 0.7 || PickLead != 0.6 {
		t.Fatal(r.d.Performs)
	}
}

func TestModelPickRejectsMismatch(t *testing.T) {
	for _, c := range []struct{ target, picked string }{
		{`"Send it"`, "Cancel"},                 // no shared word
		{`"Send it"`, "Send log"},               // a tab the target does not call a tab
		{`button "plus"`, "plus value"},         // a spin button is not the plus button
		{`link "Send message"`, "Send message"}, // a target that names a link takes only a link
	} {
		r := pickRig(t, func(a models.DecideArgs) map[string]float64 {
			return map[string]float64{refOf(t, a, c.picked): 0.97}
		})
		res := r.run(protocol.Step{Press: sel(c.target)})
		if res.OK || len(r.d.Performs) != 0 {
			t.Fatalf("%s took %s: %s", c.target, c.picked, jsonOf(res.Steps))
		}
	}
}

func TestScreenshotPickFirst(t *testing.T) {
	var images, texts int
	r := pickRig(t, func(a models.DecideArgs) map[string]float64 {
		if a.Image {
			images++
		} else {
			texts++
		}
		return map[string]float64{refOf(t, a, "Send message"): 0.95}
	})
	r.e.VisionModel, r.e.Screen = true, newScreen(400, 300)
	mustPass(t, r.run(protocol.Step{Press: sel(`"the one in the first row"`)}))
	if images != 1 || texts != 0 || strings.Join(r.d.Performs, ",") != "send:AXPress" {
		t.Fatalf("%d image, %d text picks; %v", images, texts, r.d.Performs)
	}
	// A failed screenshot pick falls back to the text pick.
	images, texts = 0, 0
	r = pickRig(t, func(a models.DecideArgs) map[string]float64 {
		if a.Image {
			images++
			return map[string]float64{"none": 0.9}
		}
		texts++
		return map[string]float64{refOf(t, a, "Send message"): 0.95}
	})
	r.e.VisionModel, r.e.Screen = true, newScreen(400, 300)
	mustPass(t, r.run(protocol.Step{Press: sel(`"Send it"`)}))
	if images != 1 || texts != 1 {
		t.Fatalf("%d image, %d text", images, texts)
	}
}

func TestServedModelCallsReported(t *testing.T) {
	r := pickRig(t, func(a models.DecideArgs) map[string]float64 {
		return map[string]float64{refOf(t, a, "Send message"): 0.95}
	})
	res := r.run(protocol.Step{Press: sel(`button "Cancel"`)}, protocol.Step{Press: sel(`"Send it"`)})
	mustPass(t, res)
	if len(res.Steps[0].Served) != 0 || len(res.Steps[1].Served) != 1 || res.Steps[1].Served[0].Model != "fake-decide" {
		t.Fatalf("%s", jsonOf(res.Steps))
	}
	// A pick that found nothing still reports its call.
	r = pickRig(t, func(a models.DecideArgs) map[string]float64 { return map[string]float64{"none": 1} })
	res = r.run(protocol.Step{Press: sel(`"Send it"`)})
	if res.OK || len(res.Steps[0].Served) != 1 {
		t.Fatalf("%s", jsonOf(res.Steps))
	}
}

func TestShowingWithoutFramePressedWithoutModel(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "hidden", "button", "Bold", fakedesk.Box(0, 0, 10, 10), "cg:1", "hidden")
	r.d.Add("cg:1", "bold", "button", "Bold", nil, "cg:1")
	m := r.models()
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Bold"`)}))
	if strings.Join(r.d.Performs, ",") != "bold:AXPress" || m.calls() != 0 {
		t.Fatalf("%v %d", r.d.Performs, m.calls())
	}
}

// ---------------------------------------------------------------------------
// Credits

// creditsServer answers every model request 402 and counts them.
func creditsServer(t *testing.T) (*models.Client, *int64) {
	var n int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&n, 1)
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"message":"Insufficient credits"}}`))
	}))
	t.Cleanup(srv.Close)
	cfg := models.Defaults()
	cfg.BaseURL, cfg.DecisionsURL, cfg.APIKey = srv.URL, srv.URL+"/decisions", models.Secret("test-key")
	c, err := models.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, &n
}

func TestCreditsStopPlan(t *testing.T) {
	// Grounding a click: no other window element to pick from.
	r := newRig(t, policy.Config{})
	r.d.Win("cg:1", "TextEdit", "Notes", 100, [4]int{0, 0, 800, 600}, true)
	c, n := creditsServer(t)
	r.e.Models, r.e.Screen = c, newScreen(100, 100)
	res := r.run(protocol.Step{Click: sel(`button "Gear"`)}, protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 0, protocol.ErrCreditsExhausted)
	if *n != 1 || clicks(r.d) != 0 || len(r.d.Calls) != 0 {
		t.Fatalf("%d requests; %s", *n, r.d)
	}
	// The check of a grounded point: no fallback call, no click.
	r = groundRig(t, 20, 20)
	r.m.decide = func(a models.DecideArgs) (*models.Decided, error) {
		if strings.Contains(string(a.Questions), `"pick"`) {
			return choice(map[string]float64{"none": 1}), nil
		}
		return nil, &models.Error{Code: protocol.ErrCreditsExhausted, Message: models.CreditsUsedUp + " fake"}
	}
	res = groundClick(r)
	mustStop(t, res, 0, protocol.ErrCreditsExhausted)
	if checks(r.m) != 1 || clicks(r.d) != 0 {
		t.Fatalf("%d checks %s", checks(r.m), r.d)
	}
	// The screenshot pick: no text pick after it.
	r = pickRig(t, nil)
	r.m.decide = func(models.DecideArgs) (*models.Decided, error) {
		return nil, &models.Error{Code: protocol.ErrCreditsExhausted, Message: models.CreditsUsedUp + " fake"}
	}
	r.e.VisionModel, r.e.Screen = true, newScreen(100, 100)
	res = r.run(protocol.Step{Press: sel(`"Send it"`)})
	mustStop(t, res, 0, protocol.ErrCreditsExhausted)
	if len(r.m.decides) != 1 || len(r.d.Performs) != 0 {
		t.Fatalf("%d decides", len(r.m.decides))
	}
}

func Test402StopsPlan(t *testing.T) {
	r := pickRig(t, nil)
	c, n := creditsServer(t)
	r.e.Models = c
	res := r.run(protocol.Step{Press: sel(`"Send it"`)}, protocol.Step{Key: "cmd+s"})
	mustStop(t, res, 0, protocol.ErrCreditsExhausted)
	if !strings.HasPrefix(res.Error.Message, models.CreditsUsedUp) || *n != 1 || len(r.d.Performs) != 0 || len(r.d.Calls) != 0 {
		t.Fatalf("%+v %d %s", res.Error, *n, r.d)
	}
	if res.Usage != nil && len(res.Usage) > 0 {
		for _, u := range res.Usage {
			if u.Calls > 1 {
				t.Fatalf("usage %v", res.Usage)
			}
		}
	}
}
