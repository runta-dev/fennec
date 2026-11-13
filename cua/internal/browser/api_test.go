package browser

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

func formManager(t *testing.T) (*fakeBrowser, *Manager, *gateRec) {
	f := newFake()
	f.build = formPage
	m, g := newTestManager(t, f)
	return f, m, g
}

func open(t *testing.T, m *Manager, key any) (binding float64, text string) {
	t.Helper()
	res := mustCall(t, m, key, map[string]any{"op": "createTab", "browserId": "chromium", "url": "shop.test/form", "sessionName": "🔎 Shop"})
	return res["binding"].(float64), res["text"].(string)
}

func TestBrowsersAndState(t *testing.T) {
	f, m, _ := formManager(t)
	v, perr := m.Call(context.Background(), "s", json.RawMessage(`{"op":"browsers"}`))
	if perr != nil {
		t.Fatal(perr)
	}
	data, _ := json.Marshal(v)
	if string(data) != `[{"id":"chromium","name":"Chromium","family":"chromium","type":"cdp"}]` {
		t.Errorf("browsers %s", data)
	}
	// No tab yet: getState lists the browser with no tabs and starts
	// nothing.
	v, _ = m.Call(context.Background(), "s", json.RawMessage(`{"op":"state"}`))
	data, _ = json.Marshal(v)
	if string(data) != `[{"id":"chromium","name":"Chromium","family":"chromium","type":"cdp","tabs":[]}]` || f.starts != 0 {
		t.Errorf("state %s, %d starts", data, f.starts)
	}
	open(t, m, "s")
	v, _ = m.Call(context.Background(), "s", json.RawMessage(`{"op":"state"}`))
	data, _ = json.Marshal(v)
	if !strings.Contains(string(data), `"tabs":[{"id":"1","title":"Form","url":"https://shop.test/form","browserId":"chromium"}]`) {
		t.Errorf("state %s", data)
	}
	// The browser documentation shows once per runtime, again after reset.
	res := mustCall(t, m, "s", map[string]any{"op": "browser", "id": "chrome"})
	if res["doc"] != "" || res["browserId"] != "chromium" {
		t.Errorf("browser %v", res)
	}
	m.Reset("s")
	if res := mustCall(t, m, "s", map[string]any{"op": "browser"}); !strings.HasPrefix(res["doc"].(string), "# Browser: Chromium") {
		t.Errorf("doc after reset %v", res)
	}
	for _, id := range []string{"firefox", "ego", "google chrome"} {
		if _, perr := call(t, m, "s", map[string]any{"op": "browser", "id": id}); perr == nil || !strings.Contains(perr.Message, `Browser "`+id+`" is not available`) {
			t.Errorf("%s: %v", id, perr)
		}
	}
}

func TestCreateTab(t *testing.T) {
	f, m, _ := formManager(t)
	res := mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": "shop.test/form", "sessionName": "🔎 Shop", "visible": true})
	text := res["text"].(string)
	if !strings.HasPrefix(text, "Browser tab: 1, Title: \"Form\", URL: \"https://shop.test/form\".\n0 AXWebArea Form, URL: shop.test/form\n\t1 heading Order, Value: 1") {
		t.Errorf("state:\n%s", text)
	}
	if !strings.HasPrefix(res["doc"].(string), "# Browser: Chromium") || res["id"] != "1" {
		t.Errorf("create %v", res)
	}
	// The first tab opens a new window in the background; the page is
	// loaded through Page.navigate after attaching with lifecycle events.
	if got := f.callsLike("Target.createTarget"); len(got) != 1 || got[0] != `Target.createTarget {"background":true,"newWindow":true,"url":"about:blank"}` {
		t.Errorf("create %v", got)
	}
	if got := f.callsLike("p1 Page."); len(got) < 3 || got[0] != "p1 Page.enable" || got[1] != `p1 Page.setLifecycleEventsEnabled {"enabled":true}` || got[2] != `p1 Page.navigate {"url":"https://shop.test/form"}` {
		t.Errorf("page calls %v", got)
	}
	// A second tab opens in the same window.
	res = mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chrome", "url": "https://shop.test/two"})
	if res["id"] != "2" || res["doc"] != "" || !strings.Contains(res["text"].(string), `Title: "Two"`) {
		t.Errorf("second %v", res)
	}
	if got := f.callsLike("Target.createTarget"); len(got) != 2 || strings.Contains(got[1], "newWindow") {
		t.Errorf("second create %v", got)
	}
	if len(f.pages) != 2 || f.pages[0].window != f.pages[1].window {
		t.Errorf("windows %+v %+v", f.pages[0], f.pages[1])
	}
	for _, bad := range []map[string]any{
		{"op": "createTab"},
		{"op": "createTab", "browserId": ""},
		{"op": "createTab", "browserId": "chromium", "visible": "yes"},
		{"op": "createTab", "browserId": "chromium", "sessionName": 3},
	} {
		if _, perr := call(t, m, "s", bad); perr == nil || perr.Code != protocol.ErrInvalid {
			t.Errorf("%v: %v", bad, perr)
		}
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "createTab", "browserId": ""}); !strings.Contains(perr.Message, "createBrowserTab requires a browser ID. Select one with cua.getBrowser().") {
		t.Errorf("message %q", perr.Message)
	}
	// A failed navigation reports Chromium's error.
	if _, perr := call(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": "unreachable.test"}); perr == nil || perr.Message != "net::ERR_NAME_NOT_RESOLVED at https://unreachable.test" {
		t.Errorf("unreachable %v", perr)
	}
}

// Each session works in its own window; it sees its own tabs, the pages
// they opened and tabs the user opened in its window, never another
// session's.
func TestWindows(t *testing.T) {
	f, m, _ := formManager(t)
	mustCall(t, m, "a", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/form", "sessionName": "A"})
	mustCall(t, m, "b", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/two", "sessionName": "B"})
	if f.pages[0].window == f.pages[1].window {
		t.Fatal("two sessions share a window")
	}
	popup := f.addPage("https://shop.test/two", 9, f.pages[0].targetID) // a popup of A's tab
	f.addPage("https://user.test/", f.pages[0].window, "")              // the user's tab in A's window
	f.addPage("https://elsewhere.test/", 77, "")                        // a tab in a window of no session
	res := mustCall(t, m, "a", map[string]any{"op": "listTabs"})
	data, _ := json.Marshal(res["tabs"])
	if !strings.Contains(string(data), `"url":"https://shop.test/form"`) || !strings.Contains(string(data), `"url":"https://user.test/"`) ||
		strings.Count(string(data), `"id"`) != 3 || strings.Contains(string(data), "elsewhere") {
		t.Errorf("a's tabs %s", data)
	}
	res = mustCall(t, m, "b", map[string]any{"op": "listTabs"})
	data, _ = json.Marshal(res["tabs"])
	if strings.Count(string(data), `"id"`) != 1 {
		t.Errorf("b's tabs %s", data)
	}
	// The popup is an agent tab (closed at the turn's end); the user's is
	// not.
	m.TurnEnded(context.Background(), "a")
	for _, p := range f.pages {
		if p == popup || p.url == "https://shop.test/form" {
			t.Errorf("agent tab %s survived the turn", p.url)
		}
	}
	if f.target(f.pages[len(f.pages)-1].targetID) == nil || len(f.pages) != 3 {
		t.Errorf("pages after the turn: %d", len(f.pages))
	}
	// nameSession names the agent cursor.
	mustCall(t, m, "c", map[string]any{"op": "nameSession", "name": "Named"})
	res = mustCall(t, m, "c", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/form"})
	f.clearCalls()
	mustCall(t, m, "c", map[string]any{"op": "tab", "binding": res["binding"], "method": "click", "args": []any{6}})
	if len(f.cursor) == 0 || !strings.Contains(f.cursor[0], "move 40 155 Named · click button Save") {
		t.Errorf("cursor %v", f.cursor)
	}
}

func TestGetTabAndList(t *testing.T) {
	_, m, _ := formManager(t)
	if _, perr := call(t, m, "s", map[string]any{"op": "getTab", "ref": "1"}); perr == nil || perr.Message != "Tab not found in browser chromium." {
		t.Errorf("no tabs: %v", perr)
	}
	open(t, m, "s")
	mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/form"})
	res := mustCall(t, m, "s", map[string]any{"op": "listTabs", "browser": "chromium"})
	data, _ := json.Marshal(res["tabs"])
	if string(data) != `[{"browserId":"chromium","id":"1","title":"Form","url":"https://shop.test/form"},{"browserId":"chromium","id":"2","title":"Form","url":"https://shop.test/form"}]` {
		t.Errorf("tabs %s", data)
	}
	res = mustCall(t, m, "s", map[string]any{"op": "getTab", "ref": "2", "browser": "chromium"})
	if res["id"] != "2" || !strings.Contains(res["text"].(string), "Browser tab: 2,") {
		t.Errorf("getTab %v", res)
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "getTab", "ref": map[string]any{"url": "https://shop.test/form"}, "browser": "chromium"}); perr == nil || !strings.HasPrefix(perr.Message, "Multiple tabs match the reference in browser chromium: [") {
		t.Errorf("ambiguous: %v", perr)
	}
	cases := map[string]map[string]any{
		"getTab requires a tab reference":               {"op": "getTab", "ref": ""},
		"getTab requires an absolute URL.":              {"op": "getTab", "ref": map[string]any{"url": "shop.test"}, "browser": "chromium"},
		"getTab({ url }) requires an explicit browser.": {"op": "getTab", "ref": map[string]any{"url": "https://shop.test/form"}},
		"Tab mentions are not available in Chromium":    {"op": "getTab", "ref": map[string]any{"mention": "plugin://x"}},
		"Tab not found in browser chromium.":            {"op": "getTab", "ref": "9"},
	}
	for want, req := range cases {
		if _, perr := call(t, m, "s", req); perr == nil || !strings.HasPrefix(perr.Message, want) {
			t.Errorf("%s: %v", want, perr)
		}
	}
}

func joined(list []string) string { return strings.Join(list, "|") }

func TestClickAndPoints(t *testing.T) {
	f, m, g := formManager(t)
	b, text := open(t, m, "s")
	if !strings.Contains(text, "\t6 button Save, ID: save\n") {
		t.Fatalf("state:\n%s", text)
	}
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{6}})
	if got := joined(f.inputs("p1 ")); got != "p1 move 40 155|p1 down left 40 155 x1 mods0|p1 up left 40 155 x1" {
		t.Errorf("click %q", got)
	}
	if len(f.cursor) != 1 || f.cursor[0] != "p1 [move 40 155 🔎 Shop · click button Save]" {
		t.Errorf("cursor %v", f.cursor)
	}
	if !strings.Contains(g.asked[len(g.asked)-1], `"click":{"ref":"element 6"}`) || !strings.HasSuffix(g.asked[len(g.asked)-1], " @ Form") {
		t.Errorf("gate %v", g.asked)
	}
	// Right double click with shift held: the key goes down and up around
	// it, and each press carries the modifier and its click count.
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{6, map[string]any{"mouseButton": "r", "clickCount": 2, "key": "shift"}}})
	if got := joined(f.inputs("p1 ")); got != "p1 key down Shift|p1 move 40 155|p1 down right 40 155 x1 mods8|p1 up right 40 155 x1|p1 down right 40 155 x2 mods8|p1 up right 40 155 x2|p1 key up Shift" {
		t.Errorf("sequence %v", got)
	}
	// A held press: move, then down and up per click.
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{6, map[string]any{"durationMs": 10}}})
	if got := f.inputs("p1 "); len(got) != 3 || !strings.Contains(got[1], "down") || !strings.Contains(got[2], "up") {
		t.Errorf("held %v", got)
	}
	// Points are screenshot pixels: an 800 px shot of a 1000 px viewport
	// scales by 1.25.
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "getScreenshot"})
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "click", "args": []any{[]any{100, 40}}})
	if got := f.inputs("p1 down"); len(got) != 1 || !strings.HasPrefix(got[0], "p1 down left 125 50 ") {
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
	// Drag and scroll.
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "drag", "args": []any{[]any{0, 0}, []any{80, 0}}})
	if got := f.inputs("p1 "); len(got) != 15 || got[1] != "p1 down left 0 0 x1 mods0" || got[13] != "p1 move 100 0" || got[14] != "p1 up left 100 0 x1" {
		t.Errorf("drag %v", got)
	}
	f.clearCalls()
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "scroll", "args": []any{6, "down", 2}})
	if got := joined(f.inputs("p1 ")); got != "p1 move 40 155|p1 wheel 0 1080" {
		t.Errorf("scroll %v", got)
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
	if perr := tab("typeText", 3, "Ad!\né"); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.callsLike("p1 DOM.focus")); !strings.Contains(got, `{"backendNodeId":`) {
		t.Errorf("focus %s", got)
	}
	if got := joined(f.inputs("p1 ")); got != `p1 key down A text="A"|p1 key up A|p1 key down d text="d"|p1 key up d|p1 key down ! text="!"|p1 key up !|p1 key down Enter text="\r" [insertNewline]|p1 key up Enter|p1 insert é` {
		t.Errorf("typeText %s", got)
	}
	f.clearCalls()
	if perr := tab("pressKey", nil, "super+a"); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 ")); got != "p1 key down Meta|p1 key down a [selectAll]|p1 key up a|p1 key up Meta" {
		t.Errorf("pressKey %v", got)
	}
	f.clearCalls()
	if perr := tab("pressKey", 3, "Return", map[string]any{"durationMs": 5}); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 key")); got != `p1 key down Enter text="\r" [insertNewline]|p1 key up Enter` {
		t.Errorf("held key %v", got)
	}
	f.clearCalls()
	if perr := tab("pressKey", nil, "BackSpace"); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 key")); got != "p1 key down Backspace [deleteBackward]|p1 key up Backspace" {
		t.Errorf("backspace %v", got)
	}
	f.clearCalls()
	if perr := tab("paste", nil, "bold x", map[string]any{"format": "html"}); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 ")); got != `p1 paste ["bold x","bold x"]|p1 insert bold x` {
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
	if got := f.callsLike("p1 Runtime.callFunctionOn"); len(got) < 2 || !strings.Contains(got[len(got)-1], `"arguments":[{"value":17},{"value":17}]`) {
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
	if len(f.inputs("p1 down")) != 1 {
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
	f.mu.Lock()
	f.fail["Input.dispatchKeyEvent"] = "typing failed for hunter2-secret"
	f.mu.Unlock()
	_, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "typeText", "args": []any{4, "hunter2-secret"}})
	if perr == nil || strings.Contains(perr.Message, "hunter2-secret") || !strings.Contains(perr.Message, "[text]") {
		t.Errorf("error %v", perr)
	}
	f.mu.Lock()
	delete(f.fail, "Input.dispatchKeyEvent")
	f.mu.Unlock()
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
	if got := f.inputs(""); len(got) != 0 {
		t.Errorf("sent %v", got)
	}
}

// The allowlist refuses every call when Chromium is not admitted; state
// and the browser list show nothing.
func TestAllowlist(t *testing.T) {
	f := newFake()
	m := NewManager(Options{Browser: f, Allowed: func() bool { return false }})
	defer m.CloseAll(context.Background())
	if _, perr := call(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium"}); perr == nil || perr.Code != protocol.ErrNotAllowed {
		t.Errorf("create %v", perr)
	}
	v, _ := m.Call(context.Background(), "s", json.RawMessage(`{"op":"browsers"}`))
	if data, _ := json.Marshal(v); string(data) != "[]" {
		t.Errorf("browsers %s", data)
	}
	if f.starts != 0 {
		t.Error("the browser started")
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
	f.mu.Lock()
	f.delay["Page.navigate"] = time.Second
	f.mu.Unlock()
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
	if _, perr := tab("reload"); perr != nil {
		t.Fatal(perr)
	}
	for _, args := range [][]any{{"load"}, {map[string]any{"state": "networkidle"}}, {"domcontentloaded", map[string]any{"timeoutMs": 100}}} {
		if _, perr := tab("waitForLoadState", args...); perr != nil {
			t.Errorf("waitForLoadState %v: %v", args, perr)
		}
	}
	if _, perr := tab("waitForLoadState", "idle"); perr == nil {
		t.Error("an unknown load state was accepted")
	}
	if _, perr := tab("waitForURL", "**/two"); perr != nil {
		t.Errorf("waitForURL glob %v", perr)
	}
	if _, perr := tab("waitForURL", "https://shop.test/{one,three}", map[string]any{"timeoutMs": 150}); perr == nil || !strings.Contains(perr.Message, "Timeout 150ms exceeded") {
		t.Errorf("waitForURL timeout %v", perr)
	}
	// Hand-off brings the tab to the front for the user.
	if _, perr := tab("markHandoff"); perr != nil {
		t.Fatal(perr)
	}
	if got := f.callsLike("Target.activateTarget"); len(got) != 1 || !strings.Contains(got[0], f.pages[0].targetID) {
		t.Errorf("handoff %v", got)
	}
	if _, perr := tab("getAXState"); perr != nil {
		t.Fatal(perr)
	}
	// Close: the tab and its bindings go.
	res2 := mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/two"})
	b2 := res2["binding"]
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b2, "method": "close"}); perr != nil {
		t.Fatal(perr)
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b2, "method": "getAXState"}); perr == nil || perr.Message != "Tab 2 was closed; get or create a fresh tab from this browser." {
		t.Errorf("closed %v", perr)
	}
	// End of the session: the marked tab stays open, others close.
	mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/two"})
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "markDeliverable"})
	m.Close(context.Background(), "s")
	if len(f.pages) != 1 || f.pages[0].label != "p1" {
		t.Errorf("after close %d pages", len(f.pages))
	}
}

// The end of a turn closes unmarked agent tabs and clears the marks; a
// user's tab is never closed.
func TestTurnEnded(t *testing.T) {
	f, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/two"})
	user := f.addPage("https://user.test/", f.pages[0].window, "")
	user.title = "Mine"
	mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "markDeliverable"})
	mustCall(t, m, "s", map[string]any{"op": "listTabs"})
	m.TurnEnded(context.Background(), "s")
	res := mustCall(t, m, "s", map[string]any{"op": "listTabs"})
	data, _ := json.Marshal(res["tabs"])
	if !strings.Contains(string(data), `"title":"Form"`) || strings.Contains(string(data), `"title":"Two"`) || !strings.Contains(string(data), `"title":"Mine"`) {
		t.Errorf("after the turn %s", data)
	}
	// The user's tab can be bound (attached on first use) but not closed.
	res = mustCall(t, m, "s", map[string]any{"op": "getTab", "ref": "3"})
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": res["binding"], "method": "close"}); perr == nil || perr.Code != protocol.ErrNotAllowed {
		t.Errorf("closing a user tab: %v", perr)
	}
	if got := f.callsLike("Target.attachToTarget"); len(got) != 3 || !strings.Contains(got[2], user.targetID) {
		t.Errorf("attach %v", got)
	}
	// The marks cleared: the session's end closes the formerly kept tab.
	m.Close(context.Background(), "s")
	if len(f.pages) != 1 || f.pages[0] != user {
		t.Errorf("after close %d pages", len(f.pages))
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

// A browser crash: the call in flight fails with advice; the tabs are gone
// with it, and the next tab starts the browser again.
func TestCrashRecovery(t *testing.T) {
	f, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	f.mu.Lock()
	f.delay["Page.reload"] = 300 * time.Millisecond
	f.mu.Unlock()
	go func() { time.Sleep(50 * time.Millisecond); f.crash() }()
	_, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "reload"})
	if perr == nil || !strings.Contains(perr.Message, "Chromium stopped; the action may have been sent") {
		t.Fatalf("in flight %v", perr)
	}
	if _, perr := call(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "getAXState"}); perr == nil || !strings.Contains(perr.Message, "the tab was closed") {
		t.Errorf("old tab after the crash: %v", perr)
	}
	res := mustCall(t, m, "s", map[string]any{"op": "createTab", "browserId": "chromium", "url": "https://shop.test/form"})
	if !strings.Contains(res["text"].(string), "button Save") || f.starts != 2 || res["id"] != "2" {
		t.Errorf("recovery: %d starts, %v", f.starts, res["id"])
	}
	if got := f.callsLike("Target.createTarget"); len(got) != 2 || !strings.Contains(got[1], "newWindow") {
		t.Errorf("a new window after the restart: %v", got)
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
	if got := joined(f.inputs("p1 ")); got != "p1 move 40 155|p1 down left 40 155 x1 mods0|p1 up left 40 155 x1" {
		t.Errorf("click %q", got)
	}
	if !strings.Contains(g.asked[len(g.asked)-1], `"select":"the locator"`) {
		t.Errorf("gate %v", g.asked[len(g.asked)-1])
	}
	f.clearCalls()
	if _, perr := loc("hover", role("button", s("Save"), true)); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 ")); got != "p1 move 40 155" {
		t.Errorf("hover %q", got)
	}
	f.clearCalls()
	if _, perr := loc("fill", []map[string]any{{"kind": "label", "text": s("Name")}}, "Ada"); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 ")); got != "p1 insert Ada" {
		t.Errorf("fill %v", got)
	}
	f.clearCalls()
	if _, perr := loc("clear", []map[string]any{{"kind": "label", "text": s("Name")}}); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 ")); got != "p1 key down Delete [deleteForward]|p1 key up Delete" {
		t.Errorf("clear %v", got)
	}
	f.clearCalls()
	if _, perr := loc("press", []map[string]any{{"kind": "label", "text": s("Name")}}, "Shift+Tab"); perr != nil {
		t.Fatal(perr)
	}
	if got := joined(f.inputs("p1 key")); got != "p1 key down Shift|p1 key down Tab [insertBacktab]|p1 key up Tab|p1 key up Shift" {
		t.Errorf("press %v", got)
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

// Screenshots hide the agent cursor and come at CSS-pixel size.
func TestScreenshot(t *testing.T) {
	f, m, _ := formManager(t)
	b, _ := open(t, m, "s")
	f.clearCalls()
	res := mustCall(t, m, "s", map[string]any{"op": "tab", "binding": b, "method": "screenshot", "args": []any{map[string]any{"fullPage": true}}})
	if res["mime"] != "image/png" || res["data"] == "" {
		t.Errorf("shot %v", res)
	}
	if got := f.callsLike("p1 Page.captureScreenshot"); len(got) != 1 || !strings.Contains(got[0], `"captureBeyondViewport":true`) || !strings.Contains(got[0], `"height":2400`) {
		t.Errorf("capture %v", got)
	}
	if len(f.cursor) != 2 || !strings.Contains(f.cursor[0], "hide") || !strings.Contains(f.cursor[1], "show") {
		t.Errorf("cursor %v", f.cursor)
	}
}
