package actions_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/driver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// focusOn makes pid's focus the Name field holding value.
func focusOn(d *fakedesk.Desk, pid int, values ...string) {
	for _, v := range values {
		n := *d.Node("name")
		v := v
		n.Value = &v
		d.Focus[pid] = append(d.Focus[pid], &n)
	}
}

func typed(d *fakedesk.Desk) []string {
	var out []string
	for _, c := range calls(d, "type_text") {
		out = append(out, c.Arg("text"))
	}
	return out
}

func failedWith(tool, msg string, result map[string]any) fakedesk.Reply {
	b, _ := json.Marshal(result)
	return fakedesk.Reply{Err: &driver.Error{Tool: tool, Message: msg, Result: b}}
}

func TestTypingDelayRules(t *testing.T) {
	for _, tc := range []struct {
		name, input, text string
		chromium          bool
		want              string
	}{
		{"foreground BMP", protocol.InputForeground, "héllo", false, "0"},
		{"foreground emoji", protocol.InputForeground, "hi 🎉", false, ""},
		{"background chromium", protocol.InputBackground, "hello", true, "1"},
		{"background native", protocol.InputBackground, "hello", false, ""},
	} {
		d := scene()
		d.Chromiums[100] = tc.chromium
		focusOn(d, 100, "")
		newRig(t, d, tc.input).step(protocol.Step{Type: text(tc.text), Window: "Notes"})
		c := calls(d, "type_text")
		if len(c) != 1 || c[0].Arg("delay_ms") != tc.want {
			t.Errorf("%s: %s", tc.name, d)
		}
	}
}

func TestChromiumTypePacing(t *testing.T) {
	d := scene()
	d.Chromiums[100] = true
	focusOn(d, 100, "")
	long := strings.Repeat("abcdefghij", 62)
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text(long + "\nmore"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusConfirmed)
	for _, c := range calls(d, "type_text") {
		if c.Arg("delay_ms") != "1" {
			t.Fatalf("every chunk is paced: %s", d)
		}
	}
	// Characters an app could not take fail with what landed.
	d = scene()
	d.Chromiums[100] = true
	focusOn(d, 100, "")
	d.Replies["type_text"] = []fakedesk.Reply{failedWith("type_text", "type_text failed: an emoji cannot reach this app in the background", map[string]any{"delivered_chars": 3})}
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("abc🎉"), Window: "Notes"})
	if r.Error == nil || !strings.Contains(r.Error.Message, "3 of 4 characters landed") {
		t.Fatalf("%+v", r.Error)
	}
}

func TestStripTrailingProtocolTags(t *testing.T) {
	for in, want := range map[string]string{
		"":               "",
		"hello":          "hello",
		"hello\n</text>": "hello",
	} {
		if got := actions.StripProtocolTail(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	end := func(name string) string { return "<" + "/" + name + ">" }
	for in, want := range map[string]string{
		"hello\n" + end("parameter") + "\n" + end("invoke") + "\n": "hello",
		"hello\n" + end("ns:parameter"):                            "hello",
		"run\n" + end("function_calls"):                            "run",
		"x\n" + end("tool_use") + "\n" + end("tool_call"):          "x",
		"<text>quoted\n" + end("text"):                             "<text>quoted\n" + end("text"),
		"a " + end("text") + " b":                                  "a " + end("text") + " b",
		"<div>\n" + end("div"):                                     "<div>\n" + end("div"),
		"line\n" + end("p"):                                        "line\n" + end("p"),
	} {
		if got := actions.StripProtocolTail(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestTypeLiteralClosingTag(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	in := "say hello" + "<" + "/text>"
	newRig(t, d, "").step(protocol.Step{Type: text(in)})
	c := calls(d, "type_text")
	if len(c) != 1 || c[0].Arg("text") != in || c[0].Arg("literal") != "true" {
		t.Fatalf("a closing tag inside a line is typed as written: %s", d)
	}
}

func TestProtocolTailStripped(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	newRig(t, d, "").step(protocol.Step{Type: text("hi\n" + "<" + "/parameter>\n" + "<" + "/invoke>")})
	if got := typed(d); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("typed %q", got)
	}
}

func TestTypedTail(t *testing.T) {
	for in, want := range map[string]string{
		"hello":                      "hello",
		"line one\nline two":         "line two",
		"done\n":                     "",
		"a much longer line of text": "ger line of text",
	} {
		if got := actions.TypedTail(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := actions.TypedTail("a much longer line of text"); utf8.RuneCountInString(got) != 16 {
		t.Errorf("tail %q is not 16 characters", got)
	}
}

func TestTypeSettleBounded(t *testing.T) {
	d := scene()
	d.FocusHang = true
	x := newRig(t, d, "")
	x.x.Now, x.x.Sleep = nil, nil // real time: the budget is wall time
	start := time.Now()
	r := x.step(protocol.Step{Type: text("hello")})
	wantStatus(t, r, protocol.StatusConfirmed)
	if el := time.Since(start); el > 600*time.Millisecond {
		t.Fatalf("a focus lookup that never answers held the step for %s", el)
	}
}

func TestNonASCIIChunksOfEight(t *testing.T) {
	in := "abc äöüßéèêëîï end ñ"
	chunks := actions.Chunks(in)
	if strings.Join(chunks, "") != in {
		t.Fatalf("chunks %q do not add up", chunks)
	}
	for _, c := range chunks {
		seen := map[rune]bool{}
		for _, r := range c {
			if r >= 0x80 {
				seen[r] = true
			}
		}
		if len(seen) > 8 {
			t.Fatalf("chunk %q holds %d non-ASCII characters", c, len(seen))
		}
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks %q", chunks)
	}
	d := scene()
	focusOn(d, 100, "")
	newRig(t, d, "").step(protocol.Step{Type: text(in)})
	if got := typed(d); strings.Join(got, "") != in || len(got) != 2 {
		t.Fatalf("typed %q", got)
	}
}

func TestTrailingNewlineSameCall(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	newRig(t, d, "").step(protocol.Step{Type: text("hello\n")})
	if joined(d.Tools()) != "type_text" || typed(d)[0] != "hello\n" {
		t.Fatalf("the newline goes with its text: %s", d)
	}
}

func TestTrailingNewlineTypedWithText(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	newRig(t, d, "").step(protocol.Step{Type: text("one\ntwo\n")})
	if joined(d.Tools()) != "type_text,press_key,type_text" {
		t.Fatalf("lines: %s", d)
	}
	if got := typed(d); got[0] != "one" || got[1] != "two\n" {
		t.Fatalf("typed %q", got)
	}
}

func TestMacForegroundTypeCallSequence(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	newRig(t, d, protocol.InputForeground).step(protocol.Step{Type: text("abc")})
	if joined(d.Tools()) != "type_text" || len(d.Raised) != 0 {
		t.Fatalf("foreground typing addresses the front window without raising it: %s", d)
	}
	if c := d.Calls[0]; c.Arg("scope") != "desktop" || c.Arg("delivery_mode") != "foreground" {
		t.Fatalf("%v", c.Args)
	}
}

func TestTypeAtPointClicksThenTypes(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	r := newRig(t, d, "").step(protocol.Step{Type: text("abc"), Into: at(200, 210)})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
	if joined(d.Tools()) != "click,type_text" {
		t.Fatalf("click first, then type: %s", d)
	}
	if strings.Contains(d.Calls[1].Arg("text"), "200") {
		t.Fatal("the text carries no point")
	}
}

func TestTypeDeadlineScales(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	long := strings.Repeat("x", 1000)
	x := newRig(t, d, "")
	x.step(protocol.Step{Type: text("hi")})
	x.step(protocol.Step{Type: text(long)})
	c := calls(d, "type_text")
	short, big := c[0].Budget, c[1].Budget
	if short > driver.RequestTimeout+time.Second || short < driver.RequestTimeout-time.Second {
		t.Fatalf("a short text gets about %s: %s", driver.RequestTimeout, short)
	}
	if want := driver.TypeTimeout(long); big < want-time.Second {
		t.Fatalf("a long text gets %s, want about %s", big, want)
	}
}

func TestPartialTypeLineCount(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.OK, fakedesk.Failed("type_text", "type_text failed: the app stopped answering")}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("one\ntwo\nthree"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if r.OK || !retryFalse(r) || !strings.Contains(r.Error.Message, "1 of 3 lines were typed before it") {
		t.Fatalf("%+v %+v", r, r.Error)
	}
}

func TestTypeReportsLinesTypedBeforeFailure(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.OK, fakedesk.OK, fakedesk.Failed("type_text", "type_text failed: gone")}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("a\nb\nc"), Window: "Notes"})
	if !strings.Contains(r.Error.Message, "2 of 3 lines") {
		t.Fatalf("%+v", r.Error)
	}
	d = scene()
	focusOn(d, 100, "")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Failed("type_text", "type_text failed: gone")}
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("single"), Window: "Notes"})
	if r.Error == nil || strings.Contains(r.Error.Message, "lines") {
		t.Fatalf("a single line says nothing about lines: %+v", r.Error)
	}
}

func TestFailedTypeCountsLanded(t *testing.T) {
	for _, tc := range []struct {
		result map[string]any
		want   string
	}{
		{map[string]any{"delivered_chars": 4}, "4 of 10 characters landed"},
		{map[string]any{"skipped_chars": 3}, "7 of 10 characters landed"},
		{map[string]any{}, "0 of 10 characters landed"},
	} {
		d := scene()
		focusOn(d, 100, "")
		d.Replies["type_text"] = []fakedesk.Reply{failedWith("type_text", "type_text failed", tc.result)}
		r := newRig(t, d, "").step(protocol.Step{Type: text("0123456789")})
		wantStatus(t, r, protocol.StatusUnconfirmed)
		if !strings.Contains(r.Error.Message, tc.want) || !retryFalse(r) {
			t.Errorf("%v: %+v", tc.result, r.Error)
		}
		if len(calls(d, "type_text")) != 1 {
			t.Errorf("the rest was typed again: %s", d)
		}
	}
}

func TestTypeTerminalFlag(t *testing.T) {
	for _, reply := range []string{`{"effect":"confirmed","terminal":true}`, `{"effect":"unverifiable","terminal":true}`} {
		d := scene()
		focusOn(d, 100, "$ ")
		d.Replies["type_text"] = []fakedesk.Reply{{Raw: reply}}
		r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("ls"), Window: "Notes"})
		if !notesHave(r, "terminal: true") {
			t.Errorf("%s: %+v", reply, r)
		}
	}
}

func TestTerminalLineWithoutConfirmGetsNoReturn(t *testing.T) {
	d := scene()
	focusOn(d, 100, "$ ")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.OK, {Raw: `{"effect":"unverifiable","terminal":true,"note":"no echo"}`}}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("cd /tmp\nrm -rf build\nls"), Window: "Notes"})
	if joined(d.Tools()) != "type_text,press_key,type_text" {
		t.Fatalf("the unconfirmed line got its Return: %s", d)
	}
	if r.OK || r.Error == nil || !strings.Contains(r.Error.Message, "line 2 of 3") || !strings.Contains(r.Error.Message, "no echo") {
		t.Fatalf("%+v", r.Error)
	}
}

func TestTerminalReadbackMatch(t *testing.T) {
	for _, tc := range []struct {
		screen, typed string
		want          bool
	}{
		{"$ echo hello", "echo hello", true},
		{"$ echohello", "echo hello", false},
		{"$ echo HELLO", "echo hello", false},
		{"$ echo hel\nlo", "echo hello", true},
		{"$ echo hello", "echo hello ", true},
		{"$ echo hello", "echo hello\t", true},
	} {
		if got := actions.TerminalShows(tc.screen, tc.typed); got != tc.want {
			t.Errorf("%q shows %q: %v", tc.screen, tc.typed, got)
		}
	}
}

func TestTerminalConfirmCountsCopies(t *testing.T) {
	unverified := fakedesk.Reply{Raw: `{"effect":"unverifiable","terminal":true}`}
	d := scene()
	focusOn(d, 100, "$ git push\n$ ", "$ git push\n$ git pu")
	d.Replies["type_text"] = []fakedesk.Reply{unverified}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("git push"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusUnconfirmed)

	d = scene()
	focusOn(d, 100, "$ git push\n$ ", "$ git push\n$ git push")
	d.Replies["type_text"] = []fakedesk.Reply{unverified}
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("git push"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusConfirmed)

	if n := actions.CountCopies("a\nab\na", "a"); n != 3 {
		t.Fatalf("copies %d", n)
	}
}

func TestNoTerminalRetype(t *testing.T) {
	for _, input := range []string{protocol.InputBackground, protocol.InputAuto} {
		d := scene()
		focusOn(d, 100, "Password:")
		d.Replies["type_text"] = []fakedesk.Reply{{Raw: `{"effect":"unverifiable","terminal":true,"escalation":{"recommended":"foreground","reason":"retry in the foreground"}}`}}
		r := newRig(t, d, input).step(protocol.Step{Type: text("abc"), Window: "Notes"})
		wantStatus(t, r, protocol.StatusUnconfirmed)
		if len(calls(d, "type_text")) != 1 || !retryFalse(r) || r.Input != protocol.InputBackground {
			t.Fatalf("%s: typed more than once: %s", input, d)
		}
		for _, n := range r.Notes {
			if strings.Contains(n, "foreground") || strings.Contains(n, "type it again") {
				t.Fatalf("%s: advice to retype: %q", input, n)
			}
		}
	}
}

func TestInsertRewrittenNotRetyped(t *testing.T) {
	d := scene()
	focusOn(d, 100, "teh cat", "the cat")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("inserted through accessibility")}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("x"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if len(calls(d, "type_text")) != 1 || len(calls(d, "press_key")) != 0 {
		t.Fatalf("the text was entered again: %s", d)
	}
}

func TestTypeSettleTimings(t *testing.T) {
	// A field that changed without growing answers 0.5 s after it held still.
	d := scene()
	focusOn(d, 100, "abc", "abd")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("")}
	x := newRig(t, d, protocol.InputBackground)
	start := x.clk.now()
	r := x.step(protocol.Step{Type: text("x"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if el := x.clk.now().Sub(start); el < actions.ReadbackStill || el > actions.ReadbackStill+100*time.Millisecond {
		t.Fatalf("answered after %s", el)
	}
	// A field that never takes the text answers after about 0.7 s.
	d = scene()
	focusOn(d, 100, "abc")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("")}
	x = newRig(t, d, protocol.InputBackground)
	start = x.clk.now()
	wantStatus(t, x.step(protocol.Step{Type: text("x"), Window: "Notes"}), protocol.StatusUnconfirmed)
	if el := x.clk.now().Sub(start); el < actions.ReadbackUntouched || el > actions.ReadbackUntouched+100*time.Millisecond {
		t.Fatalf("answered after %s", el)
	}
}

func TestBlankReadbackUnconfirmed(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("value write")}
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("hello"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !r.OK || !notesHave(r, "blank") || strings.Contains(r.Evidence, "typed 0") {
		t.Fatalf("%+v", r)
	}
}

func TestCaptureFailureAfterInput(t *testing.T) {
	d := scene()
	focusOn(d, 100, "")
	d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("")}
	_ = d
	// The read-back after typing fails.
	d2 := scene()
	d2.FocusErr = errFocus
	d2.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("")}
	r := newRig(t, d2, protocol.InputBackground).step(protocol.Step{Type: text("hello"), Window: "Notes", AnyFocus: true})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !r.OK || r.Error != nil || !notesHave(r, "delivered") {
		t.Fatalf("a delivered type whose read-back failed: %+v", r)
	}
	// A set whose read-back fails is still delivered.
	d3 := scene()
	x3 := newRig(t, d3, "")
	d3.ReadErr = errFocus
	r = x3.step(protocol.Step{Set: tgt(`textbox "Name"`), Value: raw("Ada")})
	if !r.OK || r.Status != protocol.StatusUnconfirmed || len(d3.Values) != 1 {
		t.Fatalf("%+v", r)
	}
}

var errFocus = &actions.AXError{Code: -25204, Op: "focus"}

func TestTypeFocusWaitPolls(t *testing.T) {
	d := scene()
	d.Focus[100] = []*view.RawNode{nil, nil, nil, nil, nil, nil, nil, d.Node("name")}
	x := newRig(t, d, "")
	start := x.clk.now()
	r := x.step(protocol.Step{Type: text("ada"), Into: tgt(`textbox "Name"`)})
	wantStatus(t, r, protocol.StatusConfirmed)
	if el := x.clk.now().Sub(start); el < 700*time.Millisecond || el > time.Second {
		t.Fatalf("typed after %s", el)
	}
	// Never focused: nothing is typed after 1 s.
	d = scene()
	x = newRig(t, d, "")
	start = x.clk.now()
	r = x.step(protocol.Step{Type: text("ada"), Into: tgt(`textbox "Name"`)})
	wantCode(t, r, protocol.ErrFocusMismatch)
	if len(calls(d, "type_text")) != 0 || x.clk.now().Sub(start) < time.Second {
		t.Fatalf("%s", d)
	}
	// A password step finds a plain field with a sheet holding focus.
	d = scene()
	d.Window("cg:10").Role = "sheet"
	focusOn(d, 100, "")
	r = newRig(t, d, "").step(protocol.Step{Type: text("hunter2"), Into: tgt(`textbox "Name"`), Secret: true})
	if r.Error == nil || !strings.Contains(r.Error.Message, "not a password field") || !strings.Contains(r.Error.Message, "sheet") {
		t.Fatalf("%+v", r.Error)
	}
	if strings.Contains(r.Error.Message, "hunter2") {
		t.Fatal("the secret leaked")
	}
}

func TestBackgroundTypeChecksFocusAfterClick(t *testing.T) {
	d := scene()
	d.Add("cg:10", "other", "textbox", "Other", fakedesk.Box(100, 300, 200, 20), "cg:10")
	other := *d.Node("other")
	d.Focus[100] = []*view.RawNode{&other}
	x := newRig(t, d, protocol.InputBackground)
	d.Replies["click"] = []fakedesk.Reply{{Raw: `{"effect":"unverifiable","focused":{"role":"textbox","name":"Other","at_point":false}}`}}
	r := x.step(protocol.Step{Click: tgt(x.ref("cg:10", "name"))})
	if !notesHave(r, `focus: textbox "Other"`) {
		t.Fatalf("the click names the focus: %+v", r)
	}
	r = x.step(protocol.Step{Type: text("ada"), Window: "Notes"})
	wantCode(t, r, protocol.ErrFocusMismatch)
	if !strings.Contains(r.Error.Message, "Other") || len(calls(d, "type_text")) != 0 {
		t.Fatalf("%+v", r.Error)
	}
	// A tab moves the focus on purpose: typing then goes through.
	wantStatus(t, x.step(protocol.Step{Key: "tab", Window: "Notes"}), protocol.StatusConfirmed)
	r = x.step(protocol.Step{Type: text("ada"), Window: "Notes"})
	if r.Status == protocol.StatusRefused {
		t.Fatalf("%+v", r.Error)
	}
}

func TestTypeFocusUnverified(t *testing.T) {
	d := scene()
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("ada"), Window: "Notes"})
	wantCode(t, r, protocol.ErrFocusMismatch)
	if !strings.Contains(r.Error.Hint, "any_focus") || len(d.Calls) != 0 {
		t.Fatalf("%+v", r.Error)
	}
	r = newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("ada"), Window: "Notes", AnyFocus: true})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !notesHave(r, "before Return or Send") || len(calls(d, "type_text")) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestUnverifiedFocusOptIn(t *testing.T) {
	d := scene()
	d.Focus[100] = []*view.RawNode{node("button", "OK", "")}
	x := newRig(t, d, protocol.InputBackground)
	wantStatus(t, x.step(protocol.Step{Type: text("a"), Window: "Notes"}), protocol.StatusRefused)
	r := x.step(protocol.Step{Type: text("a"), Window: "Notes", AnyFocus: true})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !r.OK {
		t.Fatalf("%+v", r)
	}
}

func TestUnconfirmedBackgroundFlagged(t *testing.T) {
	d := scene()
	d.Replies["press_key"] = []fakedesk.Reply{{Raw: `{"effect":"unverifiable","note":"no read-back for keys","escalation":{"recommended":"foreground","reason":"retry in front"}}`}, fakedesk.OK}
	x := newRig(t, d, protocol.InputBackground)
	r := x.step(protocol.Step{Key: "return", Window: "Notes"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !retryFalse(r) || !notesHave(r, "no read-back for keys") || !notesHave(r, "retry in front") {
		t.Fatalf("%+v", r)
	}
	r = x.step(protocol.Step{Key: "return", Window: "Notes"})
	wantStatus(t, r, protocol.StatusConfirmed)
	if len(r.Notes) != 0 {
		t.Fatalf("a confirmed key is a plain ok: %+v", r)
	}
	fg := scene()
	focusOn(fg, 100, "")
	r = newRig(t, fg, protocol.InputForeground).step(protocol.Step{Type: text("hello")})
	wantStatus(t, r, protocol.StatusConfirmed)
	if r.Evidence != "typed 5 characters" || len(r.Notes) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestLockedInputMessages(t *testing.T) {
	d := scene()
	d.Locked = true
	r := newRig(t, d, protocol.InputBackground).step(protocol.Step{Type: text("x"), Window: "Notes"})
	wantCode(t, r, protocol.ErrScreenLocked)
	if !strings.Contains(r.Error.Message, "cannot unlock") || !strings.Contains(r.Error.Hint, "Screen Sharing") {
		t.Fatalf("%+v", r.Error)
	}
	x := newRigPolicyLocked(t, d)
	r = x.step(protocol.Step{Type: text("x"), Window: "Notes"})
	wantStatus(t, r, protocol.StatusUnconfirmed)
	if !notesHave(r, "key window of TextEdit as it was before the lock") {
		t.Fatalf("%+v", r)
	}
	if c := calls(d, "type_text"); len(c) != 1 || c[0].Arg("locked_input") != "true" {
		t.Fatalf("%s", d)
	}
}
