package ego

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/protocol"
)

//go:embed guide.md
var guide string

// Guide is the browser documentation the first browser call of a runtime
// shows.
func Guide() string { return guide }

// The one browser.
const (
	BrowserID     = "ego"
	BrowserName   = "ego lite"
	BrowserFamily = "chromium"
	BrowserType   = "cdp"
	// AppName is the name the app allowlist admits ego lite by.
	AppName = "ego lite"
)

// browserAliases are the ids that select ego lite: its own, and the ids
// models default to.
var browserAliases = map[string]bool{"ego": true, "ego lite": true, "ego-lite": true, "chrome": true, "browser": true, "chromium": true}

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

func egoInfo() BrowserInfo {
	return BrowserInfo{ID: BrowserID, Name: BrowserName, Family: BrowserFamily, Type: BrowserType}
}

// Asker is the confirm gate (policy.Gate).
type Asker interface {
	Ask(ctx context.Context, steps []protocol.Step, target string) *protocol.Error
}

// Options configure a Manager.
type Options struct {
	// Bin is ego-browser; empty means FindBin.
	Bin string
	// Dir holds bridge sockets and screenshots; keep it short.
	Dir string
	// Start replaces the bridge starter (tests).
	Start Starter
	Log   func(string, ...any)
	// Allowed says whether the app allowlist admits ego lite; nil admits.
	Allowed func() bool
	// Gate asks the user in confirm mode; nil never asks.
	Gate Asker
	// Timeout bounds one bridge request; zero means CallTimeout.
	Timeout time.Duration
}

// FindBin locates ego-browser: FENNEC_CUA_EGO ("off" turns browsers
// off), the PATH, then ego lite's per-user install.
func FindBin() string {
	if v := os.Getenv("FENNEC_CUA_EGO"); v != "" {
		if v == "off" {
			return ""
		}
		return v
	}
	if p, err := exec.LookPath("ego-browser"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "ego-browser")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// Manager holds each REPL session's browser state.
type Manager struct {
	opts Options

	mu       sync.Mutex
	sessions map[any]*Session
	names    map[string]bool
}

// NewManager makes a manager.
func NewManager(o Options) *Manager {
	return &Manager{opts: o, sessions: map[any]*Session{}, names: map[string]bool{}}
}

// Available says whether ego lite can be driven.
func (m *Manager) Available() bool {
	if m.opts.Start != nil {
		return true
	}
	if m.opts.Bin == "" {
		return false
	}
	_, err := os.Stat(m.opts.Bin)
	return err == nil
}

func (m *Manager) allowed() bool {
	return m.opts.Allowed == nil || m.opts.Allowed()
}

func (m *Manager) session(key any, create bool) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s == nil && create {
		start := m.opts.Start
		if start == nil && m.opts.Bin != "" {
			start = ExecStarter(m.opts.Bin, m.opts.Log)
		}
		s = &Session{m: m, client: &Client{Dir: m.opts.Dir, Start: start, Log: m.opts.Log, Timeout: m.opts.Timeout},
			byTarget: map[string]*tabRec{}, byID: map[string]*tabRec{}, bindings: map[int]*binding{}}
		m.sessions[key] = s
	}
	return s
}

// claimName reserves a task space name for one session.
func (m *Manager) claimName(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cand := name
	for i := 2; m.names[cand]; i++ {
		cand = fmt.Sprintf("%s (%d)", name, i)
	}
	m.names[cand] = true
	return cand
}

func (m *Manager) releaseName(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.names, name)
}

// Reset forgets a runtime's tab bindings (js_reset): tabs, their ids and
// the task space stay, as in the reference, and the next browser call shows
// the browser documentation again.
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
	if s.spaceID == 0 || s.handedOff || !s.client.Live() {
		for _, t := range s.tabs {
			t.mark = ""
		}
		return
	}
	for _, t := range s.tabs {
		if t.closed || !t.agent || t.mark != "" || t.label == "" {
			continue
		}
		if _, err := s.page(ctx, t, "close"); err == nil {
			s.forget(t)
		}
	}
	for _, t := range s.tabs {
		t.mark = ""
	}
}

// Close ends a session: its task space is finished keeping only the tabs
// marked deliverable or for hand-off (a space handed to the user is left
// to the user), and its bridge stops.
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
	if s.spaceID != 0 && !s.handedOff && s.client.Live() {
		keep := []string{}
		for _, t := range s.tabs {
			if !t.closed && t.mark != "" && t.label != "" {
				keep = append(keep, t.label)
			}
		}
		if _, err := s.task(ctx, "finish", map[string]any{"keep": keep}); err != nil && m.opts.Log != nil {
			m.opts.Log("ego: finishing space %d: %v", s.spaceID, err)
		}
	}
	if s.spaceName != "" {
		m.releaseName(s.spaceName)
	}
	s.client.Close()
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

// Session is one REPL session's browser state: its bridge, its task space
// and its tab registry. Tab ids stay valid across js_reset.
type Session struct {
	m      *Manager
	client *Client

	mu        sync.Mutex
	spaceID   int
	spaceName string
	gen       int  // the bridge generation the space was opened under
	handedOff bool // the space was handed to the user
	p1Fresh   bool // the new space's first page is still unused
	nextName  string

	tabs     []*tabRec
	byTarget map[string]*tabRec
	byID     map[string]*tabRec
	nextTab  int

	bindings    map[int]*binding
	nextBinding int
	docShown    bool
}

// tabRec is a tab of the session's space.
type tabRec struct {
	id       string
	label    string // the SDK page label; empty while unmanaged
	targetID string
	title    string
	url      string
	agent    bool   // opened by the agent
	mark     string // "deliverable" or "handoff"
	closed   bool
	scale    float64 // CSS pixels per screenshot pixel
	dirty    bool    // input was sent since the last read
}

// binding is one Tab object of a runtime.
type binding struct {
	tab *tabRec
	mem *Memory
}

func (s *Session) info(t *tabRec) TabInfo {
	return TabInfo{ID: t.id, Title: t.title, URL: t.url, BrowserID: BrowserID}
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
		return []BrowserInfo{egoInfo()}, nil
	case "state":
		if !avail {
			return []BrowserState{}, nil
		}
		st := BrowserState{ID: BrowserID, Name: BrowserName, Family: BrowserFamily, Type: BrowserType, Tabs: []TabInfo{}}
		if s := m.session(key, false); s != nil {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.spaceID != 0 {
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
		if s.spaceID == 0 {
			s.nextName = strings.TrimSpace(*req.Name)
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

// ensureSpace opens the session's task space: a new agent-owned space
// named after the session, never a user's.
func (s *Session) ensureSpace(ctx context.Context, name string) error {
	if s.spaceID != 0 {
		return s.reopen(ctx)
	}
	if name == "" {
		name = s.nextName
	}
	if name == "" {
		name = "fennec-cua"
	}
	base := name
	for attempt := 0; attempt < 6; attempt++ {
		cand := s.m.claimName(base)
		v, gen, err := s.client.Do(ctx, "space", map[string]any{"name": cand})
		if err != nil {
			s.m.releaseName(cand)
			var re *RemoteError
			if errors.As(err, &re) && (re.Code == "EGO_TASK_SPACE_NAME_IN_USE" || strings.Contains(re.Message, "EGO_TASK_SPACE_NAME_IN_USE") || strings.Contains(re.Message, "in use")) {
				base = fmt.Sprintf("%s (%d)", name, attempt+2)
				continue
			}
			return err
		}
		var info struct {
			SpaceID   int    `json:"spaceId"`
			Name      string `json:"name"`
			Ownership string `json:"ownership"`
		}
		_ = json.Unmarshal(v, &info)
		if info.Ownership != "" && info.Ownership != "agent" {
			// Never work in a space that is not the agent's.
			s.m.releaseName(cand)
			base = fmt.Sprintf("%s (%d)", name, attempt+2)
			continue
		}
		s.spaceID, s.spaceName, s.gen, s.p1Fresh = info.SpaceID, cand, gen, true
		return nil
	}
	return errors.New("no free task space name for " + strconv.Quote(name))
}

// reopen opens the space again in a restarted bridge, and takes it back
// from the user after a hand-off.
func (s *Session) reopen(ctx context.Context) error {
	if s.handedOff {
		if _, gen, err := s.client.Do(ctx, "takeover", map[string]any{"spaceId": s.spaceID}); err != nil {
			return fmt.Errorf("the task space was handed to the user and could not be taken back: %w", err)
		} else {
			s.gen = gen
		}
		s.handedOff = false
		return nil
	}
	if s.client.Live() && s.client.Gen() == s.gen {
		return nil
	}
	_, gen, err := s.client.Do(ctx, "resume", map[string]any{"spaceId": s.spaceID})
	if err != nil {
		return err
	}
	s.gen = gen
	return nil
}

// task calls a TaskSpace method.
func (s *Session) task(ctx context.Context, method string, args ...any) (json.RawMessage, error) {
	if err := s.reopen(ctx); err != nil {
		return nil, err
	}
	if args == nil {
		args = []any{}
	}
	v, _, err := s.client.Do(ctx, "call", map[string]any{"spaceId": s.spaceID, "method": method, "args": args})
	return v, err
}

// page calls a Page method of a tab, adopting an unmanaged tab first.
func (s *Session) page(ctx context.Context, t *tabRec, method string, args ...any) (json.RawMessage, error) {
	if err := s.reopen(ctx); err != nil {
		return nil, err
	}
	if t.label == "" {
		v, _, err := s.client.Do(ctx, "adopt", map[string]any{"spaceId": s.spaceID, "targetId": t.targetID})
		if err != nil {
			return nil, err
		}
		var a struct {
			Label string `json:"label"`
		}
		_ = json.Unmarshal(v, &a)
		if a.Label == "" {
			return nil, errors.New("the tab could not be adopted")
		}
		t.label = a.Label
	}
	if args == nil {
		args = []any{}
	}
	v, _, err := s.client.Do(ctx, "call", map[string]any{"spaceId": s.spaceID, "page": t.label, "method": method, "args": args})
	return v, err
}

// cdp sends a CDP command through a tab's page.
func (s *Session) cdp(ctx context.Context, t *tabRec, method string, params any) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	return s.page(ctx, t, "cdp", method, params)
}

// sync reads the space's tabs into the registry.
func (s *Session) sync(ctx context.Context) error {
	v, err := s.task(ctx, "tabs")
	if err != nil {
		return err
	}
	var list []struct {
		Label    string `json:"label"`
		TargetID string `json:"targetId"`
		Title    string `json:"title"`
		URL      string `json:"url"`
		OpenedBy string `json:"openedBy"`
	}
	if err := json.Unmarshal(v, &list); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, it := range list {
		seen[it.TargetID] = true
		t := s.byTarget[it.TargetID]
		if t == nil {
			s.nextTab++
			t = &tabRec{id: strconv.Itoa(s.nextTab), targetID: it.TargetID, agent: it.OpenedBy == "agent", scale: 1}
			s.byTarget[it.TargetID] = t
			s.byID[t.id] = t
			s.tabs = append(s.tabs, t)
		}
		if it.Label != "" {
			t.label = it.Label
		}
		t.title, t.url = it.Title, it.URL
	}
	for _, t := range s.tabs {
		if !seen[t.targetID] {
			t.closed = true
		}
	}
	kept := s.tabs[:0]
	for _, t := range s.tabs {
		if !t.closed {
			kept = append(kept, t)
		}
	}
	s.tabs = kept
	return nil
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
}

func (s *Session) bind(t *tabRec) int {
	s.nextBinding++
	s.bindings[s.nextBinding] = &binding{tab: t, mem: NewMemory()}
	return s.nextBinding
}

// fail maps an error from the bridge onto a REPL error; secret is text the
// message must not repeat.
func (s *Session) fail(err error, secret string) *protocol.Error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &protocol.Error{Code: protocol.ErrStopped, Message: "stopped"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: protocol.ErrTimeout, Message: "ego lite did not answer in time; the action may have been sent, so read the state again before retrying"}
	}
	msg := err.Error()
	if errors.Is(err, ErrBridgeGone) {
		msg = "the ego lite bridge stopped; the action may have been sent. Read the tab's state again; the next call starts a new bridge"
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
	sname := ""
	if name != nil {
		sname = strings.TrimSpace(*name)
	}
	if err := s.ensureSpace(ctx, sname); err != nil {
		return nil, s.fail(err, "")
	}
	label := ""
	if s.p1Fresh {
		label = "p1"
		s.p1Fresh = false
	} else {
		v, err := s.task(ctx, "newPage")
		if err != nil {
			return nil, s.fail(err, "")
		}
		var p struct {
			Label string `json:"label"`
		}
		_ = json.Unmarshal(v, &p)
		label = p.Label
	}
	if err := s.sync(ctx); err != nil {
		return nil, s.fail(err, "")
	}
	var t *tabRec
	for _, x := range s.tabs {
		if x.label == label {
			t = x
		}
	}
	if t == nil {
		return nil, s.fail(errors.New("the new tab did not appear in the task space"), "")
	}
	t.agent = true
	if _, err := s.page(ctx, t, "goto", target); err != nil {
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
	if s.spaceID == 0 {
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
	if s.spaceID != 0 {
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
