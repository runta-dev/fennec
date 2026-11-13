package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Page operations over CDP: navigation and load waits, the viewport,
// screenshots, mouse and keyboard input, and the agent cursor.

// lifecycle tracks a tab's main-frame lifecycle events per document.
type lifecycle struct {
	mu      sync.Mutex
	frame   string // the main frame, learned from navigate or "init"
	loader  string // the current document's loader
	events  map[string]bool
	changed chan struct{} // closed and replaced on every event
}

func newLifecycle() *lifecycle {
	return &lifecycle{events: map[string]bool{}, changed: make(chan struct{})}
}

func (l *lifecycle) note(frame, loader, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.frame != "" && frame != l.frame {
		return // a child frame
	}
	if name == "init" {
		if l.frame == "" {
			l.frame = frame
		}
		l.loader = loader
		l.events = map[string]bool{}
	} else if loader != l.loader {
		return
	}
	l.events[name] = true
	close(l.changed)
	l.changed = make(chan struct{})
}

func (l *lifecycle) setFrame(frame string) {
	l.mu.Lock()
	if l.frame == "" {
		l.frame = frame
	}
	l.mu.Unlock()
}

// state answers the current loader, whether it saw event, and a channel
// that closes on the next event.
func (l *lifecycle) state(event string) (string, bool, <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loader, l.events[event], l.changed
}

// lifecycle event names for Playwright's load states.
var loadEvents = map[string]string{"load": "load", "domcontentloaded": "DOMContentLoaded", "networkidle": "networkIdle"}

// waitLoader waits until loader (or, when loader is "", any loader other
// than notLoader) has seen event.
func (s *Session) waitLoader(ctx context.Context, t *tabRec, loader, notLoader, event string, timeout time.Duration) error {
	if t.life == nil {
		return nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		cur, seen, ch := t.life.state(event)
		if seen && ((loader != "" && cur == loader) || (loader == "" && cur != notLoader)) {
			return nil
		}
		select {
		case <-ch:
		case <-deadline.C:
			return fmt.Errorf("Timeout %dms exceeded waiting for the page to load", timeout.Milliseconds())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// navigate loads url in the tab and waits for its load event (up to 30 s;
// a page that keeps loading is left to the next read).
func (s *Session) navigate(ctx context.Context, t *tabRec, u string) error {
	v, err := s.cdp(ctx, t, "Page.navigate", map[string]any{"url": u})
	if err != nil {
		return err
	}
	var r struct {
		FrameID   string `json:"frameId"`
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	_ = json.Unmarshal(v, &r)
	if r.ErrorText != "" && r.ErrorText != "net::ERR_ABORTED" {
		return fmt.Errorf("%s at %s", r.ErrorText, u)
	}
	if t.life != nil {
		t.life.setFrame(r.FrameID)
	}
	if r.LoaderID == "" {
		return nil // same-document navigation
	}
	err = s.waitLoader(ctx, t, r.LoaderID, "", "load", 30*time.Second)
	if err != nil && ctx.Err() != nil {
		return err
	}
	return nil
}

func (s *Session) reload(ctx context.Context, t *tabRec) error {
	before := ""
	if t.life != nil {
		before, _, _ = t.life.state("load")
	}
	if _, err := s.cdp(ctx, t, "Page.reload", nil); err != nil {
		return err
	}
	err := s.waitLoader(ctx, t, "", before, "load", 30*time.Second)
	if err != nil && ctx.Err() != nil {
		return err
	}
	return nil
}

// eval evaluates an expression in the page and returns its JSON value.
func (s *Session) eval(ctx context.Context, t *tabRec, expr string) (json.RawMessage, error) {
	v, err := s.cdp(ctx, t, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	if err != nil {
		return nil, err
	}
	return runtimeValue(v)
}

// waitLoadState waits for a Playwright load state of the current document.
func (s *Session) waitLoadState(ctx context.Context, t *tabRec, state string, timeout time.Duration) error {
	ev, ok := loadEvents[state]
	if !ok {
		return fmt.Errorf("state is load, domcontentloaded or networkidle")
	}
	deadline := time.Now().Add(timeout)
	for {
		v, err := s.eval(ctx, t, "document.readyState")
		if err != nil {
			return err
		}
		var rs string
		_ = json.Unmarshal(v, &rs)
		done := false
		switch state {
		case "domcontentloaded":
			done = rs == "interactive" || rs == "complete"
		case "load":
			done = rs == "complete"
		case "networkidle":
			if rs == "complete" {
				if t.life == nil {
					done = true
				} else {
					_, done, _ = t.life.state(ev)
				}
			}
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Timeout %dms exceeded waiting for %s", timeout.Milliseconds(), state)
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// globRE compiles a Playwright URL glob: ** any text, * any text without
// a slash, {a,b} alternatives; everything else literal.
func globRE(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	inGroup := false
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '{':
			inGroup = true
			b.WriteString("(?:")
		case c == '}' && inGroup:
			inGroup = false
			b.WriteString(")")
		case c == ',' && inGroup:
			b.WriteString("|")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// waitURL waits until the tab's URL matches (exactly, or as a glob when
// the pattern has * or {), then for its load event.
func (s *Session) waitURL(ctx context.Context, t *tabRec, pattern string, timeout time.Duration) error {
	match := func(u string) bool { return u == pattern }
	if strings.ContainsAny(pattern, "*{") {
		re, err := globRE(pattern)
		if err != nil {
			return err
		}
		match = re.MatchString
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, err := s.pageInfo(ctx, t); err != nil {
			return err
		}
		if match(t.url) {
			rest := time.Until(deadline)
			if rest < time.Second {
				rest = time.Second
			}
			return s.waitLoadState(ctx, t, "load", rest)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Timeout %dms exceeded waiting for the URL %s", timeout.Milliseconds(), pattern)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// viewport is the tab's URL, title and layout viewport in CSS pixels.
type viewport struct {
	URL   string
	Title string
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	DPR   float64 `json:"dpr"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

const viewportJS = `({ w: innerWidth, h: innerHeight, dpr: devicePixelRatio, x: scrollX, y: scrollY })`

// pageInfo reads the tab's URL, title and viewport.
func (s *Session) pageInfo(ctx context.Context, t *tabRec) (viewport, error) {
	var vp viewport
	v, err := s.call(ctx, "Target.getTargetInfo", map[string]any{"targetId": t.targetID})
	if err != nil {
		if isGone(err) {
			s.forget(t)
			return vp, errTabGone
		}
		return vp, err
	}
	var ti struct {
		TargetInfo targetInfo `json:"targetInfo"`
	}
	_ = json.Unmarshal(v, &ti)
	t.url, t.title = ti.TargetInfo.URL, ti.TargetInfo.Title
	vp.URL, vp.Title = t.url, t.title
	raw, err := s.eval(ctx, t, viewportJS)
	if err != nil {
		return vp, err
	}
	_ = json.Unmarshal(raw, &vp)
	return vp, nil
}

// capture takes a PNG of the viewport (or the whole page) at CSS-pixel
// size, without the agent cursor.
func (s *Session) capture(ctx context.Context, t *tabRec, vp viewport, full bool) ([]byte, error) {
	params := map[string]any{"format": "png"}
	scale := 1.0
	if vp.DPR > 0 {
		scale = 1 / vp.DPR
	}
	switch {
	case full:
		v, err := s.cdp(ctx, t, "Page.getLayoutMetrics", nil)
		if err != nil {
			return nil, err
		}
		var lm struct {
			Content struct {
				Width  float64 `json:"width"`
				Height float64 `json:"height"`
			} `json:"cssContentSize"`
		}
		_ = json.Unmarshal(v, &lm)
		if lm.Content.Width > 0 && lm.Content.Height > 0 {
			params["captureBeyondViewport"] = true
			params["clip"] = map[string]any{"x": 0, "y": 0, "width": lm.Content.Width, "height": lm.Content.Height, "scale": scale}
		}
	case scale != 1 && vp.W > 0 && vp.H > 0:
		params["clip"] = map[string]any{"x": vp.X, "y": vp.Y, "width": vp.W, "height": vp.H, "scale": scale}
	}
	s.cursor(ctx, t, "hide", 0, 0, "")
	defer s.cursor(context.WithoutCancel(ctx), t, "show", 0, 0, "")
	v, err := s.cdp(ctx, t, "Page.captureScreenshot", params)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data string `json:"data"`
	}
	_ = json.Unmarshal(v, &r)
	data, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil || len(data) == 0 {
		return nil, errors.New("the screenshot did not arrive")
	}
	return data, nil
}

// The agent cursor: a fixed, click-through, aria-hidden element in the
// page (outside body, label drawn by CSS so it adds no text to the
// document) that shows where the agent acts and what it does.
const cursorJS = `((op, x, y, label) => {
  const tag = 'fennec-agent-cursor';
  let host = document.documentElement && document.documentElement.querySelector(':scope > ' + tag);
  if (!host) {
    if (op !== 'move' || !document.documentElement) return false;
    host = document.createElement(tag);
    host.setAttribute('aria-hidden', 'true');
    host.style.cssText = 'all:initial;position:fixed;left:0;top:0;width:0;height:0;z-index:2147483647;pointer-events:none;';
    const root = host.attachShadow({ mode: 'open' });
    root.innerHTML = '<style>' +
      '.c{position:fixed;width:18px;height:18px;margin:-3px 0 0 -3px;transition:left .18s ease-out,top .18s ease-out;}' +
      '.c svg{width:18px;height:18px;filter:drop-shadow(0 1px 1px rgba(0,0,0,.35));}' +
      '.c::after{content:attr(data-label);position:absolute;left:16px;top:16px;white-space:nowrap;font:500 11px/1.4 -apple-system,system-ui,sans-serif;color:#fff;background:#d4572a;border-radius:4px;padding:1px 6px;max-width:320px;overflow:hidden;text-overflow:ellipsis;}' +
      '.c[data-label=""]::after{display:none;}' +
      '</style><div class="c"><svg viewBox="0 0 18 18"><path d="M3 2l11 6.5-5 1.2-2.3 4.8z" fill="#d4572a" stroke="#fff" stroke-width="1.2" stroke-linejoin="round"/></svg></div>';
    document.documentElement.appendChild(host);
  }
  if (op === 'hide') { host.style.display = 'none'; return true; }
  if (op === 'show') { host.style.display = ''; return true; }
  const c = host.shadowRoot.querySelector('.c');
  c.style.left = x + 'px'; c.style.top = y + 'px';
  c.setAttribute('data-label', label || '');
  host.style.display = '';
  return true;
})`

// cursor moves, hides or shows the agent cursor; it never fails a step.
func (s *Session) cursor(ctx context.Context, t *tabRec, op string, x, y float64, lbl string) {
	if !t.agent && op == "move" {
		return
	}
	args, _ := json.Marshal([]any{op, x, y, lbl})
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = s.cdp(cctx, t, "Runtime.evaluate", map[string]any{"expression": cursorJS + "(..." + string(args) + ")", "returnByValue": true})
}

// Mouse input. Buttons and modifiers use CDP's bit values.
var buttonBits = map[string]int{"left": 1, "right": 2, "middle": 4}

func (s *Session) mouse(ctx context.Context, t *tabRec, typ string, x, y float64, button string, count int, extra map[string]any) error {
	p := map[string]any{"type": typ, "x": round2(x), "y": round2(y), "modifiers": t.mods, "buttons": t.buttons}
	if button != "" {
		p["button"] = button
		p["clickCount"] = count
	}
	for k, v := range extra {
		p[k] = v
	}
	_, err := s.cdp(ctx, t, "Input.dispatchMouseEvent", p)
	return err
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// lastPoint is where the tab's pointer is, for down/up without a point.
func (t *tabRec) pointer() (float64, float64) { return t.px, t.py }

func (s *Session) mouseMove(ctx context.Context, t *tabRec, x, y float64, steps int, lbl string) error {
	if lbl != "" {
		s.cursor(ctx, t, "move", x, y, s.cursorLabel(lbl))
	}
	if steps < 1 {
		steps = 1
	}
	x0, y0 := t.pointer()
	for i := 1; i <= steps; i++ {
		xi := x0 + (x-x0)*float64(i)/float64(steps)
		yi := y0 + (y-y0)*float64(i)/float64(steps)
		if err := s.mouse(ctx, t, "mouseMoved", xi, yi, "", 0, nil); err != nil {
			return err
		}
	}
	t.px, t.py = x, y
	return nil
}

func (s *Session) mouseDown(ctx context.Context, t *tabRec, button string, count int) error {
	x, y := t.pointer()
	t.buttons |= buttonBits[button]
	return s.mouse(ctx, t, "mousePressed", x, y, button, count, nil)
}

func (s *Session) mouseUp(ctx context.Context, t *tabRec, button string, count int) error {
	x, y := t.pointer()
	t.buttons &^= buttonBits[button]
	return s.mouse(ctx, t, "mouseReleased", x, y, button, count, nil)
}

// mouseClick moves to the point and presses count times, as Playwright's
// click does (each press carries its click count).
func (s *Session) mouseClick(ctx context.Context, t *tabRec, x, y float64, button string, count int, lbl string) error {
	if err := s.mouseMove(ctx, t, x, y, 1, lbl); err != nil {
		return err
	}
	for i := 1; i <= count; i++ {
		if err := s.mouseDown(ctx, t, button, i); err != nil {
			return err
		}
		if err := s.mouseUp(ctx, t, button, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) mouseWheel(ctx context.Context, t *tabRec, dx, dy float64) error {
	x, y := t.pointer()
	return s.mouse(ctx, t, "mouseWheel", x, y, "", 0, map[string]any{"deltaX": dx, "deltaY": dy})
}

// Keyboard input.

func (s *Session) keyDown(ctx context.Context, t *tabRec, name string) error {
	k, ok := keyDef(name)
	if !ok {
		return fmt.Errorf("unknown key %q", name)
	}
	if bit := modifierBits[k.key]; bit != 0 {
		t.mods |= bit
	}
	p := map[string]any{"type": "rawKeyDown", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.keyCode, "modifiers": t.mods, "location": k.location}
	text := k.text
	if t.mods&^modShift != 0 {
		text = "" // a shortcut types nothing
	}
	if text != "" {
		p["type"] = "keyDown"
		p["text"], p["unmodifiedText"] = text, text
	}
	if cmds := editingCommands(t.mods, k.code); len(cmds) > 0 {
		p["commands"] = cmds
	}
	_, err := s.cdp(ctx, t, "Input.dispatchKeyEvent", p)
	return err
}

func (s *Session) keyUp(ctx context.Context, t *tabRec, name string) error {
	k, ok := keyDef(name)
	if !ok {
		return fmt.Errorf("unknown key %q", name)
	}
	if bit := modifierBits[k.key]; bit != 0 {
		t.mods &^= bit
	}
	_, err := s.cdp(ctx, t, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.keyCode, "modifiers": t.mods, "location": k.location})
	return err
}

// splitChord reads a Playwright chord ("Meta+a", "Shift++").
func splitChord(chord string) []string {
	parts := strings.Split(chord, "+")
	if n := len(parts); n >= 2 && parts[n-1] == "" && parts[n-2] == "" {
		parts = append(parts[:n-2], "+")
	}
	return parts
}

// keyPress presses a Playwright chord: every key down in order, then up
// in reverse.
func (s *Session) keyPress(ctx context.Context, t *tabRec, chord string) error {
	keys := splitChord(chord)
	for _, k := range keys {
		if _, ok := keyDef(k); !ok {
			return fmt.Errorf("unknown key %q", k)
		}
	}
	for i, k := range keys {
		if err := s.keyDown(ctx, t, k); err != nil {
			s.releaseKeys(ctx, t, keys[:i])
			return err
		}
	}
	s.releaseKeys(ctx, t, keys)
	return nil
}

func (s *Session) releaseKeys(ctx context.Context, t *tabRec, keys []string) {
	rctx := context.WithoutCancel(ctx)
	for i := len(keys) - 1; i >= 0; i-- {
		_ = s.keyUp(rctx, t, keys[i])
	}
}

// keyType types text: characters of the US layout as key presses, others
// (and everything while a modifier is held) as inserted text.
func (s *Session) keyType(ctx context.Context, t *tabRec, text string) error {
	for _, r := range text {
		ch := string(r)
		if r == '\n' || r == '\r' {
			ch = "Enter"
		}
		if k, ok := keyDef(ch); ok && (k.text != "" || ch == "Enter") && t.mods == 0 {
			if err := s.keyDown(ctx, t, ch); err != nil {
				return err
			}
			if err := s.keyUp(ctx, t, ch); err != nil {
				return err
			}
			continue
		}
		if err := s.insertText(ctx, t, ch); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) insertText(ctx context.Context, t *tabRec, text string) error {
	_, err := s.cdp(ctx, t, "Input.insertText", map[string]any{"text": text})
	return err
}

// pasteJS offers the content to the focused element as a paste event (with
// text/plain and, for html, text/html); when the page does not take it,
// the text is inserted as typing would. The user's clipboard is never
// touched.
const pasteJS = `((text, html) => {
  const el = document.activeElement || document.body;
  const dt = new DataTransfer();
  dt.setData('text/plain', text);
  if (html) dt.setData('text/html', html);
  const ev = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true, composed: true });
  const taken = !el.dispatchEvent(ev);
  if (taken) return 'taken';
  if (html && el.isContentEditable && document.execCommand('insertHTML', false, html)) return 'html';
  return 'insert';
})`

func (s *Session) pasteContent(ctx context.Context, t *tabRec, text, html string) error {
	args, _ := json.Marshal([]any{text, html})
	v, err := s.eval(ctx, t, pasteJS+"(..."+string(args)+")")
	if err != nil {
		return err
	}
	var how string
	_ = json.Unmarshal(v, &how)
	if how == "insert" {
		return s.insertText(ctx, t, text)
	}
	return nil
}
