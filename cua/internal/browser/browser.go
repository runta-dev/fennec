// Package browser serves the REPL's browser API (docs/codex-parity.md,
// section 8) from the Chromium fennec-cua bundles (internal/chromium),
// driven over the DevTools protocol (internal/cdp). Every decision (the tab
// registry, element ids, the state text, locators, key names, safety
// checks) is made here in Go.
package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/cdp"
	"fennec/cua/internal/protocol"
)

//go:embed guide.md
var guide string

// Guide is the browser documentation the first browser call of a runtime
// shows.
func Guide() string { return guide }

// The one browser.
const (
	BrowserID     = "chromium"
	BrowserName   = "Chromium"
	BrowserFamily = "chromium"
	BrowserType   = "cdp"
	// AppName is the name the app allowlist admits the managed browser by.
	AppName = "Chromium"
)

// CallTimeout bounds one browser command unless the caller's context ends
// sooner.
const CallTimeout = 45 * time.Second

// browserAliases are the ids that select the managed Chromium: its own,
// and the ids models default to.
var browserAliases = map[string]bool{"chromium": true, "chrome": true, "browser": true}

// BrowserInfo is listBrowsers' entry.
type BrowserInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Family string `json:"family,omitempty"`
	Type   string `json:"type,omitempty"`
}

// TabInfo is listTabs' entry.
type TabInfo struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"`
	BrowserID string `json:"browserId,omitempty"`
}

// BrowserState is getState's browsers entry.
type BrowserState struct {
	ID     string    `json:"id"`
	Name   string    `json:"name,omitempty"`
	Family string    `json:"family,omitempty"`
	Type   string    `json:"type,omitempty"`
	Tabs   []TabInfo `json:"tabs"`
}

func chromiumInfo() BrowserInfo {
	return BrowserInfo{ID: BrowserID, Name: BrowserName, Family: BrowserFamily, Type: BrowserType}
}

// Asker is the confirm gate (policy.Gate).
type Asker interface {
	Ask(ctx context.Context, steps []protocol.Step, target string) *protocol.Error
}

// Launcher runs the browser (chromium.Browser): Conn starts it when it is
// not running; gen counts starts, so a new value means the targets of
// earlier ones are gone.
type Launcher interface {
	Conn(ctx context.Context) (conn *cdp.Conn, gen int, err error)
	Available() bool
}

// Options configure a Manager.
type Options struct {
	Browser Launcher
	Log     func(string, ...any)
	// Allowed says whether the app allowlist admits the browser; nil
	// admits.
	Allowed func() bool
	// Gate asks the user in confirm mode; nil never asks.
	Gate Asker
	// Timeout bounds one command; zero means CallTimeout.
	Timeout time.Duration
	// Import configures "Import from Chrome"; nil leaves it unavailable.
	Import *ImportConfig
}

// Manager holds each REPL session's browser state.
type Manager struct {
	opts Options

	mu       sync.Mutex
	sessions map[any]*Session
	// importMu serialises imports.
	importMu sync.Mutex
}

// NewManager makes a manager.
func NewManager(o Options) *Manager {
	return &Manager{opts: o, sessions: map[any]*Session{}}
}

// Available says whether the browser can be driven.
func (m *Manager) Available() bool {
	return m.opts.Browser != nil && m.opts.Browser.Available()
}

func (m *Manager) allowed() bool {
	return m.opts.Allowed == nil || m.opts.Allowed()
}

func (m *Manager) timeout() time.Duration {
	if m.opts.Timeout > 0 {
		return m.opts.Timeout
	}
	return CallTimeout
}

func (m *Manager) logf(format string, args ...any) {
	if m.opts.Log != nil {
		m.opts.Log(format, args...)
	}
}

func (m *Manager) session(key any, create bool) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s == nil && create {
		s = &Session{m: m, byTarget: map[string]*tabRec{}, byID: map[string]*tabRec{}, bindings: map[int]*binding{}, foreign: map[string]bool{}, lives: map[string]*lifecycle{}}
		m.sessions[key] = s
	}
	return s
}

// Reset forgets a runtime's tab bindings (js_reset): tabs and their ids
// stay, as in the reference, and the next browser call shows the browser
// documentation again.
func (m *Manager) Reset(key any) {
	if s := m.session(key, false); s != nil {
		s.mu.Lock()
		s.bindings = map[int]*binding{}
		s.docShown = false
		s.mu.Unlock()
	}
}

// TurnEnded closes the session's agent tabs that no mark keeps, as the
// reference does when a turn ends, and clears the marks.
func (m *Manager) TurnEnded(ctx context.Context, key any) {
	s := m.session(key, false)
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live() {
		for _, t := range append([]*tabRec(nil), s.tabs...) {
			if t.closed || !t.agent || t.mark != "" {
				continue
			}
			if err := s.closeTarget(ctx, t); err == nil {
				s.forget(t)
			}
		}
	}
	for _, t := range s.tabs {
		t.mark = ""
	}
}

// Close ends a session: its unmarked agent tabs close, tabs marked
// deliverable or for hand-off stay open for the user, and the session's
// hold on them is released.
func (m *Manager) Close(ctx context.Context, key any) {
	m.mu.Lock()
	s := m.sessions[key]
	delete(m.sessions, key)
	m.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live() {
		for _, t := range append([]*tabRec(nil), s.tabs...) {
			switch {
			case t.closed:
			case t.agent && t.mark == "":
				if err := s.closeTarget(ctx, t); err != nil {
					m.logf("browser: closing tab %s: %v", t.id, err)
				}
			case t.sid != "":
				cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_, _ = s.conn.Call(cctx, "", "Target.detachFromTarget", map[string]any{"sessionId": t.sid})
				cancel()
			}
		}
	}
	if s.unsub != nil {
		s.unsub()
		s.unsub = nil
	}
}

// CloseAll closes every session (daemon shutdown).
func (m *Manager) CloseAll(ctx context.Context) {
	m.mu.Lock()
	keys := make([]any, 0, len(m.sessions))
	for k := range m.sessions {
		keys = append(keys, k)
	}
	m.mu.Unlock()
	for _, k := range keys {
		m.Close(ctx, k)
	}
}

// Session is one REPL session's browser state: its connection, its agent
// window and its tab registry. Tab ids stay valid across js_reset.
type Session struct {
	m *Manager

	mu       sync.Mutex
	conn     *cdp.Conn
	gen      int // the browser start the tabs belong to
	unsub    func()
	windowID int    // the session's agent window
	name     string // sessionName, shown on the agent cursor
	nextName string

	tabs     []*tabRec
	byTarget map[string]*tabRec
	byID     map[string]*tabRec
	foreign  map[string]bool // targets of other windows, not ours
	nextTab  int

	bindings    map[int]*binding
	nextBinding int
	docShown    bool

	lifeMu sync.Mutex
	lives  map[string]*lifecycle // by CDP session id
}

// tabRec is a tab of the session.
type tabRec struct {
	id       string
	targetID string
	sid      string // the flattened CDP session; empty until attached
	title    string
	url      string
	agent    bool   // opened by the agent (or by a page of an agent tab)
	mark     string // "deliverable" or "handoff"
	closed   bool
	scale    float64 // CSS pixels per screenshot pixel
	dirty    bool    // input was sent since the last read
	mods     int     // modifier keys held down (CDP bits)
	buttons  int     // mouse buttons held down (CDP bits)
	px, py   float64 // where the pointer is
	life     *lifecycle
}

// binding is one Tab object of a runtime.
type binding struct {
	tab *tabRec
	mem *Memory
}

func (s *Session) info(t *tabRec) TabInfo {
	return TabInfo{ID: t.id, Title: t.title, URL: t.url, BrowserID: BrowserID}
}

// live says whether the session's connection is up.
func (s *Session) live() bool {
	if s.conn == nil {
		return false
	}
	select {
	case <-s.conn.Done():
		return false
	default:
		return true
	}
}

// request is a browser call from the REPL.
type request struct {
	Op          string            `json:"op"`
	ID          *string           `json:"id"`
	URL         *string           `json:"url"`
	Browser     *string           `json:"browser"`
	BrowserID   *json.RawMessage  `json:"browserId"`
	SessionName *json.RawMessage  `json:"sessionName"`
	Visible     *json.RawMessage  `json:"visible"`
	Ref         json.RawMessage   `json:"ref"`
	Binding     int               `json:"binding"`
	Method      string            `json:"method"`
	Args        []json.RawMessage `json:"args"`
	Chain       []locStep         `json:"chain"`
	Desc        string            `json:"desc"`
	Name        *string           `json:"name"`
	Profile     string            `json:"profile"`
	Replace     bool              `json:"replace"`
}

func invalid(msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.ErrInvalid, Message: msg}
}

func failed(msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.ErrTargetNotFound, Message: msg}
}

// Call answers one browser call of a REPL session.
func (m *Manager) Call(ctx context.Context, key any, args json.RawMessage) (any, *protocol.Error) {
	var req request
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, invalid("bad browser call: " + err.Error())
	}
	avail := m.Available() && m.allowed()
	switch req.Op {
	case "browsers":
		if !avail {
			return []BrowserInfo{}, nil
		}
		return []BrowserInfo{chromiumInfo()}, nil
	case "state":
		if !avail {
			return []BrowserState{}, nil
		}
		st := BrowserState{ID: BrowserID, Name: BrowserName, Family: BrowserFamily, Type: BrowserType, Tabs: []TabInfo{}}
		if s := m.session(key, false); s != nil {
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.tabs) > 0 {
				if err := s.sync(ctx); err != nil {
					return nil, s.fail(err, "")
				}
				for _, t := range s.tabs {
					st.Tabs = append(st.Tabs, s.info(t))
				}
			}
		}
		return []BrowserState{st}, nil
	case "guide":
		return guide, nil
	case "chromeProfiles":
		return m.replProfiles()
	case "importChrome":
		return m.replImport(ctx, req)
	}
	if !m.allowed() {
		return nil, &protocol.Error{Code: protocol.ErrNotAllowed, Message: AppName + " is outside the app allowlist; nothing was sent", Hint: "the user admits apps in Fennec's settings"}
	}
	if !m.Available() {
		return nil, &protocol.Error{Code: protocol.ErrUnavailable, Message: "No browser is available."}
	}
	s := m.session(key, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, &protocol.Error{Code: protocol.ErrStopped, Message: "stopped"}
	}
	switch req.Op {
	case "browser":
		return s.getBrowser(req)
	case "nameSession":
		if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
			return nil, invalid("nameSession takes a name")
		}
		s.nextName = strings.TrimSpace(*req.Name)
		if s.name == "" {
			s.name = s.nextName
		}
		return nil, nil
	case "createTab":
		return s.createTab(ctx, req)
	case "getTab":
		return s.getTab(ctx, req)
	case "listTabs":
		return s.listTabs(ctx, req)
	case "tab":
		return s.tabCall(ctx, req)
	case "loc":
		return s.locCall(ctx, req)
	}
	return nil, invalid("unknown browser call " + strconv.Quote(req.Op))
}

// doc returns the browser documentation once per runtime.
func (s *Session) doc() string {
	if s.docShown {
		return ""
	}
	s.docShown = true
	return guide
}

func resolveBrowser(id string) (string, *protocol.Error) {
	if browserAliases[strings.ToLower(strings.TrimSpace(id))] {
		return BrowserID, nil
	}
	return "", failed("Browser " + strconv.Quote(id) + " is not available; cua.listBrowsers() lists the browsers (" + BrowserID + ").")
}

func (s *Session) getBrowser(req request) (any, *protocol.Error) {
	if req.ID != nil {
		if _, perr := resolveBrowser(*req.ID); perr != nil {
			return nil, perr
		}
	}
	return map[string]any{"browserId": BrowserID, "name": BrowserName, "doc": s.doc()}, nil
}

// normalizeURL adds https:// to a URL without a scheme, as the reference
// does for createBrowserTab.
func normalizeURL(u string) string {
	if p, err := url.Parse(u); err == nil && p.Scheme != "" && (p.Opaque != "" || p.Host != "" || p.Scheme == "about" || p.Scheme == "data" || p.Scheme == "file") {
		return u
	}
	return "https://" + u
}

// ensureConn connects to the browser, starting it when needed. After a
// restart the session's tabs are gone with the browser that held them.
func (s *Session) ensureConn(ctx context.Context) error {
	conn, gen, err := s.m.opts.Browser.Conn(ctx)
	if err != nil {
		return err
	}
	if s.conn == conn && s.gen == gen {
		return nil
	}
	if s.unsub != nil {
		s.unsub()
	}
	for _, t := range s.tabs {
		t.closed = true
	}
	s.tabs = nil
	s.foreign = map[string]bool{}
	s.windowID = 0
	s.conn, s.gen = conn, gen
	s.lifeMu.Lock()
	s.lives = map[string]*lifecycle{}
	s.lifeMu.Unlock()
	s.unsub = conn.Subscribe(s.event)
	return nil
}

// event records page lifecycle events of the session's tabs.
func (s *Session) event(e cdp.Event) {
	if e.Method != "Page.lifecycleEvent" || e.SessionID == "" {
		return
	}
	s.lifeMu.Lock()
	l := s.lives[e.SessionID]
	s.lifeMu.Unlock()
	if l == nil {
		return
	}
	var p struct {
		FrameID  string `json:"frameId"`
		LoaderID string `json:"loaderId"`
		Name     string `json:"name"`
	}
	if json.Unmarshal(e.Params, &p) == nil {
		l.note(p.FrameID, p.LoaderID, p.Name)
	}
}

// call sends a browser-level command.
func (s *Session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !s.live() {
		if err := s.ensureConn(ctx); err != nil {
			return nil, err
		}
	}
	cctx, cancel := context.WithTimeout(ctx, s.m.timeout())
	defer cancel()
	return s.conn.Call(cctx, "", method, params)
}

// attach opens a flattened session on a tab and turns on the events the
// package reads.
func (s *Session) attach(ctx context.Context, t *tabRec) error {
	if t.sid != "" {
		return nil
	}
	v, err := s.call(ctx, "Target.attachToTarget", map[string]any{"targetId": t.targetID, "flatten": true})
	if err != nil {
		if isGone(err) {
			s.forget(t)
			return errTabGone
		}
		return err
	}
	var r struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(v, &r)
	if r.SessionID == "" {
		return errors.New("the tab could not be attached")
	}
	t.sid = r.SessionID
	t.life = newLifecycle()
	s.lifeMu.Lock()
	s.lives[t.sid] = t.life
	s.lifeMu.Unlock()
	for _, m := range []string{"Page.enable", "Page.setLifecycleEventsEnabled"} {
		var params any
		if m == "Page.setLifecycleEventsEnabled" {
			params = map[string]any{"enabled": true}
		}
		if _, err := s.cdp(ctx, t, m, params); err != nil {
			return err
		}
	}
	return nil
}

var errTabGone = errors.New("the tab is gone")

// isGone says whether the browser answered that a target or its session
// no longer exists.
func isGone(err error) bool {
	var ce *cdp.Error
	if !errors.As(err, &ce) {
		return false
	}
	msg := strings.ToLower(ce.Message)
	return strings.Contains(msg, "no target with given id") || strings.Contains(msg, "session with given id not found") || strings.Contains(msg, "target closed")
}

// cdp sends a command to a tab.
func (s *Session) cdp(ctx context.Context, t *tabRec, method string, params any) (json.RawMessage, error) {
	if t.closed {
		return nil, errTabGone
	}
	if !s.live() {
		if err := s.ensureConn(ctx); err != nil {
			return nil, err
		}
		if t.closed {
			return nil, errTabGone
		}
	}
	if t.sid == "" {
		if err := s.attach(ctx, t); err != nil {
			return nil, err
		}
	}
	cctx, cancel := context.WithTimeout(ctx, s.m.timeout())
	defer cancel()
	v, err := s.conn.Call(cctx, t.sid, method, params)
	if err != nil && isGone(err) {
		s.forget(t)
		return nil, errTabGone
	}
	return v, err
}

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	OpenerID string `json:"openerId"`
}

// sync reads the browser's pages into the registry: the session's own
// tabs, pages its tabs opened (popups, target=_blank), and tabs the user
// opened in the session's window.
func (s *Session) sync(ctx context.Context) error {
	if err := s.ensureConn(ctx); err != nil {
		return err
	}
	v, err := s.call(ctx, "Target.getTargets", nil)
	if err != nil {
		return err
	}
	var r struct {
		TargetInfos []targetInfo `json:"targetInfos"`
	}
	if err := json.Unmarshal(v, &r); err != nil {
		return err
	}
	seen := map[string]bool{}
	pages := map[string]targetInfo{}
	for _, it := range r.TargetInfos {
		if it.Type == "page" {
			pages[it.TargetID] = it
		}
	}
	// Openers first: a popup of a popup joins once its opener has.
	for changed := true; changed; {
		changed = false
		for id, it := range pages {
			if s.byTarget[id] != nil && !s.byTarget[id].closed || s.foreign[id] {
				continue
			}
			if op := s.byTarget[it.OpenerID]; op != nil && !op.closed {
				s.add(it, op.agent)
				changed = true
			}
		}
	}
	for id, it := range pages {
		if (s.byTarget[id] != nil && !s.byTarget[id].closed) || s.foreign[id] {
			continue
		}
		if s.windowID == 0 {
			s.foreign[id] = true
			continue
		}
		if w, err := s.windowOf(ctx, id); err == nil && w == s.windowID {
			s.add(it, false)
		} else {
			s.foreign[id] = true
		}
	}
	for id, it := range pages {
		seen[id] = true
		if t := s.byTarget[id]; t != nil {
			t.title, t.url = it.Title, it.URL
		}
	}
	for _, t := range s.tabs {
		if !seen[t.targetID] {
			t.closed = true
		}
	}
	for id := range s.foreign {
		if !seen[id] {
			delete(s.foreign, id)
		}
	}
	kept := s.tabs[:0]
	for _, t := range s.tabs {
		if !t.closed {
			kept = append(kept, t)
		}
	}
	s.tabs = kept
	if len(s.tabs) == 0 {
		s.windowID = 0
	}
	return nil
}

func (s *Session) add(it targetInfo, agent bool) *tabRec {
	s.nextTab++
	t := &tabRec{id: strconv.Itoa(s.nextTab), targetID: it.TargetID, title: it.Title, url: it.URL, agent: agent, scale: 1}
	s.byTarget[it.TargetID] = t
	s.byID[t.id] = t
	s.tabs = append(s.tabs, t)
	return t
}

func (s *Session) windowOf(ctx context.Context, targetID string) (int, error) {
	v, err := s.call(ctx, "Browser.getWindowForTarget", map[string]any{"targetId": targetID})
	if err != nil {
		return 0, err
	}
	var w struct {
		WindowID int `json:"windowId"`
	}
	_ = json.Unmarshal(v, &w)
	return w.WindowID, nil
}

func (s *Session) forget(t *tabRec) {
	t.closed = true
	kept := s.tabs[:0]
	for _, x := range s.tabs {
		if x != t {
			kept = append(kept, x)
		}
	}
	s.tabs = kept
	if t.sid != "" {
		s.lifeMu.Lock()
		delete(s.lives, t.sid)
		s.lifeMu.Unlock()
	}
	if len(s.tabs) == 0 {
		s.windowID = 0
	}
}

// closeTarget closes a tab in the browser.
func (s *Session) closeTarget(ctx context.Context, t *tabRec) error {
	_, err := s.call(ctx, "Target.closeTarget", map[string]any{"targetId": t.targetID})
	if err != nil && isGone(err) {
		return nil
	}
	return err
}

func (s *Session) bind(t *tabRec) int {
	s.nextBinding++
	s.bindings[s.nextBinding] = &binding{tab: t, mem: NewMemory()}
	return s.nextBinding
}

// fail maps an error from the browser onto a REPL error; secret is text
// the message must not repeat.
func (s *Session) fail(err error, secret string) *protocol.Error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &protocol.Error{Code: protocol.ErrStopped, Message: "stopped"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: protocol.ErrTimeout, Message: "Chromium did not answer in time; the action may have been sent, so read the state again before retrying"}
	}
	msg := err.Error()
	switch {
	case errors.Is(err, cdp.ErrClosed):
		msg = "Chromium stopped; the action may have been sent. Its tabs closed with it: the next call starts it again, then open a fresh tab with cua.createBrowserTab()"
	case errors.Is(err, errTabGone):
		msg = "the tab was closed; get or create a fresh tab from this browser."
	}
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "[text]")
	}
	return &protocol.Error{Code: protocol.ErrInternal, Message: msg}
}

func decodeOptString(raw *json.RawMessage, what string) (*string, *protocol.Error) {
	if raw == nil || string(*raw) == "null" {
		return nil, nil
	}
	var v string
	if json.Unmarshal(*raw, &v) != nil {
		return nil, invalid(what + " must be a string")
	}
	return &v, nil
}

// newTab opens a page: the session's first in a new agent window (in the
// background), later ones in that window.
func (s *Session) newTab(ctx context.Context) (*tabRec, error) {
	if err := s.sync(ctx); err != nil {
		return nil, err
	}
	params := map[string]any{"url": "about:blank", "background": true}
	if s.windowID == 0 {
		params["newWindow"] = true
	}
	v, err := s.call(ctx, "Target.createTarget", params)
	if err != nil {
		return nil, err
	}
	var r struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(v, &r); err != nil || r.TargetID == "" {
		return nil, errors.New("the browser did not open a tab")
	}
	t := s.add(targetInfo{TargetID: r.TargetID, URL: "about:blank"}, true)
	if s.windowID == 0 {
		if w, err := s.windowOf(ctx, r.TargetID); err == nil {
			s.windowID = w
		}
	}
	if err := s.attach(ctx, t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Session) createTab(ctx context.Context, req request) (any, *protocol.Error) {
	bid, perr := decodeOptString(req.BrowserID, "browserId")
	if perr != nil || bid == nil || strings.TrimSpace(*bid) == "" {
		return nil, invalid("createBrowserTab requires a browser ID. Select one with cua.getBrowser().")
	}
	if _, perr := resolveBrowser(*bid); perr != nil {
		return nil, perr
	}
	name, perr := decodeOptString(req.SessionName, "sessionName")
	if perr != nil {
		return nil, perr
	}
	if req.Visible != nil && string(*req.Visible) != "null" {
		var b bool
		if json.Unmarshal(*req.Visible, &b) != nil {
			return nil, invalid("visible must be true or false")
		}
	}
	target := "about:blank"
	if req.URL != nil && *req.URL != "" {
		target = normalizeURL(*req.URL)
	}
	if perr := s.ask(ctx, protocol.Step{Launch: target}, BrowserName); perr != nil {
		return nil, perr
	}
	if name != nil && strings.TrimSpace(*name) != "" {
		s.name = strings.TrimSpace(*name)
	} else if s.name == "" {
		s.name = s.nextName
	}
	t, err := s.newTab(ctx)
	if err != nil {
		return nil, s.fail(err, "")
	}
	if err := s.navigate(ctx, t, target); err != nil {
		return nil, s.fail(err, "")
	}
	b := s.bind(t)
	text, perr := s.readState(ctx, s.bindings[b], true, false)
	if perr != nil {
		return nil, perr
	}
	return map[string]any{"id": t.id, "binding": b, "doc": s.doc(), "text": text}, nil
}

var newTabURLs = map[string]bool{"chrome://newtab": true, "chrome://newtab/": true, "chrome://new-tab-page": true, "chrome://new-tab-page/": true}

func (s *Session) getTab(ctx context.Context, req request) (any, *protocol.Error) {
	if req.Browser != nil {
		if _, perr := resolveBrowser(*req.Browser); perr != nil {
			return nil, perr
		}
	}
	var id string
	var byURL *string
	switch {
	case len(req.Ref) == 0 || string(req.Ref) == "null":
		return nil, invalid("getTab requires a tab reference")
	case json.Unmarshal(req.Ref, &id) == nil:
		if id == "" {
			return nil, invalid("getTab requires a tab reference")
		}
	default:
		var ref struct {
			URL     *string `json:"url"`
			Mention *string `json:"mention"`
		}
		if json.Unmarshal(req.Ref, &ref) != nil || (ref.URL == nil && ref.Mention == nil) {
			return nil, invalid("getTab takes a tab id, { url } or { mention }")
		}
		if ref.Mention != nil {
			return nil, invalid("Tab mentions are not available in " + BrowserName + "; list its tabs with cua.listTabs() and pass a tab id.")
		}
		if u, err := url.Parse(*ref.URL); err != nil || u.Scheme == "" {
			return nil, invalid("getTab requires an absolute URL.")
		}
		if req.Browser == nil {
			return nil, invalid("getTab({ url }) requires an explicit browser.")
		}
		byURL = ref.URL
	}
	if len(s.tabs) == 0 {
		return nil, failed("Tab not found in browser " + BrowserID + ".")
	}
	if err := s.sync(ctx); err != nil {
		return nil, s.fail(err, "")
	}
	var hits []*tabRec
	for _, t := range s.tabs {
		if (byURL == nil && (t.id == id || t.targetID == id)) || (byURL != nil && t.url == *byURL) {
			hits = append(hits, t)
		}
	}
	if len(hits) == 0 {
		return nil, failed("Tab not found in browser " + BrowserID + ".")
	}
	if len(hits) > 1 {
		infos := make([]TabInfo, len(hits))
		for i, t := range hits {
			infos[i] = s.info(t)
		}
		data, _ := json.Marshal(infos)
		return nil, &protocol.Error{Code: protocol.ErrTargetAmbiguous, Message: "Multiple tabs match the reference in browser " + BrowserID + ": " + string(data)}
	}
	t := hits[0]
	b := s.bind(t)
	out := map[string]any{"id": t.id, "binding": b, "doc": s.doc()}
	if newTabURLs[t.url] {
		data, _ := json.Marshal(s.info(t))
		out["text"] = string(data)
		return out, nil
	}
	text, perr := s.readState(ctx, s.bindings[b], true, false)
	if perr != nil {
		return nil, perr
	}
	out["text"] = text
	return out, nil
}

func (s *Session) listTabs(ctx context.Context, req request) (any, *protocol.Error) {
	doc := ""
	if req.Browser != nil {
		if _, perr := resolveBrowser(*req.Browser); perr != nil {
			return nil, perr
		}
		doc = s.doc()
	}
	tabs := []TabInfo{}
	if len(s.tabs) > 0 {
		if err := s.sync(ctx); err != nil {
			return nil, s.fail(err, "")
		}
		for _, t := range s.tabs {
			tabs = append(tabs, s.info(t))
		}
	}
	sort.SliceStable(tabs, func(i, j int) bool {
		a, _ := strconv.Atoi(tabs[i].ID)
		b, _ := strconv.Atoi(tabs[j].ID)
		return a < b
	})
	return map[string]any{"tabs": tabs, "doc": doc}, nil
}

// ask passes a step through the confirm gate.
func (s *Session) ask(ctx context.Context, step protocol.Step, target string) *protocol.Error {
	if s.m.opts.Gate == nil {
		return nil
	}
	return s.m.opts.Gate.Ask(ctx, []protocol.Step{step}, target)
}

// cursorLabel prefixes an action with the session's name for the agent
// cursor.
func (s *Session) cursorLabel(action string) string {
	if s.name == "" {
		return action
	}
	return fmt.Sprintf("%s · %s", s.name, action)
}
