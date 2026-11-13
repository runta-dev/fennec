package ego

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/protocol"
)

// formPage is the synthetic page the API tests drive.
func formPage(url string) *spec {
	if strings.HasSuffix(url, "/two") {
		return n("RootWebArea", "Two", n("heading", "Two", n("StaticText", "Two")).with("level", 1))
	}
	if url == "about:blank" {
		return n("RootWebArea", "")
	}
	pw := n("textbox", "Password").from("relatedElement").with("settable", true).with("focusable", true).id("pw").at(10, 60, 200, 20)
	pw.password = true
	pw.tag = "input"
	name := n("textbox", "Name").from("relatedElement").with("settable", true).with("focusable", true).id("name").at(10, 30, 200, 20)
	name.tag = "input"
	notes := n("textbox", "Notes").from("attribute").val("hello world hello").with("multiline", true).with("settable", true).id("notes").at(10, 90, 200, 40)
	save := n("button", "Save", n("StaticText", "Save").at(20, 150, 30, 10)).with("focusable", true).id("save").at(10, 140, 60, 30)
	save.tag = "button"
	cancel := n("button", "Cancel", n("StaticText", "Cancel")).with("focusable", true).at(80, 140, 60, 30)
	cancel.tag = "button"
	more := n("button", "More", n("StaticText", "More")).with("hasPopup", "menu").with("expanded", false).at(150, 140, 60, 30)
	agree := n("checkbox", "Agree").with("checked", false).id("agree").at(10, 180, 16, 16)
	hidden := n("button", "Ghost")
	return n("RootWebArea", "Form",
		n("heading", "Order", n("StaticText", "Order")).with("level", 1),
		name, pw, notes, save, cancel, more, agree, hidden,
		n("paragraph", "", n("StaticText", "Saved nothing")).attr("data-testid", "status"),
		n("link", "Next", n("StaticText", "Next")).with("url", "https://shop.test/two").at(10, 220, 40, 14),
	)
}

func formManager(t *testing.T) (*fakeEgo, *Manager, *gateRec) {
	f := newFake()
	f.pages = formPage
	m, g := newTestManager(t, f)
	return f, m, g
}

func open(t *testing.T, m *Manager, key any) (binding float64, text string) {
	t.Helper()
	res := mustCall(t, m, key, map[string]any{"op": "createTab", "browserId": "ego", "url": "shop.test/form", "sessionName": "🔎 Shop"})
	return res["binding"].(float64), res["text"].(string)
}

func TestBrowsersAndState(t *testing.T) {
	_, m, _ := formManager(t)
	v, perr := m.Call(context.Background(), "s", json.RawMessage(`{"op":"browsers"}`))
	if perr != nil {
		t.Fatal(perr)
	}
	data, _ := json.Marshal(v)
	if string(data) != `[{"id":"ego","name":"ego lite","family":"chromium","type":"cdp"}]` {
		t.Errorf("browsers %s", data)
	}
	// No space yet: getState lists the browser with no tabs and starts
	// nothing.
	v, _ = m.Call(context.Background(), "s", json.RawMessage(`{"op":"state"}`))
	data, _ = json.Marshal(v)
	if string(data) != `[{"id":"ego","name":"ego lite","family":"chromium","type":"cdp","tabs":[]}]` {
		t.Errorf("state %s", data)
	}
	open(t, m, "s")
	v, _ = m.Call(context.Background(), "s", json.RawMessage(`{"op":"state"}`))
	data, _ = json.Marshal(v)
	if !strings.Contains(string(data), `"tabs":[{"id":"1","title":"Form","url":"https://shop.test/form","browserId":"ego"}]`) {
		t.Errorf("state %s", data)
	}
	// The browser documentation shows once per runtime, again after reset.
	res := mustCall(t, m, "s", map[string]any{"op": "browser", "id": "chrome"})
	if res["doc"] != "" || res["browserId"] != "ego" {
		t.Errorf("browser %v", res)
	}
	m.Reset("s")
	if res := mustCall(t, m, "s", map[string]any{"op": "browser"}); !strings.HasPrefix(res["doc"].(string), "# Browser: ego lite") {
		t.Errorf("doc after reset %v", res)
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "browser", "id": "firefox"}); perr == nil || !strings.Contains(perr.Message, `Browser "firefox" is not available`) {
		t.Errorf("unknown browser %v", perr)
	}
}

func TestCreateTab(t *testing.T) {
	f, m, _ := formManager(t)
	res := mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "ego", "url": "shop.test/form", "sessionName": "🔎 Shop", "visible": true})
	text := res["text"].(string)
	if !strings.HasPrefix(text, "Browser tab: 1, Title: \"Form\", URL: \"https://shop.test/form\".\n0 AXWebArea Form, URL: shop.test/form\n\t1 heading Order, Value: 1") {
		t.Errorf("state:\n%s", text)
	}
	if !strings.HasPrefix(res["doc"].(string), "# Browser: ego lite") || res["id"] != "1" {
		t.Errorf("create %v", res)
	}
	// The space is named after sessionName and its first page is used.
	if got := f.callsLike("space"); len(got) != 1 || !strings.Contains(got[0], "") {
		t.Errorf("space calls %v", got)
	}
	if sp := f.spaces[1]; sp.name != "🔎 Shop" || len(sp.pages) != 1 {
		t.Errorf("space %+v", sp)
	}
	// A second tab opens a new page in the same space.
	res = mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chrome", "url": "https://shop.test/two"})
	if res["id"] != "2" || res["doc"] != "" || !strings.Contains(res["text"].(string), `Title: "Two"`) {
		t.Errorf("second %v", res)
	}
	if len(f.spaces) != 1 || len(f.spaces[1].pages) != 2 {
		t.Errorf("spaces %d", len(f.spaces))
	}
	for _, bad := range []map[string]any{
		{"op": "createTab"},
		{"op": "createTab", "browserId": ""},
		{"op": "createTab", "browserId": "ego", "visible": "yes"},
		{"op": "createTab", "browserId": "ego", "sessionName": 3},
	} {
		if _, perr := call(t, m, "s", bad); perr == nil || perr.Code != protocol.ErrInvalid {
			t.Errorf("%v: %v", bad, perr)
		}
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "createTab", "browserId": ""}); !strings.Contains(perr.Message, "createBrowserTab requires a browser ID. Select one with cua.getBrowser().") {
		t.Errorf("message %q", perr.Message)
	}
}

// A name a user space holds is never taken; another session's name is not
// shared.
func TestSpaceNames(t *testing.T) {
	f, m, _ := formManager(t)
	f.userNames["Research"] = true
	mustCall(t, m, "a", map[string]any{"op": "createTab", "browserId": "ego", "sessionName": "Research"})
	mustCall(t, m, "b", map[string]any{"op": "createTab", "browserId": "ego", "sessionName": "Research"})
	names := map[string]bool{}
	for _, sp := range f.spaces {
		names[sp.name] = true
		if sp.ownership != "agent" {
			t.Errorf("space %+v", sp)
		}
	}
	if names["Research"] || len(names) != 2 {
		t.Errorf("names %v", names)
	}
	// nameSession before the first tab names the space.
	mustCall(t, m, "c", map[string]any{"op": "nameSession", "name": "Named"})
	mustCall(t, m, "c", map[string]any{"op": "createTab", "browserId": "ego"})
	found := false
	for _, sp := range f.spaces {
		found = found || sp.name == "Named"
	}
	if !found {
		t.Error("nameSession was not used")
	}
}

func TestGetTabAndList(t *testing.T) {
	_, m, _ := formManager(t)
	if _, perr := call(t, m, "s", map[string]any{"op": "getTab", "ref": "1"}); perr == nil || perr.Message != "Tab not found in browser ego." {
		t.Errorf("no space: %v", perr)
	}
	open(t, m, "s")
	mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "ego", "url": "https://shop.test/form"})
	res := mustCall(t, m, "s", map[string]any{"op": "listTabs", "browser": "ego"})
	data, _ := json.Marshal(res["tabs"])
	if string(data) != `[{"browserId":"ego","id":"1","title":"Form","url":"https://shop.test/form"},{"browserId":"ego","id":"2","title":"Form","url":"https://shop.test/form"}]` {
		t.Errorf("tabs %s", data)
	}
	res = mustCall(t, m, "s", map[string]any{"op": "getTab", "ref": "2", "browser": "ego"})
	if res["id"] != "2" || !strings.Contains(res["text"].(string), "Browser tab: 2,") {
		t.Errorf("getTab %v", res)
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "getTab", "ref": map[string]any{"url": "https://shop.test/form"}, "browser": "ego"}); perr == nil || !strings.HasPrefix(perr.Message, "Multiple tabs match the reference in browser ego: [") {
		t.Errorf("ambiguous: %v", perr)
	}
	cases := map[string]map[string]any{
		"getTab requires a tab reference":               {"op": "getTab", "ref": ""},
		"getTab requires an absolute URL.":              {"op": "getTab", "ref": map[string]any{"url": "shop.test"}, "browser": "ego"},
		"getTab({ url }) requires an explicit browser.": {"op": "getTab", "ref": map[string]any{"url": "https://shop.test/form"}},
		"Tab mentions are not available in ego lite":    {"op": "getTab", "ref": map[string]any{"mention": "plugin://x"}},
		"Tab not found in browser ego.":                 {"op": "getTab", "ref": "9"},
	}
	for want, req := range cases {
		if _, perr := call(t, m, "s", req); perr == nil || !strings.HasPrefix(perr.Message, want) {
			t.Errorf("%s: %v", want, perr)
		}
	}
}

func centre(calls []string) string {
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1]
}

func TestClickAndPoints(t *testing.T) {
	f, m, g := formManager(t)
	b, text := open(t, m, "s")
	if !strings.Contains(text, "\t6 button Save, ID: save\n") {
		t.Fatalf("state:\n%s", text)
	}
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{6}})
	if got := centre(f.callsLike("call p1 mouse.click")); got != `call p1 mouse.click 40 155 {"button":"left","clickCount":1,"label":"click button Save"}` {
		t.Errorf("click %q", got)
	}
	if !strings.Contains(g.asked[len(g.asked)-1], `"click":{"ref":"element 6"}`) || !strings.HasSuffix(g.asked[len(g.asked)-1], " @ Form") {
		t.Errorf("gate %v", g.asked)
	}
	// Right double click with shift held: keys go down and up around it.
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{6, map[string]any{"mouseButton": "r", "clickCount": 2, "key": "shift"}}})
	var seq []string
	for _, c := range f.callsLike("call p1 ") {
		if strings.Contains(c, "keyboard.") || strings.Contains(c, "mouse.") {
			seq = append(seq, c)
		}
	}
	if strings.Join(seq, "|") != `call p1 keyboard.down "Shift"|call p1 mouse.click 40 155 {"button":"right","clickCount":2,"label":"click button Save"}|call p1 keyboard.up "Shift"` {
		t.Errorf("sequence %v", seq)
	}
	// A held press: move, then down and up per click.
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{6, map[string]any{"durationMs": 10}}})
	if got := f.callsLike("call p1 mouse."); len(got) != 3 || !strings.Contains(got[1], "mouse.down") || !strings.Contains(got[2], "mouse.up") {
		t.Errorf("held %v", got)
	}
	// Points are screenshot pixels: an 800 px shot of a 1000 px viewport
	// scales by 1.25.
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "getScreenshot"})
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{[]any{100, 40}}})
	if got := centre(f.callsLike("call p1 mouse.click")); !strings.HasPrefix(got, "call p1 mouse.click 125 50 ") {
		t.Errorf("point %q", got)
	}
	errs := map[string][]any{
		"element 99 is not in the latest state of tab 1; call getAXState() again": {99},
		"an element id is a whole number":                                         {1.5},
		"a point is [x, y]":                                                       {[]any{1}},
	}
	for want, args := range errs {
		if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": args}); perr == nil || !strings.Contains(perr.Message, want) {
			t.Errorf("%s: %v", want, perr)
		}
	}
	for want, o := range map[string]map[string]any{
		"durationMs must be an integer from 0 to 30000 milliseconds.": {"durationMs": 40000},
		"mouseButton is left, right or middle":                        {"mouseButton": "x"},
	} {
		if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{6, o}}); perr == nil || !strings.Contains(perr.Message, want) {
			t.Errorf("%s: %v", want, perr)
		}
	}
	// A hidden element has no box.
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{13}}); perr == nil || !strings.Contains(perr.Message, "no box") {
		t.Errorf("hidden: %v", perr)
	}
}

func TestInputMethods(t *testing.T) {
	f, m, g := formManager(t)
	b, _ := open(t, m, "s")
	tab := func(method string, args ...any) *protocol.Error {
		_, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": method, "args": args})
		return perr
	}
	f.clearCalls()
	if perr := tab("typeText", 3, "Ada"); perr != nil {
		t.Fatal(perr)
	}
	if got := strings.Join(f.callsLike("call p1 "), "|"); !strings.Contains(got, `cdp "DOM.focus" {"backendNodeId":`) || !strings.HasSuffix(got, `call p1 keyboard.type "Ada"`) {
		t.Errorf("typeText %s", got)
	}
	f.clearCalls()
	if perr := tab("pressKey", nil, "super+a"); perr != nil {
		t.Fatal(perr)
	}
	if got := f.callsLike("call p1 keyboard."); len(got) != 1 || got[0] != `call p1 keyboard.press "Meta+a"` {
		t.Errorf("pressKey %v", got)
	}
	f.clearCalls()
	if perr := tab("pressKey", 3, "Return", map[string]any{"durationMs": 5}); perr != nil {
		t.Fatal(perr)
	}
	if got := f.callsLike("call p1 keyboard."); strings.Join(got, "|") != `call p1 keyboard.down "Enter"|call p1 keyboard.up "Enter"` {
		t.Errorf("held key %v", got)
	}
	f.clearCalls()
	if perr := tab("paste", nil, "bold x", map[string]any{"format": "html"}); perr != nil {
		t.Fatal(perr)
	}
	if got := f.callsLike("call p1 keyboard.paste"); len(got) != 1 || got[0] != `call p1 keyboard.paste {"html":"bold x","text":"bold x"}` {
		t.Errorf("paste %v", got)
	}
	for want, args := range map[string][]any{
		indexError:                   {-1, "x"},
		"format is text, md or html": {nil, "x", map[string]any{"format": "rtf"}},
		"unknown key":                {nil, "Hyper_Q"},
	} {
		method := "typeText"
		if len(args) == 3 {
			method = "paste"
		}
		if strings.Contains(want, "key") {
			method = "pressKey"
		}
		if perr := tab(method, args...); perr == nil || !strings.Contains(perr.Message, want) {
			t.Errorf("%s: %v", want, perr)
		}
	}
	// setValue, selectText and secondary actions.
	if perr := tab("setValue", 3, "Grace"); perr != nil {
		t.Fatal(perr)
	}
	if perr := tab("setValue", 6, "x"); perr == nil || !strings.Contains(perr.Message, "takes no value") {
		t.Errorf("setValue on a button: %v", perr)
	}
	if perr := tab("selectText", 5, "hello"); perr == nil || !strings.Contains(perr.Message, `"hello" appears 2 times in element 5; pass prefix or suffix`) {
		t.Errorf("ambiguous selection: %v", perr)
	}
	f.clearCalls()
	if perr := tab("selectText", 5, "hello", map[string]any{"prefix": "world ", "selectionType": "cursor_after"}); perr != nil {
		t.Fatal(perr)
	}
	if got := f.callsLike("call p1 cdp \"Runtime.callFunctionOn\""); len(got) < 2 || !strings.Contains(got[len(got)-1], `"arguments":[{"value":17},{"value":17}]`) {
		t.Errorf("selection %v", got)
	}
	if perr := tab("selectText", 5, "nope"); perr == nil || !strings.Contains(perr.Message, `"nope" is not in element 5`) {
		t.Errorf("missing text: %v", perr)
	}
	if perr := tab("performSecondaryAction", 10, "Raise"); perr == nil || perr.Message != `element 10 has no secondary action "Raise"; it lists: Expand` {
		t.Errorf("unlisted action: %v", perr)
	}
	f.clearCalls()
	if perr := tab("performSecondaryAction", 10, "expand"); perr != nil {
		t.Fatal(perr)
	}
	if len(f.callsLike("call p1 mouse.click")) != 1 {
		t.Error("Expand did not click")
	}
	// Every input step asked the gate first.
	if len(g.asked) < 8 {
		t.Errorf("gate asked %d times", len(g.asked))
	}
}

// Text typed into a password field is masked in the confirm question and
// never repeated by an error.
func TestSecrets(t *testing.T) {
	f, m, g := formManager(t)
	b, _ := open(t, m, "s")
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "typeText", "args": []any{4, "hunter2-secret"}}); perr != nil {
		t.Fatal(perr)
	}
	last := g.asked[len(g.asked)-1]
	if !strings.Contains(last, `"secret":true`) {
		t.Errorf("not marked secret: %s", last)
	}
	f.fail["keyboard.type"] = "typing failed for hunter2-secret"
	_, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "typeText", "args": []any{4, "hunter2-secret"}})
	if perr == nil || strings.Contains(perr.Message, "hunter2-secret") || !strings.Contains(perr.Message, "[text]") {
		t.Errorf("error %v", perr)
	}
	delete(f.fail, "keyboard.type")
	// setValue on the password field: the question shows dots.
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "setValue", "args": []any{4, "hunter2-secret"}}); perr != nil {
		t.Fatal(perr)
	}
	if last := g.asked[len(g.asked)-1]; strings.Contains(last, "hunter2") {
		t.Errorf("question shows the secret: %s", last)
	}
}

// A declined confirmation sends nothing.
func TestDeclined(t *testing.T) {
	f, m, g := formManager(t)
	b, _ := open(t, m, "s")
	g.deny = true
	f.clearCalls()
	for _, method := range []string{"click", "typeText"} {
		args := []any{5}
		if method == "typeText" {
			args = []any{nil, "x"}
		}
		if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": method, "args": args}); perr == nil || perr.Code != protocol.ErrDeclined {
			t.Errorf("%s: %v", method, perr)
		}
	}
	if got := f.callsLike("call p1 mouse."); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
	if got := f.callsLike("call p1 keyboard."); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}

// The allowlist refuses every call when ego lite is not admitted; state
// and the browser list show nothing.
func TestAllowlist(t *testing.T) {
	f := newFake()
	m := NewManager(Options{Dir: t.TempDir(), Start: f.starter(), Allowed: func() bool { return false }})
	defer m.CloseAll(context.Background())
	if _, perr := call(t, m, "s", map[string]any{"op": "createTab", "browserId": "ego"}); perr == nil || perr.Code != protocol.ErrNotAllowed {
		t.Errorf("create %v", perr)
	}
	v, _ := m.Call(context.Background(), "s", json.RawMessage(`{"op":"browsers"}`))
	if data, _ := json.Marshal(v); string(data) != "[]" {
		t.Errorf("browsers %s", data)
	}
	if f.starts != 0 {
		t.Error("a bridge started")
	}
	off := NewManager(Options{})
	if _, perr := call(t, off, "s", map[string]any{"op": "browser"}); perr == nil || perr.Message != "No browser is available." {
		t.Errorf("not installed %v", perr)
	}
}

// Stop: a cancelled call answers stopped at once.
func TestStop(t *testing.T) {
	f, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	f.delay["goto"] = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, perr := callCtx(ctx, t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "goto", "args": []any{"https://shop.test/two"}})
	if perr == nil || perr.Code != protocol.ErrStopped || time.Since(start) > 500*time.Millisecond {
		t.Errorf("%v after %v", perr, time.Since(start))
	}
	if _, perr := callCtx(ctx, t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "reload"}); perr == nil || perr.Code != protocol.ErrStopped {
		t.Errorf("after stop: %v", perr)
	}
}

func TestNavigationAndMarks(t *testing.T) {
	f, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	tab := func(method string, args ...any) (map[string]any, *protocol.Error) {
		return call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": method, "args": args})
	}
	if _, perr := tab("forward"); perr == nil || perr.Message != "this tab has no later page in its history" {
		t.Errorf("forward %v", perr)
	}
	if _, perr := tab("goto", "https://shop.test/two"); perr != nil {
		t.Fatal(perr)
	}
	if res, _ := tab("title"); res["value"] != "Two" {
		t.Errorf("title %v", res)
	}
	if res, _ := tab("url"); res["value"] != "https://shop.test/two" {
		t.Errorf("url %v", res)
	}
	if _, perr := tab("back"); perr != nil {
		t.Fatal(perr)
	}
	res, _ := tab("getAXState")
	if !strings.Contains(res["text"].(string), `Title: "Form"`) {
		t.Errorf("after back %v", res["text"])
	}
	if _, perr := tab("forward"); perr != nil {
		t.Fatal(perr)
	}
	// Hand-off gives the space to the user; the next call takes it back.
	if _, perr := tab("markHandoff"); perr != nil {
		t.Fatal(perr)
	}
	if f.spaces[1].ownership != "agentDelegatedToUser" {
		t.Error("not handed off")
	}
	if _, perr := tab("getAXState"); perr != nil {
		t.Fatal(perr)
	}
	if f.spaces[1].ownership != "agent" || len(f.callsLike("takeover")) != 1 {
		t.Error("not taken back")
	}
	// Close: the tab and its bindings go.
	res2 := mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "ego", "url": "https://shop.test/two"})
	b2 := res2["binding"]
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b2, "method": "close"}); perr != nil {
		t.Fatal(perr)
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b2, "method": "getAXState"}); perr == nil || perr.Message != "Tab 2 was closed; get or create a fresh tab from this browser." {
		t.Errorf("closed %v", perr)
	}
	// End of the session: finish keeps the marked tab only.
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "markDeliverable"})
	m.Close(context.Background(), "s")
	if sp := f.spaces[1]; !sp.done || strings.Join(sp.finished, ",") != "p1" {
		t.Errorf("finish %+v", sp)
	}
}

// The end of a turn closes unmarked agent tabs and clears the marks; a
// user's tab is never closed.
func TestTurnEnded(t *testing.T) {
	f, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "ego", "url": "https://shop.test/two"})
	f.mu.Lock()
	user := &fakePage{label: "", targetID: "TUSER", url: "https://user.test/", title: "Mine", openedBy: "user"}
	user.load(n("RootWebArea", "Mine"))
	f.spaces[1].pages = append(f.spaces[1].pages, user)
	f.mu.Unlock()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "markDeliverable"})
	mustCall(t, m, "s", map[string]any{"op": "listTabs"})
	m.TurnEnded(context.Background(), "s")
	res := mustCall(t, m, "s", map[string]any{"op": "listTabs"})
	data, _ := json.Marshal(res["tabs"])
	if !strings.Contains(string(data), `"title":"Form"`) || strings.Contains(string(data), `"title":"Two"`) || !strings.Contains(string(data), `"title":"Mine"`) {
		t.Errorf("after the turn %s", data)
	}
	// The user's tab can be bound (adopted) but not closed.
	res = mustCall(t, m, "s", map[string]any{"op": "getTab", "ref": "3"})
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": res["binding"], "method": "close"}); perr == nil || perr.Code != protocol.ErrNotAllowed {
		t.Errorf("closing a user tab: %v", perr)
	}
	if len(f.callsLike("adopt")) != 1 {
		t.Errorf("adopt %v", f.callsLike("adopt"))
	}
	m.Close(context.Background(), "s")
	if sp := f.spaces[1]; len(sp.finished) != 0 {
		t.Errorf("marks survived the turn: %v", sp.finished)
	}
}

// js_reset drops bindings, keeps tabs and their ids.
func TestReset(t *testing.T) {
	_, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	m.Reset("s")
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "getAXState"}); perr == nil || !strings.Contains(perr.Message, "runtime that was reset") {
		t.Errorf("old binding: %v", perr)
	}
	res := mustCall(t, m, "s", map[string]any{"op": "getTab", "ref": "1"})
	if res["id"] != "1" || !strings.HasPrefix(res["doc"].(string), "# Browser") {
		t.Errorf("after reset %v", res)
	}
}

// A bridge crash: the call in flight fails with advice; the next one
// restarts the bridge and reopens the space.
func TestCrashRecovery(t *testing.T) {
	f, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	f.delay["reload"] = 300 * time.Millisecond
	go func() { time.Sleep(50 * time.Millisecond); f.crash() }()
	_, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "reload"})
	if perr == nil || !strings.Contains(perr.Message, "the ego lite bridge stopped; the action may have been sent") {
		t.Fatalf("in flight %v", perr)
	}
	res := mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "getAXState", "args": []any{map[string]any{"disableDiffing": true}}})
	if !strings.Contains(res["text"].(string), "button Save") || len(f.callsLike("resume")) != 1 || f.starts != 2 {
		t.Errorf("recovery: %d starts, resume %v", f.starts, f.callsLike("resume"))
	}
}

func TestLocators(t *testing.T) {
	f, m, g := formManager(t)
	b, _ := open(t, m, "s")
	loc := func(method string, chain []map[string]any, args ...any) (map[string]any, *protocol.Error) {
		return call(t, m, "s", map[string]any{"op": "loc", "binding": b, "method": method, "chain": chain, "desc": "the locator", "args": args})
	}
	s := func(v string) map[string]any { return map[string]any{"s": v} }
	role := func(r string, name any, exact bool) []map[string]any {
		st := map[string]any{"kind": "role", "role": r, "exact": exact}
		if name != nil {
			st["name"] = name
		}
		return []map[string]any{st}
	}
	count := func(chain []map[string]any) float64 {
		t.Helper()
		res, perr := loc("count", chain)
		if perr != nil {
			t.Fatal(perr)
		}
		return res["value"].(float64)
	}
	if got := count(role("button", nil, false)); got != 4 {
		t.Errorf("buttons %v", got)
	}
	if got := count(role("button", s("sav"), false)); got != 1 {
		t.Errorf("substring %v", got)
	}
	if got := count(role("button", s("sav"), true)); got != 0 {
		t.Errorf("exact %v", got)
	}
	if got := count(role("button", map[string]any{"source": "^(Save|Cancel)$"}, false)); got != 2 {
		t.Errorf("regexp %v", got)
	}
	if got := count([]map[string]any{{"kind": "text", "text": s("saved NOTHING")}}); got != 1 {
		t.Errorf("text %v", got)
	}
	if got := count([]map[string]any{{"kind": "css", "selector": "#save"}}); got != 1 {
		t.Errorf("css %v", got)
	}
	if got := count([]map[string]any{{"kind": "testid", "text": s("status")}}); got != 1 {
		t.Errorf("testid %v", got)
	}
	if got := count([]map[string]any{{"kind": "label", "text": s("Name")}}); got != 1 {
		t.Errorf("label %v", got)
	}
	if got := count(append(role("button", nil, false), map[string]any{"kind": "nth", "n": -1})); got != 1 {
		t.Errorf("last %v", got)
	}
	if got := count(append(role("button", nil, false), map[string]any{"kind": "filter", "hasText": s("Cancel")})); got != 1 {
		t.Errorf("filter %v", got)
	}
	// Strictness and waiting.
	if _, perr := loc("click", role("button", nil, false)); perr == nil || perr.Message != "strict mode violation: the locator resolved to 4 elements" {
		t.Errorf("strict %v", perr)
	}
	if _, perr := loc("click", role("button", s("Nope"), false), map[string]any{"timeoutMs": 200}); perr == nil || perr.Message != "Timeout 200ms exceeded waiting for the locator" {
		t.Errorf("timeout %v", perr)
	}
	f.clearCalls()
	if _, perr := loc("click", role("button", s("Save"), true)); perr != nil {
		t.Fatal(perr)
	}
	if got := centre(f.callsLike("call p1 mouse.click")); got != `call p1 mouse.click 40 155 {"button":"left","clickCount":1,"label":"click button Save"}` {
		t.Errorf("click %q", got)
	}
	if !strings.Contains(g.asked[len(g.asked)-1], `"select":"the locator"`) {
		t.Errorf("gate %v", g.asked[len(g.asked)-1])
	}
	f.clearCalls()
	if _, perr := loc("fill", []map[string]any{{"kind": "label", "text": s("Name")}}, "Ada"); perr != nil {
		t.Fatal(perr)
	}
	if got := f.callsLike("call p1 keyboard."); len(got) != 1 || got[0] != `call p1 keyboard.insertText "Ada"` {
		t.Errorf("fill %v", got)
	}
	f.clearCalls()
	if _, perr := loc("check", role("checkbox", s("Agree"), false)); perr == nil || !strings.Contains(perr.Message, "did not change its checked state") {
		// The fake page does not toggle, so check reports it.
		t.Errorf("check %v", perr)
	}
	if res, perr := loc("innerText", role("button", s("Save"), false)); perr != nil || res["value"] != "Save" {
		t.Errorf("innerText %v %v", res, perr)
	}
	if _, perr := loc("count", []map[string]any{{"kind": "css", "selector": "internal:role=button"}}); perr == nil {
		t.Error("an internal selector was accepted")
	}
	if _, perr := loc("count", role("button", map[string]any{"source": "(?<=x)"}, false)); perr == nil || !strings.Contains(perr.Message, "not supported here") {
		t.Errorf("lookbehind %v", perr)
	}
	res := mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "domSnapshot"})
	if !strings.Contains(res["text"].(string), `- button "Save"`) {
		t.Errorf("domSnapshot %v", res["text"])
	}
	res = mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "evaluate", "args": []any{"() => 6 * 7", true, nil}})
	if res["value"].(float64) != 42 {
		t.Errorf("evaluate %v", res)
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "evaluate", "args": []any{"() => { throw new Error('boom') }", true, nil}}); perr == nil || perr.Message != "Error: boom" {
		t.Errorf("evaluate error %v", perr)
	}
}
