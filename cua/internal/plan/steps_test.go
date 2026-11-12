package plan

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/actions/fakedesk"
	"fennec/cua/internal/models"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// viewMenu gives Notes a View menu holding item.
func viewMenu(r *rig, item string, states ...string) {
	r.d.Add("cg:1", "bar", "menubar", "", nil, "cg:1")
	r.d.Add("cg:1", "view", "menubaritem", "View", nil, "bar")
	r.d.Add("cg:1", "viewmenu", "menu", "View", nil, "view")
	r.d.Add("cg:1", "item", "menuitem", item, nil, "viewmenu", states...)
}

func escapes(d *desk) int {
	n := 0
	for _, c := range d.Calls {
		if c.Tool == "press_key" && c.Arg("key") == "escape" {
			n++
		}
	}
	return n
}

func TestMenuRereadsAfterOpen(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	viewMenu(r, "Wrap to Page")
	r.d.onPerform = func(id, _ string) {
		if id == "view" {
			r.d.Node("item").Name = "Wrap to Window"
		}
	}
	mustPass(t, r.run(protocol.Step{Menu: "View > Wrap to Window"}))
	if presses(r.d) != "view:AXPress,item:AXPress" || len(keyCalls(r.d)) != 0 {
		t.Fatalf("%v %s", r.d.Performs, r.d)
	}
	// A path that ends nowhere closes every menu it opened.
	r = newRig(t, policy.Config{}).notes()
	viewMenu(r, "Wrap to Page")
	res := r.run(protocol.Step{Menu: "View > Zoom"})
	mustStop(t, res, 0, protocol.ErrTargetNotFound)
	if menusClosed(r.d) != 1 || presses(r.d) != "view:AXPress" {
		t.Fatalf("%v %s", r.d.Performs, r.d)
	}
}

func TestMenuRepeatedBarTakesFirst(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "bar", "menubar", "", nil, "cg:1")
	r.d.Add("cg:1", "f1", "menubaritem", "File", nil, "bar")
	r.d.Add("cg:1", "n1", "menuitem", "New", nil, "f1")
	r.d.Add("cg:1", "f2", "menubaritem", "File", nil, "bar")
	r.d.Add("cg:1", "n2", "menuitem", "New", nil, "f2")
	mustPass(t, r.run(protocol.Step{Menu: "File > New"}))
	if strings.Join(r.d.Performs, ",") != "f1:AXPress,n1:AXPress" {
		t.Fatal(r.d.Performs)
	}
}

func TestMenuToggleReadsMarkWhileOpen(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	// The closed menu bar shows a stale check mark; the open menu shows
	// the item off.
	viewMenu(r, "Status Bar", "checked")
	stale := true
	r.d.onPerform = func(id, _ string) {
		if id == "view" && stale {
			stale = false
			r.d.Node("item").States = nil
		}
	}
	on := raw("true")
	res := r.run(protocol.Step{Menu: "View > Status Bar", Value: on})
	mustPass(t, res)
	if !strings.Contains(res.Steps[0].Evidence, "reads on") {
		t.Fatal(jsonOf(res.Steps[0]))
	}
	// The same step again presses nothing.
	mustPass(t, r.run(protocol.Step{Menu: "View > Status Bar", Value: on}))
	n := 0
	for _, p := range r.d.Performs {
		if p == "item:AXPress" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("pressed %d times: %v", n, r.d.Performs)
	}
	// A mark that does not flip fails, saying it is still off.
	r = newRig(t, policy.Config{}).notes()
	viewMenu(r, "Status Bar")
	r.d.onPerform = func(id, _ string) {
		if id == "item" {
			r.d.Node("item").States = nil
		}
	}
	res = r.run(protocol.Step{Menu: "View > Status Bar", Value: on})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	if !strings.Contains(res.Error.Message, "still off") {
		t.Fatal(res.Error)
	}
}

func TestMenuBarFallbackBeforeModel(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "bar", "menubar", "", nil, "cg:1")
	r.d.Add("cg:1", "file", "menubaritem", "File", nil, "bar")
	r.d.Add("cg:1", "print", "menuitem", "Print…", nil, "file")
	m := r.models()
	r.e.Screen = newScreen(10, 10)
	res := r.run(protocol.Step{Press: sel(`"Print…"`)})
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "print:AXPress" || m.calls() != 0 || len(keyCalls(r.d)) != 0 {
		t.Fatalf("%v %d %s", r.d.Performs, m.calls(), r.d)
	}
	// A verify that finds its target only in the menu bar checks it there.
	res = r.run(protocol.Step{Expect: &protocol.Expect{Exists: sel(`menuitem "Print…" disabled`), TimeoutMs: 200}})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	mustPass(t, r.run(protocol.Step{Expect: &protocol.Expect{Exists: sel(`menuitem "Print…" enabled`)}}))
}

func TestExactMenuItemBeatsContains(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "preview", "button", "Print Preview", fakedesk.Box(10, 10, 80, 20), "cg:1")
	r.d.Add("cg:1", "bar", "menubar", "", nil, "cg:1")
	r.d.Add("cg:1", "file", "menubaritem", "File", nil, "bar")
	r.d.Add("cg:1", "print", "menuitem", "Print", nil, "file")
	res := r.run(protocol.Step{Press: sel(`"Print"`)})
	mustPass(t, res)
	if strings.Join(r.d.Performs, ",") != "print:AXPress" || len(r.d.Calls) != 0 {
		t.Fatalf("%v %s", r.d.Performs, r.d)
	}
}

func TestDeclinedShortcutStops(t *testing.T) {
	r := newRig(t, policy.Config{Confirm: "every"}).notes()
	viewMenu(r, "Zoom")
	r.p.Gate.Confirmer = policy.DenyAll{}
	_, perr := r.e.Run(t.Context(), nil, protocol.ActRequest{Steps: []protocol.Step{{Menu: "View > Zoom"}}}, engineOpts())
	if perr == nil || perr.Code != protocol.ErrDeclined || len(r.d.Performs) != 0 || len(r.d.Calls) != 0 {
		t.Fatalf("%+v %s", perr, r.d)
	}
}

// checkbox adds a check box "Bold" whose presses cycle through states.
func checkbox(r *rig, start string, cycle map[string]string) {
	var st []string
	if start != "off" {
		st = []string{start}
		if start == "on" {
			st = []string{"checked"}
		}
	}
	r.d.Add("cg:1", "bold", "checkbox", "Bold", fakedesk.Box(10, 40, 20, 20), "cg:1", st...)
	state := start
	r.d.onPerform = func(id, _ string) {
		if id != "bold" {
			return
		}
		state = cycle[state]
		n := r.d.Node("bold")
		switch state {
		case "on":
			n.States = []string{"checked"}
		case "mixed":
			n.States = []string{"mixed"}
		default:
			n.States = nil
		}
	}
}

func TestCheckReadsBackAndCyclesMixed(t *testing.T) {
	cycle := map[string]string{"mixed": "on", "on": "off", "off": "on"}
	r := newRig(t, policy.Config{}).notes()
	checkbox(r, "mixed", cycle)
	res := r.run(protocol.Step{Set: sel(`checkbox "Bold"`), Value: raw("false")})
	mustPass(t, res)
	if len(r.d.Performs) != 2 || !strings.Contains(res.Steps[0].Evidence, "mixed") {
		t.Fatalf("%v %s", r.d.Performs, jsonOf(res.Steps[0]))
	}
	// Already in the wanted state: no press.
	r = newRig(t, policy.Config{}).notes()
	checkbox(r, "on", cycle)
	mustPass(t, r.run(protocol.Step{Set: sel(`checkbox "Bold"`), Value: raw("true")}))
	if len(r.d.Performs) != 0 {
		t.Fatal(r.d.Performs)
	}
	// A press that does not flip it fails, tracing what it found.
	r = newRig(t, policy.Config{}).notes()
	checkbox(r, "off", map[string]string{"off": "off"})
	res = r.run(protocol.Step{Set: sel(`checkbox "Bold"`), Value: raw("true")})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	if !strings.Contains(res.Error.Message, "still off") || !strings.Contains(res.Steps[0].Evidence, "found") || strings.Contains(res.Steps[0].Evidence, "reads on") {
		t.Fatalf("%s", jsonOf(res.Steps[0]))
	}
}

func TestSetExactReadBack(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "name", "textbox", "Name", fakedesk.Box(10, 70, 200, 20), "cg:1")
	mustPass(t, r.run(protocol.Step{Set: sel(`textbox "Name"`), Value: raw(`"Alice"`)}))
	// Text that doubled fails with the value read.
	r.d.onSet = func(_, v string) string { return v + v }
	res := r.run(protocol.Step{Set: sel(`textbox "Name"`), Value: raw(`"Bob"`)})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	if !strings.Contains(res.Error.Message, `"BobBob"`) {
		t.Fatal(res.Error)
	}
	// An empty value clears the field.
	r.d.onSet = nil
	mustPass(t, r.run(protocol.Step{Set: sel(`textbox "Name"`), Value: raw(`""`)}))
	if r.d.Values["name"] != "" {
		t.Fatal(r.d.Values)
	}
}

func TestSetSpinButtonUnit(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "size", "spinbutton", "Size", fakedesk.Box(10, 70, 50, 20), "cg:1")
	r.d.onSet = func(_, v string) string { return v + " pt" }
	mustPass(t, r.run(protocol.Step{Set: sel(`spinbutton "Size"`), Value: raw(`12`)}))
	r.d.onSet = func(_, v string) string { return v + "0" }
	mustStop(t, r.run(protocol.Step{Set: sel(`spinbutton "Size"`), Value: raw(`12`)}), 0, protocol.ErrExpectFailed)
}

func TestSetNestedComboField(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "font", "combobox", "Font", fakedesk.Box(10, 70, 200, 20), "cg:1")
	r.d.Add("cg:1", "fontfield", "textbox", "Font", fakedesk.Box(10, 70, 180, 20), "font")
	m := r.models()
	mustPass(t, r.run(protocol.Step{Set: sel(`"Font"`), Value: raw(`"Arial"`)}))
	if r.d.Values["fontfield"] != "Arial" || m.calls() != 0 {
		t.Fatalf("%v %d", r.d.Values, m.calls())
	}
}

func TestSetComboPicksOption(t *testing.T) {
	setup := func() *rig {
		r := newRig(t, policy.Config{}).notes()
		small := "Small"
		r.d.Add("cg:1", "size", "combobox", "Size", fakedesk.Box(10, 70, 100, 20), "cg:1")
		r.d.Node("size").Value = &small
		r.d.onPerform = func(id, _ string) {
			switch id {
			case "size":
				r.d.Add("cg:1", "list", "list", "", nil, "size")
				r.d.Add("cg:1", "o1", "option", "Small", fakedesk.Box(10, 90, 100, 20), "list")
				r.d.Add("cg:1", "o2", "option", "Large", fakedesk.Box(10, 110, 100, 20), "list")
			case "o2":
				v := "Large"
				r.d.Node("size").Value = &v
			}
		}
		return r
	}
	r := setup()
	mustPass(t, r.run(protocol.Step{Set: sel(`combobox "Size"`), Value: raw(`"Large"`)}))
	if strings.Join(r.d.Performs, ",") != "size:AXPress" || strings.Join(r.d.Selected, ",") != "o2" || len(typeCalls(r.d)) != 0 {
		t.Fatalf("%v %s", r.d.Performs, r.d)
	}
	r = setup()
	mustPass(t, r.run(protocol.Step{Set: sel(`combobox "Size"`), Value: raw(`"Small"`)}))
	if len(r.d.Performs) != 0 {
		t.Fatal(r.d.Performs)
	}
	r = setup()
	res := r.run(protocol.Step{Set: sel(`combobox "Size"`), Value: raw(`"Huge"`)})
	mustStop(t, res, 0, protocol.ErrTargetNotFound)
	if !strings.Contains(res.Error.Hint, `"Large"`) || strings.Join(r.d.Performs, ",") != "size:AXPress" {
		t.Fatalf("%+v %v", res.Error, r.d.Performs)
	}
}

func TestSetWithoutFocusSendsNothing(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "name", "textbox", "Name", fakedesk.Box(10, 70, 200, 20), "cg:1")
	r.d.SetErr = &actions.AXError{Code: actions.AXAttributeUnsupport, Op: "focus"}
	res := r.run(protocol.Step{Set: sel(`textbox "Name"`), Value: raw(`"x"`)})
	if res.OK || len(r.d.Calls) != 0 {
		t.Fatalf("%s %s", jsonOf(res), r.d)
	}
}

func TestPressNotDoneFails(t *testing.T) {
	r := newRig(t, policy.Config{Input: "background"}).notes()
	r.d.Window("cg:1").Active = false
	r.d.PerformErr["save"] = &actions.AXError{Code: -25200, Op: "press"}
	res := r.act(protocol.ActRequest{Window: "Notes", Input: "background", Steps: []protocol.Step{{Press: sel(`button "Save"`)}}})
	if res.OK || res.Error == nil || !strings.Contains(res.Error.Message, `"Save"`) {
		t.Fatalf("%s", jsonOf(res))
	}
}

func TestCloseFailsWhenDialogStays(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Win("cg:6", "TextEdit", "Save changes?", 100, [4]int{0, 0, 300, 100}, false).Role = "dialog"
	r.d.onClose = func(string) bool { return false }
	res := r.run(protocol.Step{Close: "Save changes?"})
	mustStop(t, res, 0, protocol.ErrNotShown)
	if !strings.Contains(res.Error.Message, "still open") {
		t.Fatal(res.Error)
	}
}

func verify(r *rig, target string, equals string) *protocol.ActResult {
	return r.run(protocol.Step{Expect: &protocol.Expect{Reads: sel(target), Equals: raw(equals), TimeoutMs: 300}})
}

func TestVerifyValueExact(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	v, pt := "123", "12 pt"
	r.d.Add("cg:1", "f", "textbox", "Count", nil, "cg:1")
	r.d.Node("f").Value = &v
	r.d.Add("cg:1", "s", "spinbutton", "Size", nil, "cg:1")
	r.d.Node("s").Value = &pt
	mustStop(t, verify(r, `textbox "Count"`, `"12"`), 0, protocol.ErrExpectFailed)
	mustPass(t, verify(r, `textbox "Count"`, `"123"`))
	mustPass(t, verify(r, `spinbutton "Size"`, `12`))
	mustStop(t, verify(r, `spinbutton "Size"`, `1`), 0, protocol.ErrExpectFailed)
	// No element: it fails whatever state it asks about.
	mustStop(t, verify(r, `checkbox "Nothing"`, `false`), 0, protocol.ErrExpectFailed)
	mustStop(t, r.run(protocol.Step{Expect: &protocol.Expect{Exists: sel(`checkbox "Nothing" disabled`), TimeoutMs: 300}}), 0, protocol.ErrExpectFailed)
}

func TestVerifyLinkURL(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "l", "link", "Docs", nil, "cg:1")
	r.d.Node("l").URL = "https://example.com/Docs"
	mustPass(t, verify(r, `link "Docs"`, `"https://example.com/Docs"`))
	mustStop(t, verify(r, `link "Docs"`, `"https://example.com/docs"`), 0, protocol.ErrExpectFailed)
	mustStop(t, verify(r, `link "Docs"`, `"https://example.com/Docs/"`), 0, protocol.ErrExpectFailed)
}

func bodyRig(t *testing.T) *rig {
	r := newRig(t, policy.Config{}).notes()
	v := "Dear Bob"
	r.d.Add("cg:1", "body", "document", "Body", nil, "cg:1")
	r.d.Node("body").Value = &v
	return r
}

func TestVerifyRoleOutsideControls(t *testing.T) {
	r := bodyRig(t)
	mustPass(t, verify(r, `document "Body"`, `"Dear Bob"`))
	mustStop(t, verify(r, `"Body"`, `"Dear Bob"`), 0, protocol.ErrExpectFailed)
}

func TestVerifyRoleNonInteractive(t *testing.T) {
	r := bodyRig(t)
	mustPass(t, r.run(protocol.Step{Expect: &protocol.Expect{Exists: sel(`document "Body"`)}}))
	mustStop(t, r.run(protocol.Step{Expect: &protocol.Expect{Exists: sel(`"Body"`), TimeoutMs: 200}}), 0, protocol.ErrExpectFailed)
}

func TestValueEquality(t *testing.T) {
	for _, c := range []struct {
		role, got, want string
		ok              bool
	}{
		{"textbox", "12", "12", true},
		{"textbox", "12 pt", "12", false},
		{"spinbutton", "12 pt", "12", true},
		{"spinbutton", "12", "12", true},
		{"slider", "50 %", "50", true},
		{"spinbutton", "120", "12", false},
		{"spinbutton", "12.5", "12", false},
		{"combobox", "Large ", "Large", false},
	} {
		if ValueEquals(c.role, c.got, c.want) != c.ok {
			t.Fatalf("%+v", c)
		}
	}
	r := newRig(t, policy.Config{}).notes()
	mustStop(t, verify(r, `checkbox "Missing"`, `false`), 0, protocol.ErrExpectFailed)
}

func looksRig(t *testing.T, answer string) *rig {
	r := newRig(t, policy.Config{}).notes()
	r.e.Screen = newScreen(50, 50)
	m := r.models()
	m.ask = func(a models.AskArgs) (*models.Asked, error) {
		if len(a.Images) != 1 {
			t.Fatal("no screenshot")
		}
		return &models.Asked{Text: answer, Served: protocol.Served{Role: "ask", Model: "fake-ask"}}, nil
	}
	return r
}

func TestVisualExpect(t *testing.T) {
	r := looksRig(t, `{"answer": false, "reason": "no banner shows"}`)
	res := r.run(protocol.Step{Expect: &protocol.Expect{Looks: "does a banner show?"}})
	mustStop(t, res, 0, protocol.ErrExpectFailed)
	if !strings.Contains(res.Error.Message, "expectation not met") || !strings.Contains(res.Error.Message, "no banner shows") {
		t.Fatal(res.Error)
	}
	r = looksRig(t, "```json\n{\"answer\": true, \"reason\": \"the banner shows\"}\n```")
	res = r.run(protocol.Step{Press: sel(`button "Save"`), Expect: &protocol.Expect{Looks: "does a banner show?"}})
	mustPass(t, res)
	if len(res.Steps[0].Served) != 1 {
		t.Fatal(jsonOf(res.Steps[0]))
	}
}

func TestVisualExpectNonBooleanFails(t *testing.T) {
	for _, a := range []string{`{"answer": "false"}`, `{"answer": "yes"}`, `maybe`, `{"reason": "x"}`} {
		r := looksRig(t, a)
		mustStop(t, r.run(protocol.Step{Expect: &protocol.Expect{Looks: "is it on?"}}), 0, protocol.ErrExpectFailed)
	}
}

func TestVisualCheckNonBoolean(t *testing.T) {
	if _, _, ok := parseVerdict(`{"answer":"true"}`); ok {
		t.Fatal("a string verdict was taken")
	}
	if v, _, ok := parseVerdict(`{"answer":true}`); !ok || !v {
		t.Fatal("a real verdict was refused")
	}
	r := looksRig(t, `{"answer": 1}`)
	mustStop(t, r.run(protocol.Step{Expect: &protocol.Expect{Looks: "is it on?"}}), 0, protocol.ErrExpectFailed)
}

// sendRig has Mail behind with a Send button and a To field.
func sendRig(t *testing.T) *rig {
	r := behind(t, "background")
	v := "bob@example.com"
	r.d.Add("cg:2", "send", "button", "Send", fakedesk.Box(200, 200, 50, 20), "cg:2")
	r.d.Add("cg:2", "to", "textbox", "To", fakedesk.Box(200, 240, 200, 20), "cg:2")
	r.d.Node("to").Value = &v
	return r
}

func TestUnconfirmedStepReadBack(t *testing.T) {
	// A background click is sent, not proven; a later read confirms it.
	r := sendRig(t)
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Send"`)},
		protocol.Step{Expect: &protocol.Expect{Reads: sel(`textbox "To"`), Equals: raw(`"bob@example.com"`)}})
	mustPass(t, res)
	if res.Steps[1].Status != protocol.StatusUnconfirmed {
		t.Fatal(jsonOf(res.Steps[1]))
	}
	// A mere presence check does not.
	r = sendRig(t)
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Send"`)},
		protocol.Step{Expect: &protocol.Expect{Exists: sel(`textbox "To"`)}})
	mustStop(t, res, 1, protocol.ErrNotShown)
	if !strings.Contains(res.Error.Message, "sent but not confirmed") {
		t.Fatal(res.Error)
	}
}

func TestUnconfirmedNoteKept(t *testing.T) {
	r := sendRig(t)
	r.d.Add("cg:2", "save", "button", "Save", fakedesk.Box(260, 200, 50, 20), "cg:2")
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Send"`)}, protocol.Step{Click: sel(`button "Save"`)})
	mustStop(t, res, 1, protocol.ErrNotShown)
	if !strings.Contains(res.Error.Message, "step 1") {
		t.Fatal(res.Error)
	}
}

func TestEvidenceAttribution(t *testing.T) {
	r := sendRig(t)
	res := r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Send"`)},
		protocol.Step{Wait: &protocol.WaitRequest{Expect: protocol.Expect{Front: "Inbox"}}})
	mustStop(t, res, 1, protocol.ErrNotShown)
	r = sendRig(t)
	res = r.run(protocol.Step{Launch: "Mail"}, protocol.Step{Click: sel(`button "Send"`)},
		protocol.Step{Expect: &protocol.Expect{Gone: sel(`button "Archive"`)}})
	mustPass(t, res)
}

func TestCaveatsForUnreadActions(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	res := r.run(protocol.Step{Press: sel(`button "Save"`)})
	mustPass(t, res)
	if len(res.Caveats) != 1 || res.Caveats[0].Step != 0 {
		t.Fatalf("%s", jsonOf(res))
	}
	v := "x"
	r.d.Add("cg:1", "f", "textbox", "F", nil, "cg:1")
	r.d.Node("f").Value = &v
	res = r.run(protocol.Step{Press: sel(`button "Save"`)}, protocol.Step{Expect: &protocol.Expect{Reads: sel(`textbox "F"`), Equals: raw(`"x"`)}})
	mustPass(t, res)
	if len(res.Caveats) != 0 {
		t.Fatal(jsonOf(res.Caveats))
	}
}

var _ = view.Norm

// A popup that shows no list when pressed (a macOS NSPopUpButton under a
// synthetic press) is picked by focusing it and typing the option's name;
// a value that updates late still passes.
func TestSetPopupTypeSelects(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	red := "Red"
	r.d.Add("cg:1", "color", "combobox", "Color", fakedesk.Box(10, 70, 100, 20), "cg:1")
	r.d.Node("color").Value = &red
	r.d.onDriver = func(tool string, args map[string]any) {
		if strings.Contains(fmt.Sprint(args), "Blue") {
			blue := "Blue"
			r.d.Node("color").Value = &blue
		}
	}
	res := r.run(protocol.Step{Set: sel(`combobox "Color"`), Value: raw(`"Blue"`)})
	mustPass(t, res)
	if strings.Join(r.d.FocusedIDs, ",") != "color" || len(typeCalls(r.d)) != 1 {
		t.Fatalf("focus %v, type calls %d", r.d.FocusedIDs, len(typeCalls(r.d)))
	}
	if !strings.Contains(res.Steps[0].Evidence, "by typing") {
		t.Errorf("evidence %q", res.Steps[0].Evidence)
	}
}

// A password can be set into a secure field: the step runs, and its value
// never appears in the result or in the remaining steps.
func TestSecretSetRedacted(t *testing.T) {
	r := newRig(t, policy.Config{}).notes()
	r.d.Add("cg:1", "pw", "textbox", "Password", fakedesk.Box(10, 70, 200, 20), "cg:1")
	r.d.Node("pw").States = append(r.d.Node("pw").States, "protected")
	res := r.run(protocol.Step{Set: sel(`secure textbox "Password"`), Value: raw(`"hunter2"`), Secret: true})
	data, _ := json.Marshal(res)
	if strings.Contains(string(data), "hunter2") {
		t.Fatalf("the password shows: %s", data)
	}
	rem := Remaining([]protocol.Step{{Set: sel("Password"), Value: raw(`"hunter2"`), Secret: true, Intent: "log in"}})
	if string(*rem[0].Value) != `"[secret]"` || rem[0].Intent != "" {
		t.Errorf("remaining %+v", rem[0])
	}
}

// A secure field reads back as mask characters: the set is confirmed by
// length, and a mismatch never quotes the password, at any level.
func TestSecretSetReadsBackByLength(t *testing.T) {
	for _, tc := range []struct {
		shown string
		ok    bool
	}{{"•••••••", true}, {"•••", false}} {
		r := newRig(t, policy.Config{}).notes()
		r.d.Add("cg:1", "pw", "textbox", "Password", fakedesk.Box(10, 70, 200, 20), "cg:1")
		r.d.Node("pw").States = append(r.d.Node("pw").States, "protected")
		shown := tc.shown
		r.d.onSet = func(string, string) string { return shown }
		res := r.run(protocol.Step{Set: sel(`secure textbox "Password"`), Value: raw(`"hunter2"`), Secret: true})
		data, _ := json.Marshal(res)
		if strings.Contains(string(data), "hunter2") {
			t.Fatalf("the password shows: %s", data)
		}
		if res.OK != tc.ok {
			t.Errorf("shown %q: ok %v: %s", tc.shown, res.OK, data)
		}
	}
}
