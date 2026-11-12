package plan

import (
	"strings"
	"testing"

	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
)

// term is Terminal in front (cg:4, pid 400) whose screen echoes typed text
// through echo; the driver never confirms typing itself.
func term(t *testing.T, input string, screen string, echo func(typed string) string) *rig {
	r := newRig(t, policy.Config{Input: input})
	r.d.Win("cg:4", "Terminal", "bash", 400, [4]int{0, 0, 800, 600}, true)
	r.d.setScreen(screen)
	r.d.screenPID = 400
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.Unverified("not reported")}
	r.d.Replies["press_key"] = []fakedesk.Reply{fakedesk.OK}
	r.d.onDriver = func(tool string, args map[string]any) {
		switch tool {
		case "type_text":
			text, _ := args["text"].(string)
			if text == "\n" {
				r.d.setScreen(r.d.getScreen() + "\n$ ")
				return
			}
			r.d.setScreen(r.d.getScreen() + echo(text))
		case "press_key":
			if args["key"] == "return" {
				r.d.setScreen(r.d.getScreen() + "\n$ ")
			}
		}
	}
	return r
}

func full(s string) string { return s }
func none(string) string   { return "" }

func termSeq(d *desk) string {
	var out []string
	for _, c := range d.Calls {
		switch c.Tool {
		case "type_text":
			out = append(out, "type:"+strings.ReplaceAll(c.Arg("text"), "\n", `\n`))
		case "press_key":
			out = append(out, "key:"+c.Arg("key"))
		}
	}
	return strings.Join(out, " ")
}

func returns(d *desk) int {
	n := 0
	for _, c := range d.Calls {
		if c.Tool == "press_key" && c.Arg("key") == "return" {
			n++
		}
	}
	return n
}

func TestTerminalEchoOutcomes(t *testing.T) {
	r := term(t, "", "$ ", full)
	res := r.run(protocol.Step{Type: str("ls\n")})
	mustPass(t, res)
	if termSeq(r.d) != "type:ls key:return" || res.Steps[0].Status != protocol.StatusConfirmed {
		t.Fatalf("%s %s", termSeq(r.d), jsonOf(res.Steps[0]))
	}
	// In part: stop before the Return, no retry.
	r = term(t, "", "$ ", func(s string) string { return s[:1] })
	res = r.run(protocol.Step{Type: str("ls\n")}, protocol.Step{Key: "cmd+k"})
	mustStop(t, res, 0, protocol.ErrNotShown)
	if termSeq(r.d) != "type:ls" {
		t.Fatal(termSeq(r.d))
	}
	// Not at all (a prompt that does not echo): one Return, unconfirmed.
	for _, input := range []string{"foreground", "background", "auto"} {
		r = term(t, input, "Password: ", none)
		steps := []protocol.Step{{Type: str("hunter2\n")}}
		if input != "foreground" {
			r.d.Window("cg:4").Active = false
			r.d.Win("cg:9", "Safari", "Start", 900, [4]int{0, 0, 10, 10}, true)
			steps = append([]protocol.Step{{Launch: "Terminal"}}, steps...)
		}
		res = r.run(steps...)
		mustPass(t, res)
		last := res.Steps[len(res.Steps)-1]
		if returns(r.d) != 1 || len(typeCalls(r.d)) != 1 || last.Status != protocol.StatusUnconfirmed || !hasNote(last.Notes, "terminal: true") {
			t.Fatalf("%s: %s %s", input, termSeq(r.d), jsonOf(last))
		}
	}
}

func TestTerminalEchoComparison(t *testing.T) {
	for _, c := range []struct {
		typed string
		echo  func(string) string
		ok    bool
	}{
		{"ls -la  \n", func(s string) string { return strings.TrimRight(s, " ") }, true},
		{"   \n", full, true},
		{"abcdefgh\n", func(s string) string { return s[:4] + "\n" + s[4:] }, true},
		{"LS\n", strings.ToLower, false},
		{"a  b\n", func(string) string { return "a b" }, false},
	} {
		r := term(t, "", "$ ", c.echo)
		res := r.run(protocol.Step{Type: str(c.typed)})
		if res.OK != c.ok {
			t.Fatalf("%q: %s", c.typed, jsonOf(res))
		}
		if !c.ok && returns(r.d) != 0 {
			t.Fatalf("%q: a Return went after a line that did not show", c.typed)
		}
	}
}

func TestTerminalLongScreenReadBack(t *testing.T) {
	long := strings.Repeat("x", 70000) + "\n$ "
	r := term(t, "", long, full)
	mustPass(t, r.run(protocol.Step{Type: str("make\n")}))
	if returns(r.d) != 1 {
		t.Fatal(termSeq(r.d))
	}
	r = term(t, "", long, func(s string) string { return s[:2] })
	mustStop(t, r.run(protocol.Step{Type: str("make\n")}, protocol.Step{Key: "cmd+k"}), 0, protocol.ErrNotShown)
	if returns(r.d) != 0 || len(keyCalls(r.d)) != 0 {
		t.Fatal(termSeq(r.d))
	}
	r = term(t, "", long, none)
	res := r.run(protocol.Step{Type: str("make\n")})
	mustPass(t, res)
	if res.Steps[0].Status != protocol.StatusUnconfirmed {
		t.Fatal(jsonOf(res.Steps[0]))
	}
}

func TestTerminalPartialStops(t *testing.T) {
	r := term(t, "", "$ ", func(s string) string { return s[:len(s)-1] })
	res := r.run(protocol.Step{Type: str("rm -rf build\n")})
	mustStop(t, res, 0, protocol.ErrNotShown)
	if returns(r.d) != 0 || len(typeCalls(r.d)) != 1 {
		t.Fatal(termSeq(r.d))
	}
	// Outside the recognised terminals the window is an ordinary one.
	if terminalApp("Code") || terminalApp("Tabby") || !terminalApp("iTerm2") || !terminalApp("Ghostty") {
		t.Fatal("terminal list")
	}
}

func TestMultiLineTerminalTyping(t *testing.T) {
	r := term(t, "", "$ ", full)
	mustPass(t, r.run(protocol.Step{Type: str("cd /tmp\r\nls\n")}))
	if termSeq(r.d) != "type:cd /tmp key:return type:ls key:return" {
		t.Fatal(termSeq(r.d))
	}
	// A no-echo line still gets exactly one Return.
	n := 0
	r = term(t, "", "$ ", func(s string) string {
		n++
		if n == 1 {
			return s
		}
		return ""
	})
	mustPass(t, r.run(protocol.Step{Type: str("read -s pw\nhunter2\n")}))
	if termSeq(r.d) != "type:read -s pw key:return type:hunter2 key:return" {
		t.Fatal(termSeq(r.d))
	}
	// A leading blank line is submitted on its own through the typing path.
	r = term(t, "", "Name: ", full)
	mustPass(t, r.run(protocol.Step{Type: str("\nyes\n")}))
	if termSeq(r.d) != `type:\n type:yes key:return` {
		t.Fatal(termSeq(r.d))
	}
}

func TestMultiLineTerminalOutcome(t *testing.T) {
	// The driver confirmed every line: confirmed, even with an unconfirmed
	// Return between them.
	r := term(t, "", "$ ", full)
	r.d.Replies["type_text"] = []fakedesk.Reply{fakedesk.OK}
	r.d.Replies["press_key"] = []fakedesk.Reply{fakedesk.Unverified("not reported")}
	res := r.run(protocol.Step{Type: str("a\nb\n")})
	mustPass(t, res)
	if res.Steps[0].Status != protocol.StatusConfirmed {
		t.Fatal(jsonOf(res.Steps[0]))
	}
	// A line that shows in part names its number and what was sent.
	n := 0
	r = term(t, "", "$ ", func(s string) string {
		if n++; n == 2 {
			return s[:1]
		}
		return s
	})
	res = r.run(protocol.Step{Type: str("one\ntwo\nthree\n")})
	mustStop(t, res, 0, protocol.ErrNotShown)
	if !strings.Contains(res.Error.Message, "line 2 of 3") || !strings.Contains(res.Error.Message, "previous line and its Return were sent") || returns(r.d) != 1 {
		t.Fatalf("%+v %s", res.Error, termSeq(r.d))
	}
}

func TestMultilineReturnGate(t *testing.T) {
	r := term(t, "", "$ ", func(s string) string { return s[:1] })
	mustStop(t, r.run(protocol.Step{Type: str("echo hi\nexit\n")}), 0, protocol.ErrNotShown)
	if termSeq(r.d) != "type:echo hi" {
		t.Fatal(termSeq(r.d))
	}
}

func TestMultiLineFieldTyping(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	v := ""
	r.d.Focus[100] = []*viewNode{{Handle: "body", Role: "textbox", Value: &v, States: []string{"focused"}}}
	mustPass(t, r.run(protocol.Step{Type: str("first\nsecond\nthird")}))
	ty := typeCalls(r.d)
	if len(ty) != 1 || ty[0].Arg("text") != "first\nsecond\nthird" || returns(r.d) != 0 {
		t.Fatalf("%s", r.d)
	}
}

func TestTypeSendsWholeText(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	v := ""
	r.d.Focus[100] = []*viewNode{{Handle: "body", Role: "textbox", Value: &v, States: []string{"focused"}}}
	text := "<div>\n  <p>hi</p>\n</div>"
	mustPass(t, r.run(protocol.Step{Type: str(text)}))
	if ty := typeCalls(r.d); len(ty) != 1 || ty[0].Arg("text") != text {
		t.Fatalf("%s", r.d)
	}
}

func TestSecretEchoBlocksReturn(t *testing.T) {
	r := term(t, "", "$ ", full)
	res := r.run(protocol.Step{Type: str("hunter2\n"), Secret: true})
	mustStop(t, res, 0, protocol.ErrNotShown)
	if returns(r.d) != 0 || strings.Contains(jsonOf(res), "hunter2") {
		t.Fatalf("%s %s", termSeq(r.d), jsonOf(res))
	}
}

func TestSecretReturnGate(t *testing.T) {
	// A read -s prompt: the password does not show, one Return.
	r := term(t, "", "Password: ", none)
	mustPass(t, r.run(protocol.Step{Type: str("hunter2\n"), Secret: true}))
	if returns(r.d) != 1 {
		t.Fatal(termSeq(r.d))
	}
	// The screen changed without a prompt taking it: no Return.
	r = term(t, "", "$ ", func(string) string { return "zsh: command not found" })
	mustStop(t, r.run(protocol.Step{Type: str("hunter2\n"), Secret: true}), 0, protocol.ErrNotShown)
	if returns(r.d) != 0 {
		t.Fatal(termSeq(r.d))
	}
	// A window whose text cannot be read gets its Return unchecked.
	r = term(t, "", "", none)
	r.d.screen = nil
	mustPass(t, r.run(protocol.Step{Type: str("hunter2\n"), Secret: true, AnyFocus: true}))
	if returns(r.d) != 1 {
		t.Fatal(termSeq(r.d))
	}
	// A password field outside a terminal: one Return, no false stop.
	r = secretRig(t)
	mustPass(t, r.run(protocol.Step{Type: str("hunter2\n"), Secret: true, Into: sel(`textbox protected`)}))
	if returns(r.d) != 1 {
		t.Fatal(r.d)
	}
}

func TestPasswordOnlyIntoSecureField(t *testing.T) {
	// A focused plain field, even one named like the recorded one: nothing
	// is typed.
	r := secretRig(t)
	r.d.Node("pw").States = []string{"focused"}
	res := r.run(protocol.Step{Type: str("hunter2"), Secret: true, Into: sel(`textbox "Password" protected`)})
	mustStop(t, res, 0, protocol.ErrFocusMismatch)
	if len(typeCalls(r.d)) != 0 {
		t.Fatal(r.d)
	}
	// The focus reaches the password field a moment later: it types.
	r = secretRig(t)
	plain := *r.d.Node("pw")
	plain.States = []string{"focused"}
	r.d.Focus[100] = []*viewNode{&plain, &plain, r.d.Node("pw")}
	mustPass(t, r.run(protocol.Step{Type: str("hunter2"), Secret: true, Into: sel(`textbox protected`)}))
	if len(typeCalls(r.d)) != 1 {
		t.Fatal(r.d)
	}
}

func TestRecordedTypeFocusCheck(t *testing.T) {
	// The recorded field holds the focus: it types.
	r := newRig(t, policy.Config{}).notes()
	v := ""
	name := &viewNode{Handle: "name", Role: "textbox", Name: "Name", Value: &v, States: []string{"focused"}}
	r.d.Focus[100] = []*viewNode{name}
	mustPass(t, r.run(protocol.Step{Type: str("Alice"), Into: sel(`textbox "Name"`)}))
	// Another field holds it: a stop with a stop report, nothing typed.
	r = newRig(t, policy.Config{}).notes()
	other := &viewNode{Handle: "email", Role: "textbox", Name: "Email", Value: &v, States: []string{"focused"}}
	r.d.Focus[100] = []*viewNode{other}
	res := r.run(protocol.Step{Type: str("Alice"), Into: sel(`textbox "Name"`), Intent: "type the name"})
	mustStop(t, res, 0, protocol.ErrFocusMismatch)
	if len(typeCalls(r.d)) != 0 || !strings.Contains(res.Error.Message, `"Email"`) || !strings.Contains(res.Error.Hint, "stop report") {
		t.Fatalf("%+v %s", res.Error, r.d)
	}
	// No focused element can be read: the same.
	r = newRig(t, policy.Config{}).notes()
	res = r.run(protocol.Step{Type: str("Alice"), Into: sel(`textbox "Name"`)})
	mustStop(t, res, 0, protocol.ErrFocusMismatch)
	if len(typeCalls(r.d)) != 0 {
		t.Fatal(r.d)
	}
}
