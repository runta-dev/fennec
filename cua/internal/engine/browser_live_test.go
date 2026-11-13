//go:build chromiumlive

package engine_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/browser"
	"fennec/cua/internal/chromium"
)

// The live browser tests drive the pinned Chromium on this Mac, headless,
// through the REPL: a temporary profile, only pages this test serves on
// 127.0.0.1, no OS-level input, and never the user's Chrome or Keychain.
// Run with FENNEC_CUA_CHROMIUM=/path/to/Chromium.app
// go test -tags chromiumlive -run Live ./internal/engine

const livePage = `<!doctype html><html><head><title>Live Form</title></head><body>
<h1>Order</h1>
<label>Name <input id="name"></label>
<label>Password <input id="pw" type="password"></label>
<select id="city" aria-label="City"><option>Tokyo</option><option>Paris</option></select>
<textarea id="notes" aria-label="Notes">hello world hello</textarea>
<label><input type="checkbox" id="agree"> Agree</label>
<button id="save" onclick="document.getElementById('out').textContent='Saved ' + document.getElementById('name').value">Save</button>
<button aria-haspopup="menu" aria-expanded="false" onclick="this.setAttribute('aria-expanded', this.getAttribute('aria-expanded') === 'true' ? 'false' : 'true')">More</button>
<p id="out">Not saved</p>
<a href="/two">Next page</a>
<div style="height:3000px"></div><p>Bottom</p>
</body></html>`

func liveServer(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/two" {
			fmt.Fprint(w, `<!doctype html><title>Page Two</title><h1>Two</h1>`)
			return
		}
		fmt.Fprint(w, livePage)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

func TestLiveBrowserREPL(t *testing.T) {
	m := liveManager(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m.CloseAll(ctx)
	}()
	e := newWorld().engine()
	e.Browsers = m
	c := session(t, e)
	url := liveServer(t)
	show := func(label, out string) {
		t.Logf("---- %s ----\n%s", label, out)
	}
	run := func(code string) string {
		t.Helper()
		r := c.js(code)
		out := r.all()
		if r.isError {
			t.Fatalf("%s\n%s", code, out)
		}
		return out
	}
	run(`1`) // the first call shows the API documentation
	out := run(`var tab = await cua.createBrowserTab("chromium", "` + url + `", { sessionName: "fennec-cua live test" })`)
	show("createBrowserTab", out)
	if !strings.Contains(out, "# Browser: Chromium") || !strings.Contains(out, `Browser tab: 1, Title: "Live Form"`) || !strings.Contains(out, "0 AXWebArea Live Form, URL: 127.0.0.1:") {
		t.Fatalf("create: %s", out)
	}
	id := func(text, pattern string) string {
		t.Helper()
		m := regexp.MustCompile(`(?m)^[~+]?\t*(\d+) ` + pattern).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("no %q in\n%s", pattern, text)
		}
		return m[1]
	}
	state := run(`await tab.getAXState()`)
	show("getAXState (first read through the binding: full)", state)
	nameID := id(state, `text field \(settable\) Name`)
	saveID := id(state, `button Save`)
	moreID := id(state, `pop up button \(collapsed\) More`)
	notesID := id(state, `text entry area \(settable\) Description: Notes`)
	if out := run(`await tab.getAXState()`); !strings.Contains(out, "There has been no change in the accessibility tree.") {
		t.Errorf("unchanged read: %s", out)
	}
	out = run(`await tab.typeText(` + nameID + `, "Ada"); await tab.click(` + saveID + `); await tab.getAXState()`)
	show("typeText + click + diff", out)
	if !strings.Contains(out, "The following is a diff") || !strings.Contains(out, "Saved Ada") || !strings.Contains(out, "Value: Ada") {
		t.Errorf("diff after input: %s", out)
	}
	out = run(`await tab.performSecondaryAction(` + moreID + `, "Expand"); await tab.getAXState()`)
	if !strings.Contains(out, "pop up button (expanded) More") {
		t.Errorf("expand: %s", out)
	}
	run(`await tab.selectText(` + notesID + `, "hello", { suffix: " world" }); await tab.pressKey(null, "BackSpace")`)
	if out := run(`nodeRepl.write(await tab.playwright.locator("#notes").inputValue())`); !strings.Contains(out, " world hello") || strings.Contains(out, "hello world hello") {
		t.Errorf("selectText + BackSpace: %s", out)
	}
	run(`await tab.playwright.getByRole("combobox", { name: "City" }).selectOption("Paris")`)
	if out := run(`nodeRepl.write(await tab.playwright.getByLabel("City").inputValue())`); !strings.Contains(out, "Paris") {
		t.Errorf("selectOption: %s", out)
	}
	run(`await tab.playwright.getByRole("checkbox", { name: "Agree" }).check()`)
	if out := run(`nodeRepl.write(String(await tab.playwright.getByRole("checkbox").isChecked()))`); !strings.Contains(out, "true") {
		t.Errorf("check: %s", out)
	}
	run(`await tab.playwright.getByLabel("Password").fill("s3cret-pass")`)
	out = run(`await tab.getAXState({ disableDiffing: true })`)
	if strings.Contains(out, "s3cret-pass") {
		t.Errorf("the password shows in the state: %s", out)
	}
	if r := c.js(`await tab.playwright.getByRole("button").click()`); !r.isError || !strings.Contains(r.all(), "strict mode violation") {
		t.Errorf("strict: %s", r.all())
	}
	out = run(`nodeRepl.write(await tab.playwright.domSnapshot())`)
	show("domSnapshot", out)
	if !strings.Contains(out, `- heading "Order" [level=1]`) || !strings.Contains(out, `- button "Save"`) {
		t.Errorf("domSnapshot: %s", out)
	}
	out = run(`var shot = await tab.getScreenshot(); nodeRepl.write(shot.length > 1000 ? "png ok" : "small")`)
	if !strings.Contains(out, "png ok") {
		t.Errorf("screenshot: %s", out)
	}
	run(`await tab.scroll([200, 200], "down", 2)`)
	if out := run(`nodeRepl.write(String(await tab.playwright.evaluate(() => window.scrollY)))`); strings.TrimSpace(lastLine(out)) == "0" {
		t.Errorf("scroll did not move: %s", out)
	}
	out = run(`await tab.playwright.getByText("Next page").click(); await tab.getAXState()`)
	show("navigate by link", out)
	if !strings.Contains(out, `Title: "Page Two"`) || strings.Contains(out, "The following is a diff") {
		t.Errorf("navigation gives a full state: %s", out)
	}
	run(`await tab.back()`)
	if out := run(`nodeRepl.write(await tab.title())`); !strings.Contains(out, "Live Form") {
		t.Errorf("back: %s", out)
	}
	// The agent cursor is in the page but not in its accessibility tree.
	if out := run(`await tab.click([5, 5]); nodeRepl.write(String(await tab.playwright.evaluate(() => !!document.querySelector("fennec-agent-cursor"))))`); !strings.Contains(out, "true") {
		t.Errorf("agent cursor: %s", out)
	}
	if out := run(`await tab.getAXState({ disableDiffing: true })`); strings.Contains(out, "fennec") {
		t.Errorf("the cursor shows in the state: %s", out)
	}
	// Select all and replace by keys (macOS editing commands), then paste.
	state = run(`await tab.getAXState({ disableDiffing: true })`)
	nameID = id(state, `text field \(settable\) Name`)
	run(`await tab.pressKey(` + nameID + `, "super+a"); await tab.typeText(null, "Grace")`)
	if out := run(`nodeRepl.write(await tab.playwright.locator("#name").inputValue())`); lastLine(out) != "Grace" {
		t.Errorf("select all + type: %s", out)
	}
	run(`await tab.paste(` + nameID + `, " Hopper")`)
	if out := run(`nodeRepl.write(await tab.playwright.locator("#name").inputValue())`); lastLine(out) != "Grace Hopper" {
		t.Errorf("paste: %s", out)
	}
	// goto, waitForURL and a reload.
	run(`await tab.goto("` + url + `two"); await tab.playwright.waitForURL("**/two"); await tab.reload()`)
	if out := run(`nodeRepl.write(await tab.url())`); !strings.HasSuffix(lastLine(out), "/two") {
		t.Errorf("goto: %s", out)
	}
	if r := c.js(`await tab.goto("http://unreachable.invalid/")`); !r.isError || !strings.Contains(r.all(), "net::ERR_") {
		t.Errorf("unreachable: %s", r.all())
	}
	run(`await tab.goto("` + url + `")`)
	// Screenshots are CSS-pixel PNGs of the viewport.
	if out := run(`var s2 = await tab.screenshot(); var w = (s2[16] << 24) | (s2[17] << 16) | (s2[18] << 8) | s2[19]; nodeRepl.write(String(w) + " " + String(await tab.playwright.evaluate(() => innerWidth)))`); func() bool {
		f := strings.Fields(lastLine(out))
		return len(f) != 2 || f[0] != f[1]
	}() {
		t.Errorf("screenshot width: %s", out)
	}
	run(`await tab.markHandoff()`)
	out = run(`await cua.listTabs()`)
	show("listTabs", out)
	if !strings.Contains(out, `"id":"1"`) || !strings.Contains(out, `"browserId":"chromium"`) {
		t.Errorf("listTabs: %s", out)
	}
	out = run(`var st = await cua.getState({ emit: false }); nodeRepl.write(JSON.stringify(st.browsers))`)
	show("getState().browsers", out)
	if !strings.Contains(out, `"id":"chromium"`) || !strings.Contains(out, `"tabs":[{"id":"1"`) {
		t.Errorf("getState: %s", out)
	}
	out = run(`var t2 = await cua.createBrowserTab("chrome", "` + url + `two")`)
	if !strings.Contains(out, `Browser tab: 2, Title: "Page Two"`) || strings.Contains(out, "# Browser: Chromium") {
		t.Errorf("second tab: %s", out)
	}
	run(`await t2.close()`)
	c.call("js_reset", map[string]any{})
	out = run(`var again = await cua.getTab("1", { browser: "chromium" })`)
	show("getTab after js_reset", out)
	if !strings.Contains(out, "# Browser: Chromium") || !strings.Contains(out, "Browser tab: 1,") {
		t.Errorf("getTab after reset: %s", out)
	}
	if r := c.js(`await cua.getTab("99")`); !r.isError || !strings.Contains(r.all(), "\nTab not found in browser chromium.") {
		t.Errorf("missing tab: %s", r.all())
	}
	if r := c.js(`await again.typeText(1.5, "x")`); !r.isError || !strings.Contains(r.all(), "Browser input requires an element index") {
		t.Errorf("bad index: %s", r.all())
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// liveManager runs the Chromium FENNEC_CUA_CHROMIUM names, headless, on a
// temporary profile.
func liveManager(t *testing.T) *browser.Manager {
	t.Helper()
	bin := chromium.Locate("")
	if bin == "" {
		t.Skip("set FENNEC_CUA_CHROMIUM to the pinned Chromium.app")
	}
	dir, err := os.MkdirTemp("", "fccr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	logf := func(f string, a ...any) { t.Logf(f, a...) }
	b := chromium.New(chromium.Options{Bin: bin, ProfileDir: dir, Headless: true, Log: logf})
	t.Cleanup(b.Close)
	return browser.NewManager(browser.Options{Browser: b, Log: logf})
}
