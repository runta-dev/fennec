package engine_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"fennec/cua/internal/protocol"
)

// fakeBrowsers stands in for internal/browser behind the REPL: it records each
// call and answers canned results, so these tests check the cua surface
// (display, return values, errors) on its own.
type fakeBrowsers struct {
	mu     sync.Mutex
	calls  []map[string]any
	resets int
	turns  int
	doc    bool
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n0000000000000000")

func (f *fakeBrowsers) Call(ctx context.Context, key any, args json.RawMessage) (any, *protocol.Error) {
	var req map[string]any
	_ = json.Unmarshal(args, &req)
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	doc := ""
	if !f.doc {
		doc = "# Browser: Chromium\n(doc)"
	}
	switch req["op"] {
	case "browsers":
		return []map[string]any{{"id": "chromium", "name": "Chromium", "family": "chromium", "type": "cdp"}}, nil
	case "state":
		return []map[string]any{{"id": "chromium", "name": "Chromium", "family": "chromium", "type": "cdp", "tabs": []map[string]any{{"id": "1", "title": "T", "url": "https://x.test/", "browserId": "chromium"}}}}, nil
	case "guide":
		return "# Browser: Chromium\n(doc)", nil
	case "chromeProfiles":
		return []map[string]any{{"dir": "Default", "name": "Me", "lastUsed": true}}, nil
	case "importChrome":
		return map[string]any{"profile": map[string]any{"dir": "Default"}, "cookies": map[string]any{"found": 3, "imported": 3}}, nil
	case "browser":
		f.doc = true
		return map[string]any{"browserId": "chromium", "name": "Chromium", "doc": doc}, nil
	case "createTab", "getTab":
		if req["ref"] == "9" {
			return nil, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: "Tab not found in browser chromium."}
		}
		f.doc = true
		return map[string]any{"id": "1", "binding": 1, "doc": doc, "text": "Browser tab: 1, Title: \"T\", URL: \"https://x.test/\".\n0 AXWebArea T, URL: x.test/\n\nThe focused UI element is 0 AXWebArea T, URL: x.test/"}, nil
	case "listTabs":
		d := ""
		if req["browser"] != nil {
			d = doc
			f.doc = true
		}
		return map[string]any{"doc": d, "tabs": []map[string]any{{"id": "1", "title": "T", "url": "https://x.test/", "browserId": "chromium"}}}, nil
	case "tab":
		switch req["method"] {
		case "getAXState":
			return map[string]any{"text": "Browser tab: 1, Title: \"T\", URL: \"https://x.test/\".\nThe accessibility tree has not changed since the previous read."}, nil
		case "getScreenshot", "screenshot":
			return map[string]any{"data": base64.StdEncoding.EncodeToString(pngBytes), "mime": "image/png"}, nil
		case "getAXStateAndScreenshot":
			return map[string]any{"text": "STATE", "data": base64.StdEncoding.EncodeToString(pngBytes), "mime": "image/png"}, nil
		case "domSnapshot":
			return map[string]any{"text": "- button \"Go\""}, nil
		case "title":
			return "T", nil
		case "evaluate":
			return map[string]any{"value": 42}, nil
		case "click":
			return nil, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: "element 99 is not in the latest state of tab 1; call getAXState() again and use an id from it"}
		}
		return nil, nil
	case "loc":
		if req["method"] == "count" {
			return 2, nil
		}
		return nil, nil
	}
	return nil, nil
}

func (f *fakeBrowsers) Reset(any)                      { f.mu.Lock(); f.resets++; f.doc = false; f.mu.Unlock() }
func (f *fakeBrowsers) TurnEnded(context.Context, any) { f.mu.Lock(); f.turns++; f.mu.Unlock() }
func (f *fakeBrowsers) Close(context.Context, any)     {}
func (f *fakeBrowsers) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}
func (f *fakeBrowsers) lastJSON() string { data, _ := json.Marshal(f.last()); return string(data) }

// images lists a result's image blocks.
func (r result) images() []block {
	var out []block
	for _, b := range r.blocks {
		if b.Type == "image" {
			out = append(out, b)
		}
	}
	return out
}

func browserSession(t *testing.T) (*mcp, *fakeBrowsers) {
	e := newWorld().engine()
	fb := &fakeBrowsers{}
	e.Browsers = fb
	c := session(t, e)
	c.ok(`1`)
	return c, fb
}

// CDX-24: discovery: listBrowsers, getBrowser (documentation once, not
// the object), getState().browsers filled, listTabs as compact JSON.
func TestCDX24BrowserDiscovery(t *testing.T) {
	c, fb := browserSession(t)
	if out := c.ok(`await cua.listBrowsers()`); out != `[{"id":"chromium","name":"Chromium","family":"chromium","type":"cdp"}]` {
		t.Errorf("listBrowsers %q", out)
	}
	if out := c.ok(`await cua.listBrowsers({ emit: false })`); out != "" {
		t.Errorf("emit false %q", out)
	}
	if out := c.ok(`var b = await cua.getBrowser({ id: "chrome" }); nodeRepl.write("|" + b.browserId)`); out != "# Browser: Chromium\n(doc)\n|chromium" {
		t.Errorf("getBrowser %q", out)
	}
	if fb.last()["id"] != "chrome" {
		t.Errorf("id %v", fb.last())
	}
	if out := c.ok(`await cua.getBrowser()`); out != "" {
		t.Errorf("documentation twice %q", out)
	}
	if out := c.ok(`nodeRepl.write(await b.documentation())`); !strings.HasPrefix(out, "# Browser: Chromium") {
		t.Errorf("documentation() %q", out)
	}
	out := c.ok(`var st = await cua.getState({ emit: false }); nodeRepl.write(JSON.stringify(st.browsers))`)
	var states []struct {
		ID   string `json:"id"`
		Tabs []struct {
			ID, URL string
		} `json:"tabs"`
	}
	if err := json.Unmarshal([]byte(out), &states); err != nil || len(states) != 1 || states[0].ID != "chromium" || len(states[0].Tabs) != 1 || states[0].Tabs[0].ID != "1" {
		t.Errorf("getState %q", out)
	}
	if out := c.ok(`await cua.listTabs()`); out != `[{"id":"1","title":"T","url":"https://x.test/","browserId":"chromium"}]` {
		t.Errorf("listTabs %q", out)
	}
}

// CDX-25: createBrowserTab / getTab bind a tab and display its state (the
// documentation first, once per runtime); errors read without codes.
func TestCDX25Tabs(t *testing.T) {
	c, fb := browserSession(t)
	out := c.ok(`var tab = await cua.createBrowserTab("chromium", "x.test", { sessionName: "🔎 Test", visible: false })`)
	if out != "# Browser: Chromium\n(doc)\nBrowser tab: 1, Title: \"T\", URL: \"https://x.test/\".\n0 AXWebArea T, URL: x.test/\n\nThe focused UI element is 0 AXWebArea T, URL: x.test/" {
		t.Errorf("create %q", out)
	}
	if got := fb.lastJSON(); got != `{"browserId":"chromium","op":"createTab","sessionName":"🔎 Test","url":"x.test","visible":false}` {
		t.Errorf("args %s", got)
	}
	if out := c.ok(`nodeRepl.write(tab.id + " " + JSON.stringify(tab))`); out != `1 {"id":"1"}` {
		t.Errorf("id %q", out)
	}
	if out := c.ok(`var t2 = await cua.getTab("1", { browser: "chromium" })`); !strings.HasPrefix(out, "Browser tab: 1,") {
		t.Errorf("getTab %q", out)
	}
	r := c.js(`await cua.getTab("9")`)
	if !r.isError || !strings.Contains(r.all(), "\nTab not found in browser chromium.") || strings.Contains(r.all(), "target_not_found") {
		t.Errorf("error %q", r.all())
	}
	// js_reset forgets bindings; the documentation shows again.
	c.call("js_reset", map[string]any{})
	if fb.resets != 1 {
		t.Error("reset not passed on")
	}
	c.ok(`1`)
	if out := c.ok(`var t3 = await cua.getTab({ url: "https://x.test/" }, { browser: "chromium" })`); !strings.HasPrefix(out, "# Browser: Chromium") {
		t.Errorf("after reset %q", out)
	}
	c.call("turn_ended", map[string]any{})
	if fb.turns != 1 {
		t.Error("turn end not passed on")
	}
}

// CDX-26: tab observation displays itself once; emit: false keeps it quiet.
func TestCDX26TabObservation(t *testing.T) {
	c, _ := browserSession(t)
	c.ok(`var tab = await cua.createBrowserTab("chromium")`)
	if out := c.ok(`await tab.getAXState()`); !strings.HasSuffix(out, "has not changed since the previous read.") {
		t.Errorf("state %q", out)
	}
	if out := c.ok(`var s = await tab.getAXState({ emit: false })`); out != "" {
		t.Errorf("quiet %q", out)
	}
	r := c.js(`var shot = await tab.getScreenshot(); nodeRepl.write(String(shot.length))`)
	if r.isError || len(r.images()) != 1 || !strings.Contains(r.all(), "24") {
		t.Errorf("screenshot %q (%d images)", r.all(), len(r.images()))
	}
	r = c.js(`var both = await tab.getAXStateAndScreenshot(); nodeRepl.write("|" + both.state + "|" + both.screenshot.length)`)
	if !strings.Contains(r.all(), "STATE") || !strings.Contains(r.all(), "|STATE|24") || len(r.images()) != 1 {
		t.Errorf("both %q", r.all())
	}
	if r := c.js(`var raw = await tab.screenshot({})`); len(r.images()) != 0 {
		t.Error("screenshot() displayed")
	}
	if out := c.ok(`nodeRepl.write(await tab.playwright.domSnapshot())`); out != `- button "Go"` {
		t.Errorf("domSnapshot %q", out)
	}
}

// CDX-27: input methods pass element-index-first arguments; a bad index
// fails before anything is sent; locators carry their chain.
func TestCDX27TabInput(t *testing.T) {
	c, fb := browserSession(t)
	c.ok(`var tab = await cua.createBrowserTab("chromium")`)
	c.ok(`await tab.typeText(4, "hi")`)
	if got := fb.lastJSON(); got != `{"args":[4,"hi"],"binding":1,"method":"typeText","op":"tab"}` {
		t.Errorf("typeText %s", got)
	}
	c.ok(`await tab.pressKey(null, "Return", { durationMs: 50 })`)
	if got := fb.lastJSON(); got != `{"args":[null,"Return",{"durationMs":50}],"binding":1,"method":"pressKey","op":"tab"}` {
		t.Errorf("pressKey %s", got)
	}
	n := len(fb.calls)
	for _, code := range []string{`await tab.typeText(1.5, "x")`, `await tab.pressKey(-1, "a")`, `await tab.paste("3", "x")`} {
		r := c.js(code)
		if !r.isError || !strings.Contains(r.all(), "\nBrowser input requires an element index from the latest AX snapshot, or null to use current focus.") {
			t.Errorf("%s: %q", code, r.all())
		}
	}
	if len(fb.calls) != n {
		t.Error("a bad index was sent")
	}
	r := c.js(`await tab.click(99)`)
	if !r.isError || !strings.Contains(r.all(), "\nelement 99 is not in the latest state of tab 1; call getAXState() again") {
		t.Errorf("stale %q", r.all())
	}
	c.ok(`await tab.scroll([10, 20], "down")`)
	if got := fb.lastJSON(); got != `{"args":[[10,20],"down",null],"binding":1,"method":"scroll","op":"tab"}` {
		t.Errorf("scroll %s", got)
	}
	if out := c.ok(`nodeRepl.write(String(await tab.playwright.getByRole("button", { name: /^Save/i }).filter({ hasText: "x" }).count()))`); out != "2" {
		t.Errorf("count %q", out)
	}
	if got := fb.lastJSON(); got != `{"args":[],"binding":1,"chain":[{"exact":false,"kind":"role","name":{"flags":"i","source":"^Save"},"role":"button"},{"hasNotText":null,"hasText":{"s":"x"},"kind":"filter"}],"desc":"getByRole('button', { name: /^Save/i }).filter()","method":"count","op":"loc"}` {
		t.Errorf("chain %s", got)
	}
	c.ok(`await tab.playwright.locator("#a").first().fill("v")`)
	if got := fb.lastJSON(); !strings.Contains(got, `"chain":[{"kind":"css","selector":"#a"},{"kind":"nth","n":0}]`) || !strings.Contains(got, `"args":["v",null]`) {
		t.Errorf("fill %s", got)
	}
	if out := c.ok(`nodeRepl.write(String(await tab.playwright.evaluate((a) => a + 1, 41)))`); out != "42" {
		t.Errorf("evaluate %q", out)
	}
	if args, _ := fb.last()["args"].([]any); len(args) != 3 || args[0] != "(a) => a + 1" || args[1] != true || args[2] != float64(41) {
		t.Errorf("evaluate args %v", args)
	}
	c.ok(`await tab.markHandoff(); await tab.markDeliverable(); await tab.goto("https://x.test/2"); await tab.back()`)
	if got := fb.last()["method"]; got != "back" {
		t.Errorf("last %v", got)
	}
}

// CDX-28: with no browser surface the browser APIs throw the documented
// error and getState lists no browsers.
func TestCDX28BrowsersOff(t *testing.T) {
	c := session(t, newWorld().engine())
	out := c.ok(`var st = await cua.getState({ emit: false }); nodeRepl.write(JSON.stringify(st.browsers) + " " + JSON.stringify(st.errors || []))`)
	if out != "[] []" {
		t.Errorf("state %q", out)
	}
}

// fennec.browser reaches the import through the same browser channel.
func TestBrowserImportREPL(t *testing.T) {
	fb := &fakeBrowsers{}
	e := newWorld().engine()
	e.Browsers = fb
	c := session(t, e)
	if out := c.ok(`nodeRepl.write(JSON.stringify(await fennec.browser.chromeProfiles()))`); !strings.Contains(out, `"dir":"Default"`) {
		t.Errorf("profiles %q", out)
	}
	if out := c.ok(`var r = await fennec.browser.importFromChrome({ profile: "Me", replace: true }); nodeRepl.write(String(r.cookies.imported))`); !strings.HasSuffix(out, "3") {
		t.Errorf("import %q", out)
	}
	if got := fb.lastJSON(); got != `{"op":"importChrome","profile":"Me","replace":true}` {
		t.Errorf("request %s", got)
	}
}
