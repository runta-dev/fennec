package vision

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"fennec/cua/internal/protocol"
)

// fakeHost runs plans without a desktop.
type fakeHost struct {
	svc      *Service
	session  string
	front    *Front
	maxEdge  int
	frame    *Frame // what Shoot captures; a 2560x1600 UI by default
	reply    func(Request) (string, error)
	settled  *Capture
	shootErr error

	mu       sync.Mutex
	sent     []Request
	settles  int
	captured int
}

func (h *fakeHost) Front(context.Context) *Front { return h.front }

func (h *fakeHost) Send(_ context.Context, req Request) (string, error) {
	h.mu.Lock()
	h.sent = append(h.sent, req)
	h.mu.Unlock()
	if h.reply != nil {
		return h.reply(req)
	}
	return "done", nil
}

func (h *fakeHost) Settle(context.Context, PixelStep) *Capture {
	h.mu.Lock()
	h.settles++
	h.mu.Unlock()
	return h.settled
}

func (h *fakeHost) Shoot(_ context.Context, settled *Capture) (Screenshot, Shot, error) {
	if h.shootErr != nil {
		return Screenshot{}, Shot{}, h.shootErr
	}
	c := settled
	if c == nil {
		h.mu.Lock()
		h.captured++
		h.mu.Unlock()
		f := h.frame
		if f == nil {
			f = solid(2560, 1600, 50, 60, 70)
		}
		c = CaptureOf(f)
		c.ScreenW, c.ScreenH, c.Backing = float64(f.W)/2, float64(f.H)/2, 2
	}
	return h.svc.Adopt(h.session, "", c, h.maxEdge, false)
}

func (h *fakeHost) requests() []Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Request(nil), h.sent...)
}

func screenShot(scale float64) Shot {
	return Shot{Kind: KindScreen, Scale: scale, Screen: [2]float64{2560, 1600}, W: int(2560 / scale), H: int(1600 / scale)}
}

func xy(r Request) (float64, float64) {
	return r.Args["x"].(float64), r.Args["y"].(float64)
}

func TestPlanMapsByScreenshotBeforeIt(t *testing.T) {
	svc := NewService(&fakeCapturer{}, nil)
	svc.Sessions.Keep("s", screenShot(2.5), false)
	host := &fakeHost{svc: svc, session: "s", maxEdge: 512}
	steps := []PixelStep{
		{Action: "click", X: fp(100), Y: fp(40)},
		{Action: "double_click", X: fp(200), Y: fp(80)},
		{Action: "drag", X: fp(10), Y: fp(10), ToX: fp(20), ToY: fp(30)},
	}
	res := RunPlan(context.Background(), svc.Sessions, "s", host, steps, PlanOptions{})
	if res.Failed || res.Ran != 3 {
		t.Fatalf("%+v", res)
	}
	sent := host.requests()
	if x, y := xy(sent[0]); x != 250 || y != 100 {
		t.Fatalf("first step at %v,%v", x, y)
	}
	if x, y := xy(sent[1]); x != 500 || y != 200 {
		t.Fatalf("second step at %v,%v: mapped by the plan's own screenshot", x, y)
	}
	if d := sent[2].Args; d["from_x"] != 25.0 || d["to_y"] != 75.0 {
		t.Fatalf("drag %v", d)
	}
	// The plan's screenshot (max_edge 512) is what the next plan maps by.
	if res.Map.Scale != 5 {
		t.Fatalf("new scale %v", res.Map.Scale)
	}
	host2 := &fakeHost{svc: svc, session: "s"}
	RunPlan(context.Background(), svc.Sessions, "s", host2, steps[:1], PlanOptions{})
	if x, _ := xy(host2.requests()[0]); x != 500 {
		t.Fatalf("the next plan mapped x to %v, not by the newer screenshot", x)
	}
}

func TestPlanCapturesAfterLastStepOnly(t *testing.T) {
	svc := NewService(&fakeCapturer{}, nil)
	svc.Sessions.Keep("s", screenShot(1), false)
	host := &fakeHost{svc: svc, session: "s", reply: func(r Request) (string, error) {
		if r.Tool == "press_key" {
			return "", errors.New("press_key: the key is unknown")
		}
		return "ok", nil
	}}
	steps := []PixelStep{
		{Action: "click", X: fp(1), Y: fp(1)},
		{Action: "click", X: fp(2), Y: fp(2)},
		{Action: "key", Key: "nope"},
		{Action: "click", X: fp(3), Y: fp(3)},
		{Action: "click", X: fp(4), Y: fp(4)},
	}
	res := RunPlan(context.Background(), svc.Sessions, "s", host, steps, PlanOptions{})
	if !res.Failed || res.Ran != 2 || len(host.requests()) != 3 {
		t.Fatalf("%+v sent %d", res, len(host.requests()))
	}
	if !strings.Contains(res.Text(), "3. key failed: press_key: the key is unknown") || !strings.Contains(res.Text(), "the remaining 2 steps did not run") {
		t.Fatalf("%s", res.Text())
	}
	if host.settles != 2 || host.captured != 1 || res.Shot == nil {
		t.Fatalf("settles %d captures %d: intermediate steps settle, one screenshot after the last step that ran", host.settles, host.captured)
	}
}

func TestActionReportsRunWhenShotFails(t *testing.T) {
	svc := NewService(&fakeCapturer{}, nil)
	svc.Sessions.Keep("s", screenShot(1), false)
	host := &fakeHost{svc: svc, session: "s", shootErr: &CaptureError{Reason: "the display is asleep"}}
	for _, steps := range [][]PixelStep{
		{{Action: "click", X: fp(5), Y: fp(5)}},
		{{Action: "click", X: fp(5), Y: fp(5)}, {Action: "key", Key: "tab"}},
		{{Action: "wait", Seconds: 0.1}},
	} {
		res := RunPlan(context.Background(), svc.Sessions, "s", host, steps, PlanOptions{})
		if res.Failed || res.Error != nil || res.Shot != nil {
			t.Fatalf("a failed screenshot failed the act: %+v", res)
		}
		if !strings.Contains(res.Text(), ShotFailed+": the capture failed: the display is asleep") {
			t.Fatalf("%s", res.Text())
		}
		if !strings.Contains(res.Text(), fmt.Sprintf("%d. %s", len(steps), steps[len(steps)-1].Action)) {
			t.Fatalf("the step is not reported as done: %s", res.Text())
		}
	}
}

func TestWindowlessInputUsesDesktopScope(t *testing.T) {
	shot := screenShot(2)
	for _, step := range []PixelStep{
		{Action: "click", X: fp(10), Y: fp(20)},
		{Action: "right_click", X: fp(10), Y: fp(20)},
		{Action: "drag", X: fp(1), Y: fp(2), ToX: fp(3), ToY: fp(4)},
	} {
		req, err := Route(step, shot, &Front{PID: 7, ID: 9}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if req.Args["scope"] != "desktop" || req.Args["delivery_mode"] != protocol.InputForeground {
			t.Fatalf("%s: %v", step.Action, req.Args)
		}
		if _, ok := req.Args["pid"]; ok {
			t.Fatalf("%s carries a pid: %v", step.Action, req.Args)
		}
	}
	req, _ := Route(PixelStep{Action: "click", X: fp(10), Y: fp(20)}, shot, nil, nil)
	if x, y := xy(req); x != 20 || y != 40 {
		t.Fatalf("click at %v,%v", x, y)
	}
}

func TestFrontmostKeysGoToDesktop(t *testing.T) {
	for _, front := range []*Front{{PID: 7, ID: 9}, nil} {
		req, err := Route(PixelStep{Action: "key", Key: "cmd+s"}, screenShot(1), front, nil)
		if err != nil {
			t.Fatal(err)
		}
		if req.Tool != "press_key" || req.Args["scope"] != "desktop" || req.Args["pid"] != nil {
			t.Fatalf("%+v", req)
		}
	}
	req, err := Route(PixelStep{Action: "type", Text: "hi"}, screenShot(1), &Front{PID: 7, ID: 9}, nil)
	if err != nil || req.Tool != "type_text" || req.Args["scope"] != "desktop" || req.Args["pid"] != nil {
		t.Fatalf("%+v %v", req, err)
	}
	if _, err := Route(PixelStep{Action: "tap"}, screenShot(1), nil, nil); err == nil || !strings.Contains(err.Error(), `unknown action "tap"`) {
		t.Fatalf("unknown action: %v", err)
	}
}

func TestTypeWithNoWindowFails(t *testing.T) {
	svc := NewService(&fakeCapturer{}, nil)
	svc.Sessions.Keep("s", screenShot(1), false)
	host := &fakeHost{svc: svc, session: "s"}
	res := RunPlan(context.Background(), svc.Sessions, "s", host, []PixelStep{{Action: "type", Text: "secret"}}, PlanOptions{})
	if !res.Failed || !strings.Contains(res.Text(), NoTextTarget) || len(host.requests()) != 0 {
		t.Fatalf("%+v sent %d", res, len(host.requests()))
	}
}

func TestOffScreenPointRefused(t *testing.T) {
	shot := screenShot(2.5) // 1024x640 screenshot of a 2560x1600 screen
	_, err := Route(PixelStep{Action: "click", X: fp(1100), Y: fp(10)}, shot, nil, nil)
	var pe *protocol.Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "(1100,10) is off the screen, which is 1024x640 screenshot pixels (x 0 to 1023, y 0 to 639)") || !strings.Contains(pe.Hint, "new screenshot") {
		t.Fatalf("%v", err)
	}
	if _, err := Route(PixelStep{Action: "drag", X: fp(5), Y: fp(5), ToX: fp(5), ToY: fp(700)}, shot, nil, nil); err == nil {
		t.Fatal("a drag ending off the screen was accepted")
	}
	if _, err := Route(PixelStep{Action: "click", X: fp(-1), Y: fp(5)}, shot, nil, nil); err == nil {
		t.Fatal("a negative point was accepted")
	}
	// An element step skips the start check.
	mark := func(int) (Element, error) { return Element{N: 1, Ref: "e4", Center: &[2]float64{10, 10}}, nil }
	if _, err := Route(PixelStep{Action: "click", Element: 1, X: fp(5000), Y: fp(5000)}, shot, nil, mark); err != nil {
		t.Fatal(err)
	}
	svc := NewService(&fakeCapturer{}, nil)
	svc.Sessions.Keep("s", shot, false)
	host := &fakeHost{svc: svc, session: "s"}
	RunPlan(context.Background(), svc.Sessions, "s", host, []PixelStep{{Action: "click", X: fp(2000), Y: fp(1)}}, PlanOptions{})
	if len(host.requests()) != 0 {
		t.Fatal("input was sent for an off-screen point")
	}
}

func TestPinnedScaleMapsStepAndPlan(t *testing.T) {
	s := NewSessions()
	s.Keep("s", Shot{View: "v1", Kind: KindScreen, Scale: 2}, false)
	s.Keep("s", Shot{View: "v2", Kind: KindScreen, Scale: 4}, false)
	if shot, _ := s.Pin("s", "", 0); shot.Scale != 4 {
		t.Fatalf("latest %v", shot.Scale)
	}
	if shot, _ := s.Pin("s", "v1", 0); shot.Scale != 2 {
		t.Fatalf("a point pinned to v1 maps by %v", shot.Scale)
	}
	if shot, _ := s.Pin("s", "", 3); shot.Scale != 3 {
		t.Fatalf("an explicit scale gave %v", shot.Scale)
	}
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := s.Pin("s", "", bad); err == nil {
			t.Fatalf("scale %v accepted", bad)
		}
	}
	if _, err := s.Pin("s", "v99", 0); err == nil {
		t.Fatal("an unknown view was accepted")
	}
	svc := &Service{Capturer: &fakeCapturer{}, Sessions: s, Windows: &WindowScales{}}
	host := &fakeHost{svc: svc, session: "s"}
	res := RunPlan(context.Background(), s, "s", host, []PixelStep{{Action: "click", X: fp(10), Y: fp(10)}, {Action: "click", X: fp(20), Y: fp(20)}}, PlanOptions{Scale: 1.5})
	if res.Failed {
		t.Fatal(res.Text())
	}
	if x, _ := xy(host.requests()[1]); x != 30 {
		t.Fatalf("a plan scale mapped 20 to %v", x)
	}
	host = &fakeHost{svc: svc, session: "s"}
	res = RunPlan(context.Background(), s, "s", host, []PixelStep{{Action: "click", X: fp(1), Y: fp(1)}}, PlanOptions{Scale: -2})
	if !res.Failed || len(host.requests()) != 0 {
		t.Fatal("a negative scale was not rejected before sending")
	}
}

func TestConcurrentActsKeepOwnScale(t *testing.T) {
	svc := NewService(&fakeCapturer{}, nil)
	svc.Sessions.Keep("s", screenShot(1), false)
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := float64(i) / 4
			host := &fakeHost{svc: svc, session: "s", maxEdge: 100 * i, frame: solid(640, 400, 1, 2, 3)}
			steps := []PixelStep{{Action: "click", X: fp(40), Y: fp(40)}, {Action: "click", X: fp(80), Y: fp(80)}}
			RunPlan(context.Background(), svc.Sessions, "s", host, steps, PlanOptions{Scale: k})
			for j, r := range host.requests() {
				if x, _ := xy(r); x != math.Round(float64(40*(j+1))*k) {
					t.Errorf("act %d step %d at x %v with scale %v", i, j, x, k)
				}
			}
		}()
	}
	wg.Wait()
}

func TestSessionWithoutShotUsesLatestOfAny(t *testing.T) {
	fc := &fakeCapturer{desktop: func(int, string) (*Capture, error) {
		c := CaptureOf(solid(2560, 1600, 0, 0, 0))
		c.ScreenW, c.ScreenH, c.Backing = 1280, 800, 2
		return c, nil
	}}
	svc := NewService(fc, nil)
	svc.Sessions.Keep("a", screenShot(2), false)
	if shot, err := svc.Sessions.Pin("b", "", 0); err != nil || shot.Scale != 2 {
		t.Fatalf("b maps by %v %v", shot.Scale, err)
	}
	// A one-shot screenshot without a session becomes the latest.
	if _, _, _, err := svc.Screenshot(context.Background(), "", "", 512, false); err != nil {
		t.Fatal(err)
	}
	if shot, _ := svc.Sessions.Pin("b", "", 0); shot.Scale != 5 {
		t.Fatalf("b maps by %v, not by the one-shot screenshot", shot.Scale)
	}
	if shot, _ := svc.Sessions.Pin("a", "", 0); shot.Scale != 2 {
		t.Fatalf("a lost its own screenshot: %v", shot.Scale)
	}
	svc.Sessions.End("a")
	if shot, _ := svc.Sessions.Pin("a", "", 0); shot.Scale != 5 {
		t.Fatalf("an ended session maps by %v", shot.Scale)
	}
	if n := len(fc.ended); n != 1 || !strings.HasPrefix(fc.ended[0], "shot-") {
		t.Fatalf("the one-shot session was not ended: %v", fc.ended)
	}
}

func TestSharedShotLeavesOtherScalesAlone(t *testing.T) {
	s := NewSessions()
	s.Keep("a", Shot{Scale: 2}, false)
	s.Keep("b", Shot{Scale: 5}, true)
	if shot, _ := s.Pin("b", "", 0); shot.Scale != 5 {
		t.Fatalf("b %v", shot.Scale)
	}
	if shot, _ := s.Pin("c", "", 0); shot.Scale != 2 {
		t.Fatalf("a shared screenshot became the latest: %v", shot.Scale)
	}
}

func TestRegionScaleLatest(t *testing.T) {
	s := NewSessions()
	s.Keep("a", Shot{Kind: KindScreen, Scale: 2.5}, false)
	shot, err := s.Pin("", "", 0)
	if err != nil || shot.Scale != 2.5 {
		t.Fatalf("a click without a session maps by %v %v", shot.Scale, err)
	}
	if x, y := shot.ToDesktop(100, 40); x != 250 || y != 100 {
		t.Fatalf("%v,%v", x, y)
	}
	if _, err := NewSessions().Pin("", "", 0); err == nil {
		t.Fatal("a point with no screenshot at all was mapped")
	}
}

func TestCallersGetOwnDefaultSession(t *testing.T) {
	if got := DefaultSession("mcp-1/agent b"); got != "vision-mcp-1-agent-b" {
		t.Fatalf("%q", got)
	}
	if DefaultSession("é") != "vision--" {
		t.Fatalf("%q", DefaultSession("é"))
	}
	s := NewSessions()
	a, b := DefaultSession("one"), DefaultSession("two")
	if _, err := s.Mark(a, 1); err == nil || !strings.Contains(err.(*protocol.Error).Hint, "observe with vision") {
		t.Fatalf("acting before observing: %v", err)
	}
	s.SetMarks(a, []Element{{N: 1, Ref: "e1"}})
	s.Keep(a, Shot{Scale: 2}, false)
	if _, err := s.Mark(b, 1); err == nil {
		t.Fatal("one caller's numbers served another")
	}
	s.SetMarks(b, []Element{{N: 1, Ref: "e9"}})
	s.End(a)
	if _, err := s.Mark(a, 1); err == nil {
		t.Fatal("an ended session kept its numbers")
	}
	if e, err := s.Mark(b, 1); err != nil || e.Ref != "e9" {
		t.Fatalf("ending one session broke another: %v", err)
	}
}
