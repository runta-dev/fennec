package browserset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/cdp"
	"fennec/cua/internal/chromium"
)

// Scripted solutions prove that each task can be solved and that its
// grader passes a solved state (cua-bench check -solve). They drive the
// same Chromium on the task's profile over a DevTools pipe of their own,
// with real mouse and keyboard events wherever the task's difficulty lies
// (the date picker, the combobox, the slider, the drag, the canvas, the
// pop-up), and do the desktop parts (moving a file, a Notes note) directly.
// They are a check of the set, not a backend.

// Solvable reports whether a task has a scripted solution.
func Solvable(id string) bool { _, ok := solutions[id]; return ok }

// Solve quits the setup's Chromium, starts it again on the same profile
// with a DevTools pipe, solves the task, records the snapshot and shuts
// the browser down (which writes its Preferences).
func Solve(ctx context.Context, env Env, d Def, headless bool, logf func(string, ...any)) error {
	fn, ok := solutions[d.ID]
	if !ok {
		return fmt.Errorf("%s has no scripted solution", d.ID)
	}
	if err := env.Desk.QuitChromium(env.App); err != nil {
		return err
	}
	bin := chromium.Executable(env.App)
	_, renamed := chromium.SafeStorageOf(bin)
	b := chromium.New(chromium.Options{Bin: bin, ProfileDir: env.Profile, Headless: headless,
		MockKeychain: env.MockKeychain || !renamed, IdleTimeout: -1, Log: logf})
	defer b.Close()
	c, _, err := b.Conn(ctx)
	if err != nil {
		return err
	}
	if _, err := c.Call(ctx, "", "Browser.setDownloadBehavior", map[string]any{
		"behavior": "allow", "downloadPath": filepath.Join(env.Home, "Downloads"), "eventsEnabled": true,
	}); err != nil {
		return err
	}
	s := &solver{env: env, c: c}
	if err := fn(ctx, s); err != nil {
		return fmt.Errorf("%s: %w", d.ID, err)
	}
	wins, err := s.windows(ctx)
	if err != nil {
		return err
	}
	if err := WriteSnapshot(env, Snapshot{Running: true, Source: "solver", Windows: wins}); err != nil {
		return err
	}
	b.Close()
	if after := afterClose[d.ID]; after != nil {
		return after(env)
	}
	return nil
}

// afterClose finishes a solution in files once Chromium has exited.
var afterClose = map[string]func(Env) error{
	"settings-download-folder": func(env Env) error {
		p := PreferencesPath(env.Profile)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var prefs map[string]any
		if err := json.Unmarshal(data, &prefs); err != nil {
			return err
		}
		dl, _ := prefs["download"].(map[string]any)
		if dl == nil {
			dl = map[string]any{}
			prefs["download"] = dl
		}
		dl["default_directory"] = filepath.Join(env.Home, "Documents", "Inbox")
		if data, err = json.Marshal(prefs); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0o600)
	},
}

type solver struct {
	env Env
	c   *cdp.Conn
}

type page struct {
	s           *solver
	target, sid string
}

func (s *solver) url(p string) string { return s.env.Base + p }

// open makes a tab and loads a page in it.
func (s *solver) open(ctx context.Context, path string) (*page, error) {
	v, err := s.c.Call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"})
	if err != nil {
		return nil, err
	}
	var r struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(v, &r); err != nil {
		return nil, err
	}
	p, err := s.attach(ctx, r.TargetID)
	if err != nil {
		return nil, err
	}
	return p, p.goTo(ctx, s.url(path))
}

func (s *solver) attach(ctx context.Context, target string) (*page, error) {
	v, err := s.c.Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true})
	if err != nil {
		return nil, err
	}
	var r struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(v, &r); err != nil {
		return nil, err
	}
	p := &page{s: s, target: target, sid: r.SessionID}
	for _, m := range []string{"Page.enable", "Runtime.enable"} {
		if _, err := p.call(ctx, m, nil); err != nil {
			return nil, err
		}
	}
	return p, nil
}

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Title    string `json:"title"`
}

func (s *solver) targets(ctx context.Context) ([]targetInfo, error) {
	v, err := s.c.Call(ctx, "", "Target.getTargets", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		TargetInfos []targetInfo `json:"targetInfos"`
	}
	err = json.Unmarshal(v, &r)
	return r.TargetInfos, err
}

// windows lists the open pages as one window (the solver's connection
// does not need window grouping: the graders count tabs across windows).
func (s *solver) windows(ctx context.Context) ([]Window, error) {
	ts, err := s.targets(ctx)
	if err != nil {
		return nil, err
	}
	w := Window{Mode: "normal", Active: 1}
	for _, t := range ts {
		if t.Type == "page" {
			w.Tabs = append(w.Tabs, TabInfo{URL: t.URL, Title: t.Title})
		}
	}
	return []Window{w}, nil
}

// waitTarget waits for a page whose URL ends with suffix.
func (s *solver) waitTarget(ctx context.Context, suffix string) (string, error) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ts, err := s.targets(ctx)
		if err != nil {
			return "", err
		}
		for _, t := range ts {
			if t.Type == "page" && strings.HasSuffix(t.URL, suffix) {
				return t.TargetID, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("no tab opened %s", suffix)
}

// waitGone waits until a closed tab has left the target list.
func (s *solver) waitGone(ctx context.Context, target string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ts, err := s.targets(ctx)
		if err != nil {
			return err
		}
		gone := true
		for _, t := range ts {
			gone = gone && t.TargetID != target
		}
		if gone {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("tab %s did not close", target)
}

func (s *solver) submissions(path string) int {
	log, _ := ReadLog(s.env.LogDir)
	return count(log, KindSubmit, path)
}

// submitted runs act and waits for one more submission to path.
func (s *solver) submitted(ctx context.Context, path string, act func() error) error {
	before := s.submissions(path)
	if err := act(); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.submissions(path) <= before {
		if time.Now().After(deadline) {
			return fmt.Errorf("nothing was submitted to %s", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

func (p *page) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return p.s.c.Call(ctx, p.sid, method, params)
}

func (p *page) goTo(ctx context.Context, url string) error {
	if _, err := p.call(ctx, "Page.navigate", map[string]any{"url": url}); err != nil {
		return err
	}
	return p.waitTrue(ctx, fmt.Sprintf(`document.readyState === "complete" && location.href.startsWith(%q)`, url), 15*time.Second)
}

// jsPrelude defines __set, which sets a form control's value the way
// typing would be seen by the page (input and change events).
const jsPrelude = `const __set = (el, v) => {
  const w = el.ownerDocument.defaultView;
  const proto = el instanceof w.HTMLSelectElement ? w.HTMLSelectElement.prototype : el instanceof w.HTMLTextAreaElement ? w.HTMLTextAreaElement.prototype : w.HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value").set.call(el, v);
  el.dispatchEvent(new Event("input", { bubbles: true }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
};
`

// eval runs an expression (with the prelude in scope) and decodes its
// value into out (which may be nil).
func (p *page) eval(ctx context.Context, expr string, out any) error {
	v, err := p.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": "(() => { " + jsPrelude + "return (" + expr + "); })()", "awaitPromise": true, "returnByValue": true,
	})
	if err != nil {
		return err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(v, &r); err != nil {
		return err
	}
	if e := r.ExceptionDetails; e != nil {
		return fmt.Errorf("javascript: %s %s", e.Text, e.Exception.Description)
	}
	if out != nil && len(r.Result.Value) > 0 {
		return json.Unmarshal(r.Result.Value, out)
	}
	return nil
}

func (p *page) waitTrue(ctx context.Context, expr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var ok bool
		err := p.eval(ctx, expr, &ok)
		if err == nil && ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s (%v)", expr, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// center scrolls an element (a JavaScript expression) into view and
// returns its middle in viewport pixels.
func (p *page) center(ctx context.Context, elem string) (float64, float64, error) {
	var xy []float64
	err := p.eval(ctx, `(() => { const el = `+elem+`; if (!el) throw new Error("no element"); el.scrollIntoView({ block: "center" }); const r = el.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; })()`, &xy)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", elem, err)
	}
	return xy[0], xy[1], nil
}

func (p *page) mouse(ctx context.Context, typ string, x, y float64, buttons int) error {
	params := map[string]any{"type": typ, "x": x, "y": y, "buttons": buttons}
	if typ != "mouseMoved" {
		params["button"] = "left"
		params["clickCount"] = 1
	}
	_, err := p.call(ctx, "Input.dispatchMouseEvent", params)
	return err
}

func (p *page) clickAt(ctx context.Context, x, y float64) error {
	for _, e := range []struct {
		typ     string
		buttons int
	}{{"mouseMoved", 0}, {"mousePressed", 1}, {"mouseReleased", 0}} {
		if err := p.mouse(ctx, e.typ, x, y, e.buttons); err != nil {
			return err
		}
	}
	return nil
}

// click clicks an element's middle with the mouse.
func (p *page) click(ctx context.Context, elem string) error {
	x, y, err := p.center(ctx, elem)
	if err != nil {
		return err
	}
	return p.clickAt(ctx, x, y)
}

// drag presses at one point, moves in steps and releases at another.
func (p *page) drag(ctx context.Context, x0, y0, x1, y1 float64) error {
	if err := p.mouse(ctx, "mouseMoved", x0, y0, 0); err != nil {
		return err
	}
	if err := p.mouse(ctx, "mousePressed", x0, y0, 1); err != nil {
		return err
	}
	const steps = 8
	for i := 1; i <= steps; i++ {
		f := float64(i) / steps
		if err := p.mouse(ctx, "mouseMoved", x0+(x1-x0)*f, y0+(y1-y0)*f, 1); err != nil {
			return err
		}
	}
	return p.mouse(ctx, "mouseReleased", x1, y1, 0)
}

var keyCodes = map[string]int{"ArrowLeft": 37, "ArrowUp": 38, "ArrowRight": 39, "ArrowDown": 40, "Enter": 13, "Tab": 9, "Home": 36, "End": 35}

func (p *page) key(ctx context.Context, key string) error {
	code, ok := keyCodes[key]
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	for _, typ := range []string{"rawKeyDown", "keyUp"} {
		if _, err := p.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": typ, "key": key, "code": key, "windowsVirtualKeyCode": code}); err != nil {
			return err
		}
	}
	return nil
}

// typeInto clicks a text field, clears it and types text.
func (p *page) typeInto(ctx context.Context, elem, text string) error {
	if err := p.click(ctx, elem); err != nil {
		return err
	}
	if err := p.eval(ctx, `(() => { const el = `+elem+`; __set(el, ""); el.focus(); return true; })()`, nil); err != nil {
		return err
	}
	_, err := p.call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

// setValue sets a control's value through the page's own events (native
// selects, date inputs), which are not what these tasks test.
func (p *page) setValue(ctx context.Context, elem, value string) error {
	return p.eval(ctx, fmt.Sprintf(`(() => { __set(%s, %q); return true; })()`, elem, value), nil)
}

func q(sel string) string { return fmt.Sprintf("document.querySelector(%q)", sel) }

// byText is the first element matching sel whose trimmed text is text.
func byText(sel, text string) string {
	return fmt.Sprintf("[...document.querySelectorAll(%q)].find((e) => e.textContent.trim() === %q)", sel, text)
}

var solutions = map[string]func(context.Context, *solver) error{
	"form-shipping": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/fernhollow/checkout")
		if err != nil {
			return err
		}
		for _, f := range [][2]string{{"#full_name", "Mara Quill"}, {"#email", "mara.quill@example.test"}, {"#street", "14 Alder Row"}, {"#city", "Brookmere"}, {"#postal", "30418"}, {"#note", "Leave it at the side gate"}} {
			if err := p.typeInto(ctx, q(f[0]), f[1]); err != nil {
				return err
			}
		}
		if err := p.setValue(ctx, q("#country"), "PT"); err != nil {
			return err
		}
		for _, sel := range []string{"#d_exp", "#gift"} {
			if err := p.click(ctx, q(sel)); err != nil {
				return err
			}
		}
		return s.submitted(ctx, "/fernhollow/checkout", func() error { return p.click(ctx, q("#ship button[type=submit]")) })
	},

	"form-wizard": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/kestrel/join")
		if err != nil {
			return err
		}
		steps := []func() error{
			func() error { return p.click(ctx, q("#p_ded")) },
			func() error { return p.setValue(ctx, q("#start"), "2027-02-01") },
			func() error { return p.click(ctx, q("#next1")) },
			func() error { return p.typeInto(ctx, q("#company"), "Tidewell Studio") },
			func() error { return p.typeInto(ctx, q("#team"), "4") },
			func() error { return p.setValue(ctx, q("#country"), "CA") },
			func() error { return p.setValue(ctx, q("#region"), "NS") },
			func() error { return p.click(ctx, q("#parking")) },
			func() error { return p.click(ctx, q("#next2")) },
		}
		for _, st := range steps {
			if err := st(); err != nil {
				return err
			}
		}
		return s.submitted(ctx, "/kestrel/confirm", func() error { return p.click(ctx, q("#confirm button[type=submit]")) })
	},

	"extract-shipment": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/larkspur/shipments")
		if err != nil {
			return err
		}
		weight := ""
		for n := 1; n <= 20 && weight == ""; n++ {
			if err := p.goTo(ctx, s.url(fmt.Sprintf("/larkspur/shipments?page=%d", n))); err != nil {
				return err
			}
			if err := p.eval(ctx, `(() => { const r = [...document.querySelectorAll("tbody tr")].find((tr) => tr.cells[0].textContent.trim() === "LF-40731"); return r ? r.cells[4].textContent.trim() : ""; })()`, &weight); err != nil {
				return err
			}
		}
		if weight == "" {
			return errors.New("shipment LF-40731 not found")
		}
		if err := p.typeInto(ctx, q("#tracking"), "LF-40731"); err != nil {
			return err
		}
		if err := p.typeInto(ctx, q("#weight"), weight); err != nil {
			return err
		}
		return s.submitted(ctx, "/larkspur/report", func() error { return p.click(ctx, q("form button[type=submit]")) })
	},

	"extract-invoice-total": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/copperleaf/invoices")
		if err != nil {
			return err
		}
		var ids []string
		if err := p.eval(ctx, `[...document.querySelectorAll(".inv")].filter((i) => i.querySelector(".badge").textContent.trim() === "Overdue").map((i) => i.id)`, &ids); err != nil {
			return err
		}
		total := 0.0
		for _, id := range ids {
			if err := p.click(ctx, fmt.Sprintf("document.getElementById(%q).querySelector('button')", id)); err != nil {
				return err
			}
			var text string
			if err := p.eval(ctx, fmt.Sprintf(`document.getElementById(%q).querySelector(".detail").innerText`, id), &text); err != nil {
				return err
			}
			m := regexp.MustCompile(`\$([0-9,]+\.[0-9]{2})`).FindStringSubmatch(text)
			if m == nil {
				return fmt.Errorf("%s: no amount shown (%q)", id, text)
			}
			f, _ := parseNumber(m[1])
			total += f
		}
		if err := p.typeInto(ctx, q("#amount"), strconv.FormatFloat(total, 'f', 2, 64)); err != nil {
			return err
		}
		return s.submitted(ctx, "/copperleaf/pay", func() error { return p.click(ctx, q("form button[type=submit]")) })
	},

	"download-pick-rename": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/marrowby/reports")
		if err != nil {
			return err
		}
		link := `[...document.querySelectorAll("tbody tr")].find((tr) => tr.cells[0].textContent.includes("Q3 2026") && tr.cells[1].textContent.trim() === "Final").querySelector("a")`
		if err := p.click(ctx, link); err != nil {
			return err
		}
		var name string
		if err := p.eval(ctx, link+`.getAttribute("href").split("/").pop()`, &name); err != nil {
			return err
		}
		src := filepath.Join(s.env.Home, "Downloads", name)
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(src); err == nil {
				if _, err := os.Stat(src + ".crdownload"); errors.Is(err, os.ErrNotExist) {
					break
				}
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s was not downloaded", name)
			}
			time.Sleep(100 * time.Millisecond)
		}
		return os.Rename(src, filepath.Join(s.env.Home, "Documents", "Reports", "q3-2026.csv"))
	},

	"tabs-keep-two": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/wrenfield/")
		if err != nil {
			return err
		}
		ids := map[string]string{}
		for _, title := range []string{"Installing", "Configuring", "Upgrading"} {
			var href string
			if err := p.eval(ctx, byText("a", title)+`.getAttribute("href")`, &href); err != nil {
				return err
			}
			v, err := s.c.Call(ctx, "", "Target.createTarget", map[string]any{"url": s.url(href)})
			if err != nil {
				return err
			}
			var r struct {
				TargetID string `json:"targetId"`
			}
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			if err := WaitPage(ctx, s.env, href, 10*time.Second); err != nil {
				return err
			}
			ids[title] = r.TargetID
		}
		if _, err := s.c.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": ids["Configuring"]}); err != nil {
			return err
		}
		return s.waitGone(ctx, ids["Configuring"])
	},

	"tabs-popup-code": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/pebblestone/pickup")
		if err != nil {
			return err
		}
		if err := p.click(ctx, q("#show")); err != nil {
			return err
		}
		target, err := s.waitTarget(ctx, "/pebblestone/code")
		if err != nil {
			return err
		}
		pop, err := s.attach(ctx, target)
		if err != nil {
			return err
		}
		if err := pop.waitTrue(ctx, `document.readyState === "complete"`, 10*time.Second); err != nil {
			return err
		}
		var code string
		if err := pop.eval(ctx, `document.querySelector("strong").textContent.trim()`, &code); err != nil {
			return err
		}
		if _, err := s.c.Call(ctx, "", "Target.closeTarget", map[string]any{"targetId": target}); err != nil {
			return err
		}
		if err := p.typeInto(ctx, q("#code"), code); err != nil {
			return err
		}
		return s.submitted(ctx, "/pebblestone/pickup", func() error { return p.click(ctx, q("form button[type=submit]")) })
	},

	"widget-datepicker": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/saltmarsh/book")
		if err != nil {
			return err
		}
		if err := p.setValue(ctx, q("#room"), "garden-twin"); err != nil {
			return err
		}
		if err := p.click(ctx, q("#in_label")); err != nil {
			return err
		}
		for i := 0; i < 12; i++ {
			var month string
			if err := p.eval(ctx, q("#month")+".textContent", &month); err != nil {
				return err
			}
			if month == "March 2027" {
				break
			}
			if err := p.click(ctx, q("#next")); err != nil {
				return err
			}
		}
		for _, day := range []string{"14", "17"} {
			if err := p.click(ctx, byText("#grid button", day)); err != nil {
				return err
			}
		}
		if err := p.click(ctx, q("#plus")); err != nil {
			return err
		}
		return s.submitted(ctx, "/saltmarsh/book", func() error { return p.click(ctx, q("#book button[type=submit]")) })
	},

	"widget-combobox-slider": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/thistledown/quote")
		if err != nil {
			return err
		}
		if err := p.typeInto(ctx, q("#pet_name"), "Juniper"); err != nil {
			return err
		}
		if err := p.typeInto(ctx, q("#breed_input"), "Shetl"); err != nil {
			return err
		}
		if err := p.click(ctx, byText("#breeds li", "Shetland Sheepdog")); err != nil {
			return err
		}
		// The thumb sits at $500: press it, then step down three times.
		if err := p.click(ctx, q("#thumb")); err != nil {
			return err
		}
		for i := 0; i < 3; i++ {
			if err := p.key(ctx, "ArrowLeft"); err != nil {
				return err
			}
		}
		if err := p.click(ctx, q("#dental")); err != nil {
			return err
		}
		return s.submitted(ctx, "/thistledown/quote", func() error { return p.click(ctx, q("#quote button[type=submit]")) })
	},

	"widget-drag-reorder": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/gorse/menu")
		if err != nil {
			return err
		}
		for i, id := range []string{"d4", "d5", "d1", "d3", "d2", "d6"} {
			var at []float64 // the dish's middle, and the top of the slot it goes to
			if err := p.eval(ctx, fmt.Sprintf(`(() => { const items = [...document.querySelectorAll("#dishes li")]; const el = items.find((x) => x.dataset.id === %q); if (items.indexOf(el) === %d) return []; const r = el.getBoundingClientRect(), t = items[%d].getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2, t.top + 4]; })()`, id, i, i), &at); err != nil {
				return err
			}
			if len(at) == 0 {
				continue
			}
			if err := p.drag(ctx, at[0], at[1], at[0], at[2]); err != nil {
				return err
			}
		}
		return s.submitted(ctx, "/gorse/menu", func() error { return p.click(ctx, q("#save button[type=submit]")) })
	},

	"widget-iframe-shadow": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/nimbus/account")
		if err != nil {
			return err
		}
		root := `document.querySelector("iframe").contentDocument.querySelector("nimbus-prefs").shadowRoot`
		if err := p.waitTrue(ctx, `(() => { try { return !!(`+root+`.querySelector("#display")); } catch (e) { return false; } })()`, 10*time.Second); err != nil {
			return err
		}
		if err := p.eval(ctx, `(() => { const r = `+root+`; __set(r.querySelector("#display"), "Orla Fenwick"); r.querySelector("#g_weekly").click(); return true; })()`, nil); err != nil {
			return err
		}
		var xy []float64
		if err := p.eval(ctx, `(() => { const f = document.querySelector("iframe"); f.scrollIntoView({ block: "center" }); const fr = f.getBoundingClientRect(); const b = `+root+`.querySelector("button").getBoundingClientRect(); return [fr.left + f.clientLeft + b.left + b.width / 2, fr.top + f.clientTop + b.top + b.height / 2]; })()`, &xy); err != nil {
			return err
		}
		return s.submitted(ctx, "/nimbus/prefs", func() error { return p.clickAt(ctx, xy[0], xy[1]) })
	},

	"widget-canvas-seat": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/lumen/seats")
		if err != nil {
			return err
		}
		// Seat 8 of row F (index 5): x = 40 + 7*40 + 16 (the aisle), y = 60 + 5*32, plus half a seat.
		var xy []float64
		if err := p.eval(ctx, `(() => { const c = document.getElementById("map"); c.scrollIntoView({ block: "center" }); const r = c.getBoundingClientRect(); return [r.left + (336 + 16) * r.width / c.width, r.top + (220 + 12) * r.height / c.height]; })()`, &xy); err != nil {
			return err
		}
		if err := p.clickAt(ctx, xy[0], xy[1]); err != nil {
			return err
		}
		return s.submitted(ctx, "/lumen/reserve", func() error { return p.click(ctx, q("#reserve button[type=submit]")) })
	},

	"settings-download-folder": func(ctx context.Context, s *solver) error {
		v, err := s.c.Call(ctx, "", "Target.createTarget", map[string]any{"url": "chrome://settings/downloads"})
		if err != nil {
			return err
		}
		var r struct {
			TargetID string `json:"targetId"`
		}
		if err := json.Unmarshal(v, &r); err != nil {
			return err
		}
		p, err := s.attach(ctx, r.TargetID)
		if err != nil {
			return err
		}
		if err := p.waitTrue(ctx, `typeof chrome !== "undefined" && !!chrome.settingsPrivate`, 15*time.Second); err != nil {
			return err
		}
		// The switch, as the settings page sets it. The folder is chosen in a
		// native macOS panel, which this page cannot drive; afterClose
		// writes it once the browser is down.
		var ok bool
		if err := p.eval(ctx, `new Promise((r) => chrome.settingsPrivate.setPref("download.prompt_for_download", true, "", r))`, &ok); err != nil {
			return err
		}
		if !ok {
			return errors.New("settingsPrivate refused download.prompt_for_download")
		}
		return nil
	},

	"crossapp-textedit-form": func(ctx context.Context, s *solver) error {
		data, err := os.ReadFile(filepath.Join(s.env.Home, "Documents", "new-member.txt"))
		if err != nil {
			return err
		}
		sheet := map[string]string{}
		for _, line := range strings.Split(string(data), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok {
				sheet[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		first, last, _ := strings.Cut(sheet["Name"], " ")
		p, err := s.open(ctx, "/harborlantern/join")
		if err != nil {
			return err
		}
		for _, f := range [][2]string{{"#first", first}, {"#last", last}, {"#phone", sheet["Phone"]}, {"#member_since", sheet["Joined the club in"]}} {
			if err := p.typeInto(ctx, q(f[0]), f[1]); err != nil {
				return err
			}
		}
		if err := p.eval(ctx, fmt.Sprintf(`(() => { const s = document.getElementById("genre"); const o = [...s.options].find((o) => o.text.toLowerCase() === %q); __set(s, o.value); return true; })()`, strings.ToLower(sheet["Favourite kind of book"])), nil); err != nil {
			return err
		}
		if err := p.click(ctx, fmt.Sprintf(`document.querySelector("input[name=meetings][value=%s]")`, sheet["Prefers to meet"])); err != nil {
			return err
		}
		return s.submitted(ctx, "/harborlantern/join", func() error { return p.click(ctx, q("form button[type=submit]")) })
	},

	"crossapp-page-notes": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/briarwood/appointment")
		if err != nil {
			return err
		}
		if err := p.click(ctx, q("#show")); err != nil {
			return err
		}
		var cells map[string]string
		if err := p.eval(ctx, `Object.fromEntries([...document.querySelectorAll("tr")].map((tr) => [tr.cells[0].textContent.trim(), tr.cells[1].textContent.trim()]))`, &cells); err != nil {
			return err
		}
		return s.env.Desk.MakeNote("Vet appointment", fmt.Sprintf("%s at %s, reference %s", cells["Date"], cells["Time"], cells["Reference"]))
	},

	"distract-overlay": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/moorfield/subscribe")
		if err != nil {
			return err
		}
		if err := p.waitTrue(ctx, q("#overlay")+`.classList.contains("on")`, 5*time.Second); err != nil {
			return err
		}
		if err := p.click(ctx, q("#later")); err != nil {
			return err
		}
		if err := p.typeInto(ctx, q("#email"), "rhea.marsh@example.test"); err != nil {
			return err
		}
		for _, id := range []string{"#t_local", "#t_sport", "#t_weather", "#t_events"} {
			if err := p.click(ctx, q(id)); err != nil {
				return err
			}
		}
		return s.submitted(ctx, "/moorfield/subscribe", func() error { return p.click(ctx, byText("button", "Sign up for free")) })
	},

	"distract-notification": func(ctx context.Context, s *solver) error {
		p, err := s.open(ctx, "/driftwood/schedule")
		if err != nil {
			return err
		}
		var show string
		if err := p.eval(ctx, `(() => { const cols = [...document.querySelectorAll("thead th")].map((th) => th.textContent.trim()); const row = [...document.querySelectorAll("tbody tr")].find((tr) => tr.cells[0].textContent.trim() === "Thursday"); return row.cells[cols.indexOf("21:00")].textContent.trim(); })()`, &show); err != nil {
			return err
		}
		if err := p.eval(ctx, fmt.Sprintf(`(() => { const s = document.getElementById("show"); __set(s, [...s.options].find((o) => o.text === %q).value); return true; })()`, show), nil); err != nil {
			return err
		}
		if err := p.typeInto(ctx, q("#song"), "Low Tide Lanterns"); err != nil {
			return err
		}
		if err := p.typeInto(ctx, q("#from"), "Pim"); err != nil {
			return err
		}
		return s.submitted(ctx, "/driftwood/request", func() error { return p.click(ctx, q("form button[type=submit]")) })
	},
}
