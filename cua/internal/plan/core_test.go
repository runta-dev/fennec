package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/flows"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

func TestValidateRejectsBeforeAnyInput(t *testing.T) {
	cases := []struct{ args, want string }{
		{`{"steps":[{"press":"button \"Save\""},{"press":"e1","key":"cmd+s"}]}`, "step 1: press and key"},
		{`{"steps":[{"press":"button \"Save\""},{"key":""}]}`, "step 1"},
		{`{"steps":[{"click":""}]}`, "step 0: click is empty"},
		{`{"steps":[{"menu":""}]}`, "step 0"},
		{`{"steps":[{"close":false}]}`, "step 0: close is false"},
		{`{"steps":[{"wait":{}}]}`, "step 0: wait names no condition"},
		{`{"steps":[{"expect":{"exists":""}}]}`, "step 0"},
		{`{"steps":[{"press":"button \"Save\""},{"type":"{{who}}"}]}`, "step 1: no value for {{who}}"},
		{`{"steps":[{"type":5}]}`, "step 0: type must be text, not a number"},
		{`{"steps":[{"press":"button \"Save\"","window":["a"]}]}`, "step 0: window must be text, not a list"},
		{`{"steps":[{"key":"cmd+s","behind":true}]}`, "step 0: behind marks a key step"},
	}
	for _, c := range cases {
		r := newRig(t, policy.Config{}).notes()
		res, perr := r.tool(c.args)
		if res != nil || perr == nil || perr.Code != protocol.ErrInvalid || !strings.Contains(perr.Message, c.want) {
			t.Fatalf("%s: got %+v %+v, want an invalid_argument naming %q", c.args, res, perr, c.want)
		}
		if r.d.ListReads != 0 || len(r.d.Calls) != 0 || len(r.d.Performs) != 0 || r.d.trees != 0 {
			t.Fatalf("%s: the desk was touched: %s", c.args, r.d)
		}
	}
	// The same check runs before a flow is saved from steps.
	if perr := Validate([]protocol.Step{{Key: "cmd+s"}, {Type: str("{{x}}")}}, nil); perr == nil || !strings.Contains(perr.Message, "step 1") {
		t.Fatalf("%+v", perr)
	}
}

func TestNullOptionalFieldIsAbsent(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	res, perr := r.tool(`{"steps":[{"press":"button \"Save\"","window":null,"intent":null}],"window":null}`)
	if perr != nil {
		t.Fatal(perr)
	}
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "save:AXPress" {
		t.Fatal(r.d)
	}
}

func TestStepValidation(t *testing.T) {
	for _, c := range []struct{ args, want string }{
		{`{"steps":[{"wait":2}]}`, "step 0: wait must be an object, not a number"},
		{`{"steps":[{"key":""}]}`, "step 0"},
		{`{"steps":[{"key":"a","type":"b"}]}`, "step 0: type and key"},
		{`{"steps":[{"type":"","into":{"point":{"x":1,"y":2}}}]}`, "step 0: type is empty"},
		{`{"steps":[{"focus":"Notes"},{"menu":7}]}`, "step 1: menu must be text"},
	} {
		r := newRig(t, policy.Config{}).notes()
		_, perr := r.tool(c.args)
		if perr == nil || !strings.Contains(perr.Message, c.want) {
			t.Fatalf("%s: %+v, want %q", c.args, perr, c.want)
		}
		if r.d.ListReads != 0 || len(r.d.Calls) != 0 {
			t.Fatalf("%s: the desk was touched", c.args)
		}
	}
	r := newRig(t, policy.Config{}).notes()
	if _, perr := r.tool(`{"steps":[{"press":"button \"Save\"","app":null,"pid":null}]}`); perr != nil {
		t.Fatal(perr)
	}
}

func TestSecretStepValidation(t *testing.T) {
	for _, c := range []struct{ args, want string }{
		{`{"steps":[{"type":"hunter2\nmore","secret":true}]}`, "step 0: secret needs one password line"},
		{`{"steps":[{"focus":"Notes"},{"type":"hunter2","secret":"yes"}]}`, "step 1: secret must be true or false"},
		{`{"steps":[{"key":"return","secret":true}]}`, "step 0: secret marks a key step"},
	} {
		r := newRig(t, policy.Config{}).notes()
		_, perr := r.tool(c.args)
		if perr == nil || perr.Code != protocol.ErrInvalid || !strings.Contains(perr.Message, c.want) {
			t.Fatalf("%s: %+v, want %q", c.args, perr, c.want)
		}
		if strings.Contains(perr.Message, "hunter2") || r.d.ListReads != 0 || len(r.d.Calls) != 0 {
			t.Fatalf("%s: %+v; %s", c.args, perr, r.d)
		}
	}
	r := newRig(t, policy.Config{}).notes()
	if _, perr := r.tool(`{"steps":[{"type":"hunter2\n","secret":true,"any_focus":true}]}`); perr != nil {
		t.Fatalf("one password line ending in a newline is fine: %+v", perr)
	}
}

// secretRig types a password into a password field of a login dialog.
func secretRig(t *testing.T) *rig {
	r := newRig(t, policy.Config{})
	r.d.Win("cg:1", "Login", "Sign In", 100, [4]int{0, 0, 400, 300}, true)
	r.d.Add("cg:1", "pw", "textbox", "Password", nil, "cg:1", "protected", "focused")
	pw := r.d.Node("pw")
	r.d.Focus[100] = []*view.RawNode{pw}
	return r
}

func TestSecretNeverLeaks(t *testing.T) {
	r := secretRig(t)
	dir := t.TempDir()
	store, err := flows.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.e.Chains = &flows.Chains{Store: store}
	res := r.act(protocol.ActRequest{Steps: []protocol.Step{
		{Launch: "Login"},
		{Type: str("{{pw}}"), Secret: true, Into: sel(`textbox "Password" protected`), Intent: "type hunter2 for the admin"},
		{Press: sel(`button "Missing"`), Intent: "then go on"},
	}, Params: map[string]string{"pw": "hunter2"}})
	if res.OK {
		t.Fatal("the plan should stop at the missing button")
	}
	out := jsonOf(res)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("the password leaked into the result: %s", out)
	}
	for _, f := range []string{flows.PendingFile, flows.RunLogFile} {
		data, _ := os.ReadFile(filepath.Join(dir, f))
		if strings.Contains(string(data), "hunter2") {
			t.Fatalf("the password leaked into %s: %s", f, data)
		}
	}
	// The type call did send it, once.
	n := 0
	for _, c := range r.d.Calls {
		if c.Tool == "type_text" && c.Arg("text") == "hunter2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("typed %d times: %s", n, r.d)
	}
}

func TestSecretStepRedaction(t *testing.T) {
	r := secretRig(t)
	// The focus is on a plain field: the step stops, and its error says
	// "the password" with no outline, intent or value.
	r.d.Node("pw").States = []string{"focused"}
	r.e.Observer = &fakeObserver{res: &protocol.ObserveResult{View: "v9", Outline: "w1 window \"Sign In\""}}
	res := r.run(protocol.Step{Type: str("s3cret-pw"), Secret: true, Into: sel(`textbox protected`), Intent: "the s3cret-pw goes here"})
	mustStop(t, res, 0, protocol.ErrFocusMismatch)
	out := jsonOf(res)
	if strings.Contains(out, "s3cret-pw") {
		t.Fatalf("leak: %s", out)
	}
	if res.Steps[0].Error != nil && res.Steps[0].Error.Observe != nil {
		t.Fatalf("a password step's error carries a view: %s", out)
	}
	if res.Observe != nil {
		t.Fatalf("a stop on a password step attached the window outline: %s", out)
	}
	if len(res.Remaining) != 1 || res.Remaining[0].Intent != "" || *res.Remaining[0].Type != "[secret]" {
		t.Fatalf("remaining %s", jsonOf(res.Remaining))
	}
}

func TestPasswordNeverInResult(t *testing.T) {
	r := secretRig(t)
	res := r.run(protocol.Step{Type: str("pa55word"), Secret: true, Into: sel(`textbox protected`)})
	mustPass(t, res)
	if out := jsonOf(res); strings.Contains(out, "pa55word") {
		t.Fatalf("leak: %s", out)
	}
	// It was never read back from the field: no full read of the field.
	if strings.Contains(r.d.String(), "Read") {
		t.Fatal(r.d)
	}
}

func TestPlanTimeoutReportsDone(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.clk.real = 5 * time.Millisecond
	start := time.Now()
	res := r.act(protocol.ActRequest{TimeoutS: 1, Steps: []protocol.Step{
		{Press: sel(`button "Save"`)},
		{Wait: &protocol.WaitRequest{Expect: protocol.Expect{WindowOpens: "Never", TimeoutMs: 60000}}},
		{Key: "cmd+s"},
	}})
	if time.Since(start) > 5*time.Second {
		t.Fatal("the plan ran past its timeout")
	}
	mustStop(t, res, 1, protocol.ErrTimeout)
	if !strings.Contains(res.Error.Message, "1 s timeout after 1 of 3 steps") || !res.Steps[0].OK {
		t.Fatalf("%+v %s", res.Error, jsonOf(res.Steps))
	}
	if len(res.Remaining) != 2 {
		t.Fatalf("remaining %s", jsonOf(res.Remaining))
	}
	// A timeout above the cap is held to MaxTimeout.
	if MaxTimeout != 110*time.Second || DefaultTimeout != 90*time.Second {
		t.Fatal("timeouts")
	}
}

func TestPlanNoHiddenEnv(t *testing.T) {
	t.Setenv("FENNEC_SECRET_TOKEN", "from-the-environment")
	t.Setenv("OPENROUTER_API_KEY", "sk-should-never-show")
	r := newRig(t, policy.Config{}).notes()
	// A placeholder is filled only from params, never from the environment.
	_, perr := r.tool(`{"steps":[{"type":"{{FENNEC_SECRET_TOKEN}}"}]}`)
	if perr == nil || !strings.Contains(perr.Message, "no value for {{FENNEC_SECRET_TOKEN}}") {
		t.Fatalf("%+v", perr)
	}
	res := r.run(protocol.Step{Press: sel(`button "Save"`)}, protocol.Step{Type: str("$OPENROUTER_API_KEY"), AnyFocus: true})
	mustPass(t, res)
	for _, c := range r.d.Calls {
		if strings.Contains(jsonOf(c.Args), "sk-should-never-show") || strings.Contains(jsonOf(c.Args), "from-the-environment") {
			t.Fatalf("the environment reached the desk: %s", r.d)
		}
	}
	if !strings.Contains(r.d.String(), `"text":"$OPENROUTER_API_KEY"`) {
		t.Fatalf("the text is typed literally: %s", r.d)
	}
}

func TestCancelKeepsFinishedSteps(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	ctx, cancel := context.WithCancel(context.Background())
	sleeps := 0
	r.clk.onSleep = func() {
		sleeps++
		if sleeps == 3 {
			cancel()
		}
	}
	res, perr := r.e.Run(ctx, &engine.Call{}, protocol.ActRequest{Steps: []protocol.Step{
		{Press: sel(`button "Save"`)},
		{Wait: &protocol.WaitRequest{Expect: protocol.Expect{WindowOpens: "Never"}}},
		{Key: "cmd+s"},
	}}, engine.PlanOptions{})
	if perr != nil {
		t.Fatal(perr)
	}
	mustStop(t, res, 1, protocol.ErrStopped)
	if len(res.Steps) < 1 || !res.Steps[0].OK || !strings.Contains(res.Error.Message, "after 1 of 3 steps") {
		t.Fatalf("%+v %s", res.Error, jsonOf(res.Steps))
	}
	for _, c := range r.d.Calls {
		if c.Tool == "press_key" {
			t.Fatalf("a step after the cancel ran: %s", r.d)
		}
	}
}

func TestInterruptedPlanReport(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	ctx, cancel := context.WithCancel(context.Background())
	r.clk.onSleep = func() { cancel() }
	res, _ := r.e.Run(ctx, &engine.Call{}, protocol.ActRequest{Steps: []protocol.Step{
		{Press: sel(`button "Save"`)},
		{Wait: &protocol.WaitRequest{Expect: protocol.Expect{Front: "Later"}}},
		{Click: sel(`button "Go"`), Recorded: &protocol.Recorded{Text: "Go", Crop: "iVBORw0KGgo=", Offset: &[2]float64{1, 2}, Scale: 2}},
	}}, engine.PlanOptions{})
	if res.OK || res.StoppedAt == nil || *res.StoppedAt != 1 || len(res.Steps) == 0 || !res.Steps[0].OK {
		t.Fatalf("%s", jsonOf(res))
	}
	if len(res.Remaining) != 2 || res.Remaining[1].Recorded == nil || res.Remaining[1].Recorded.Crop != "" || res.Remaining[1].Recorded.Offset != nil {
		t.Fatalf("remaining keeps recording payloads: %s", jsonOf(res.Remaining))
	}
}

func TestRunLogRotatesAt1MiB(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	dir := t.TempDir()
	store, err := flows.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.e.Store = store
	big := strings.Repeat("x", flows.RunLogMax+10)
	if err := os.WriteFile(filepath.Join(dir, flows.RunLogFile), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	mustPass(t, r.run(protocol.Step{Press: sel(`button "Save"`)}))
	fi, err := os.Stat(filepath.Join(dir, flows.RunLogFile))
	if err != nil || fi.Size() > 4096 {
		t.Fatalf("the run log did not start over: %v %v", fi, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, flows.RunLogFile))
	if !strings.Contains(string(data), `"ok":true`) {
		t.Fatalf("%s", data)
	}
}
