// Package fakedesk is an in-memory actions.Backend for tests: windows,
// element trees, hit tests, focus answers and a recording fake cua-driver.
// It never touches the real desktop.
package fakedesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/driver"
	"fennec/cua/internal/view"
)

// Call is one recorded driver call.
type Call struct {
	Tool string
	Args map[string]any
	// Budget is the time left on the call's context, zero without a deadline.
	Budget time.Duration
}

// Arg reads an argument as a string ("" when absent).
func (c Call) Arg(name string) string {
	v, ok := c.Args[name]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Reply is a driver answer: a raw JSON result or an error.
type Reply struct {
	Raw string
	Err error
}

// OK is a confirmed reply.
var OK = Reply{Raw: `{"effect":"confirmed"}`}

// Unverified is a sent but unproven reply with a note.
func Unverified(note string) Reply {
	b, _ := json.Marshal(map[string]any{"effect": "unverifiable", "note": note})
	return Reply{Raw: string(b)}
}

// Refused is a refusal that sent nothing.
func Refused(tool, code, msg string) Reply {
	b, _ := json.Marshal(map[string]any{"status": "refused", "code": code, "refusal": map[string]string{"code": code, "message": msg}})
	return Reply{Err: &driver.Error{Tool: tool, Message: msg, Result: b}}
}

// Failed is a failure after the input may have been sent.
func Failed(tool, msg string) Reply {
	return Reply{Err: &driver.Error{Tool: tool, Message: msg}}
}

// Hit is a hit test answer by handle ids.
type Hit struct {
	Node    string
	Path    []string // ancestors, innermost first
	Win     string   // window key
	Covered string   // the window key on top, when another
}

// HitCall is a recorded hit-checked click.
type HitCall struct {
	Win   string
	Node  string
	Count int
}

// Desk is the fake desktop.
type Desk struct {
	mu sync.Mutex

	Wins  []view.RawWindow
	Trees map[string][]view.RawNode // window key -> walk; handles are string ids
	Hits  map[[2]int]Hit
	// HitErr fails every hit test.
	HitErr error
	// Focus answers Focused per pid in turn; the last answer repeats.
	Focus map[int][]*view.RawNode
	// FocusErr fails focus reads; FocusHang makes them block until the
	// context ends.
	FocusErr  error
	FocusHang bool
	ReadErr   error
	// PerformErr fails Perform by "id" or "id:action".
	PerformErr map[string]error
	SetErr     error
	Locked     bool
	Chromiums  map[int]bool
	NoDriver   bool
	NoLocked   bool
	// Replies answers driver tools in turn; the last repeats; OK by default.
	Replies    map[string][]Reply
	HitReplies []Reply
	// NoRaise leaves the front window as it is when raising.
	NoRaise bool
	// AfterList runs after each window listing (tests that move windows).
	AfterList func(d *Desk, n int)
	ScaleV    float64

	Calls      []Call
	HitCalls   []HitCall
	Performs   []string // "id:action"
	Ranges     []string // "id:loc:len" text selections
	Selected   []string
	FocusedIDs []string // handles given the keyboard focus, in order
	Values     map[string]string
	Raised     []string
	Closed     []string
	ListReads  int
	Focuses    int
	Ancestry   int
}

// New makes an empty desk.
func New() *Desk {
	return &Desk{Trees: map[string][]view.RawNode{}, Hits: map[[2]int]Hit{}, Focus: map[int][]*view.RawNode{}, PerformErr: map[string]error{}, Chromiums: map[int]bool{}, Replies: map[string][]Reply{}, Values: map[string]string{}}
}

// Win adds a window, front to back in the order added, with its root node.
func (d *Desk) Win(key, app, title string, pid int, b [4]int, active bool) *view.RawWindow {
	bb := b
	d.Wins = append(d.Wins, view.RawWindow{Key: key, Handle: key, App: app, Title: title, PID: pid, Role: "window", Active: active, Bounds: &bb})
	d.Trees[key] = []view.RawNode{{Handle: key, Key: key, Parent: -1, Role: "window", Name: title, Bounds: &bb}}
	return &d.Wins[len(d.Wins)-1]
}

// Window returns a window by key for changes.
func (d *Desk) Window(key string) *view.RawWindow {
	for i := range d.Wins {
		if d.Wins[i].Key == key {
			return &d.Wins[i]
		}
	}
	return nil
}

// Add adds an element under parent (a handle id; the window key for its
// root) in window win.
func (d *Desk) Add(win, id, role, name string, b *[4]int, parent string, states ...string) {
	nodes := d.Trees[win]
	p := 0
	for i, n := range nodes {
		if n.Handle == parent {
			p = i
		}
	}
	d.Trees[win] = append(nodes, view.RawNode{Handle: id, Key: id, Parent: p, Depth: nodes[p].Depth + 1, Role: role, Name: name, Bounds: b, States: states})
}

// Box is a frame.
func Box(x, y, w, h int) *[4]int { return &[4]int{x, y, w, h} }

func (d *Desk) find(id string) (string, int) {
	for k, nodes := range d.Trees {
		for i, n := range nodes {
			if n.Handle == id {
				return k, i
			}
		}
	}
	return "", -1
}

// Node returns a node by id for changes.
func (d *Desk) Node(id string) *view.RawNode {
	k, i := d.find(id)
	if i < 0 {
		return nil
	}
	return &d.Trees[k][i]
}

// Tools lists the driver tools called, in order.
func (d *Desk) Tools() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.Calls {
		out = append(out, c.Tool)
	}
	return out
}

// Windows implements view.Source.
func (d *Desk) Windows(ctx context.Context) ([]view.RawWindow, error) {
	d.mu.Lock()
	d.ListReads++
	n := d.ListReads
	out := make([]view.RawWindow, len(d.Wins))
	copy(out, d.Wins)
	hook := d.AfterList
	d.mu.Unlock()
	if hook != nil {
		hook(d, n)
	}
	return out, nil
}

// Tree implements view.Source.
func (d *Desk) Tree(ctx context.Context, w view.RawWindow, opts view.TreeOptions) (view.Tree, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	nodes, ok := d.Trees[w.Key]
	if !ok {
		return view.Tree{}, errors.New("no such window")
	}
	return view.Tree{Window: w, Nodes: append([]view.RawNode(nil), nodes...)}, nil
}

// Read implements view.Source.
func (d *Desk) Read(ctx context.Context, h view.Handle, full, tail bool) (view.RawNode, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ReadErr != nil {
		return view.RawNode{}, false, d.ReadErr
	}
	id, _ := h.(string)
	k, i := d.find(id)
	if i < 0 {
		return view.RawNode{}, false, errors.New("gone")
	}
	return d.Trees[k][i], false, nil
}

// At implements view.Source.
func (d *Desk) At(ctx context.Context, x, y int) (*view.RawNode, []view.RawNode, *view.RawWindow, *view.RawWindow, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.HitErr != nil {
		return nil, nil, nil, nil, d.HitErr
	}
	h, ok := d.Hits[[2]int{x, y}]
	if !ok {
		return nil, nil, nil, nil, nil
	}
	var node *view.RawNode
	if k, i := d.find(h.Node); i >= 0 {
		n := d.Trees[k][i]
		node = &n
	}
	var path []view.RawNode
	for _, p := range h.Path {
		if k, i := d.find(p); i >= 0 {
			path = append(path, d.Trees[k][i])
		}
	}
	win, cov := d.Window(h.Win), d.Window(h.Covered)
	var wc, cc *view.RawWindow
	if win != nil {
		w := *win
		wc = &w
	}
	if cov != nil {
		c := *cov
		cc = &c
	}
	return node, path, wc, cc, nil
}

// Focused implements view.Source.
func (d *Desk) Focused(ctx context.Context, pid int) (*view.RawNode, error) {
	d.mu.Lock()
	d.Focuses++
	hang, ferr := d.FocusHang, d.FocusErr
	var n *view.RawNode
	if q := d.Focus[pid]; len(q) > 0 {
		n = q[0]
		if len(q) > 1 {
			d.Focus[pid] = q[1:]
		}
	}
	d.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if ferr != nil {
		return nil, ferr
	}
	if n == nil {
		return nil, nil
	}
	cp := *n
	return &cp, nil
}

// Perform implements actions.Backend.
func (d *Desk) Perform(ctx context.Context, h view.Handle, action string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, _ := h.(string)
	d.Performs = append(d.Performs, id+":"+action)
	if err := d.PerformErr[id+":"+action]; err != nil {
		return err
	}
	if err := d.PerformErr[id]; err != nil {
		return err
	}
	if k, i := d.find(id); i >= 0 && action == "AXPress" {
		n := &d.Trees[k][i]
		if n.Role == "checkbox" || n.Role == "switch" || n.Role == "menuitem" {
			toggle(n, "checked")
		}
	}
	return nil
}

// ActionNames implements actions.Backend: a node's Actions as stored.
func (d *Desk) ActionNames(ctx context.Context, h view.Handle) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, _ := h.(string)
	if k, i := d.find(id); i >= 0 {
		return append([]string{"AXPress"}, d.Trees[k][i].Actions...), nil
	}
	return nil, errors.New("no such element")
}

// SelectRange implements actions.Backend; it records "id:loc:len".
func (d *Desk) SelectRange(ctx context.Context, h view.Handle, loc, length int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, _ := h.(string)
	d.Ranges = append(d.Ranges, fmt.Sprintf("%s:%d:%d", id, loc, length))
	return d.SetErr
}

func toggle(n *view.RawNode, state string) {
	for i, s := range n.States {
		if s == state {
			n.States = append(n.States[:i], n.States[i+1:]...)
			return
		}
	}
	n.States = append(n.States, state)
}

// SetValue implements actions.Backend.
func (d *Desk) SetValue(ctx context.Context, h view.Handle, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.SetErr != nil {
		return d.SetErr
	}
	id, _ := h.(string)
	d.Values[id] = value
	if k, i := d.find(id); i >= 0 {
		v := value
		d.Trees[k][i].Value = &v
	}
	return nil
}

// Select implements actions.Backend.
func (d *Desk) Select(ctx context.Context, h view.Handle) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, _ := h.(string)
	d.Selected = append(d.Selected, id)
	if err := d.PerformErr[id+":select"]; err != nil {
		return err
	}
	if k, i := d.find(id); i >= 0 && !d.Trees[k][i].HasState("selected") {
		d.Trees[k][i].States = append(d.Trees[k][i].States, "selected")
	}
	return nil
}

// Ancestors implements actions.Backend.
func (d *Desk) Ancestors(ctx context.Context, h view.Handle) ([]view.RawNode, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Ancestry++
	id, _ := h.(string)
	k, i := d.find(id)
	if i < 0 {
		return nil, errors.New("gone")
	}
	var out []view.RawNode
	for p := d.Trees[k][i].Parent; p >= 0; p = d.Trees[k][p].Parent {
		out = append(out, d.Trees[k][p])
	}
	return out, nil
}

// Same implements actions.Backend.
func (d *Desk) Same(a, b view.Handle) bool { return a != nil && a == b }

// Raise implements actions.Backend.
func (d *Desk) Raise(ctx context.Context, w view.RawWindow) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Raised = append(d.Raised, w.Key)
	if d.NoRaise {
		return nil
	}
	idx := -1
	for i := range d.Wins {
		d.Wins[i].Active = d.Wins[i].Key == w.Key
		if d.Wins[i].Key == w.Key {
			idx = i
		}
	}
	if idx > 0 {
		win := d.Wins[idx]
		d.Wins = append([]view.RawWindow{win}, append(d.Wins[:idx:idx], d.Wins[idx+1:]...)...)
	}
	return nil
}

// CloseWindow implements actions.Backend.
func (d *Desk) CloseWindow(ctx context.Context, w view.RawWindow) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Closed = append(d.Closed, w.Key)
	for i := range d.Wins {
		if d.Wins[i].Key == w.Key {
			d.Wins = append(d.Wins[:i], d.Wins[i+1:]...)
			break
		}
	}
	return nil
}

// ScreenLocked implements actions.Backend.
func (d *Desk) ScreenLocked(context.Context) bool { return d.Locked }

// Chromium implements actions.Backend.
func (d *Desk) Chromium(pid int) bool { return d.Chromiums[pid] }

// LockedDelivery implements actions.Backend.
func (d *Desk) LockedDelivery() bool { return !d.NoDriver && !d.NoLocked }

// Scale implements actions.Backend.
func (d *Desk) Scale() float64 {
	if d.ScaleV > 0 {
		return d.ScaleV
	}
	return 1
}

func (d *Desk) next(tool string) Reply {
	q := d.Replies[tool]
	if len(q) == 0 {
		return OK
	}
	r := q[0]
	if len(q) > 1 {
		d.Replies[tool] = q[1:]
	}
	return r
}

// Driver implements actions.Backend.
func (d *Desk) Driver(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.NoDriver {
		return nil, driver.ErrNoDriver
	}
	c := Call{Tool: tool, Args: args}
	if dl, ok := ctx.Deadline(); ok {
		c.Budget = time.Until(dl)
	}
	d.Calls = append(d.Calls, c)
	r := d.next(tool)
	if r.Err != nil {
		return nil, r.Err
	}
	return json.RawMessage(r.Raw), nil
}

// HitClick implements actions.Backend.
func (d *Desk) HitClick(ctx context.Context, w view.RawWindow, n view.RawNode, count int) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.NoDriver {
		return nil, driver.ErrNoDriver
	}
	id, _ := n.Handle.(string)
	d.HitCalls = append(d.HitCalls, HitCall{Win: w.Key, Node: id, Count: count})
	r := OK
	if len(d.HitReplies) > 0 {
		r = d.HitReplies[0]
		if len(d.HitReplies) > 1 {
			d.HitReplies = d.HitReplies[1:]
		}
	}
	if r.Err != nil {
		return nil, r.Err
	}
	return json.RawMessage(r.Raw), nil
}

// String summarizes the recorded input, for test failures.
func (d *Desk) String() string {
	var b strings.Builder
	for _, c := range d.Calls {
		args, _ := json.Marshal(c.Args)
		fmt.Fprintf(&b, "%s %s\n", c.Tool, args)
	}
	fmt.Fprintf(&b, "performs %v hits %v raised %v", d.Performs, d.HitCalls, d.Raised)
	return b.String()
}

var _ actions.Backend = (*Desk)(nil)

// FocusElement implements actions.Backend: the element becomes the only
// focused node of its window.
func (d *Desk) FocusElement(ctx context.Context, h view.Handle) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, _ := h.(string)
	d.FocusedIDs = append(d.FocusedIDs, id)
	if err := d.PerformErr[id+":focus"]; err != nil {
		return err
	}
	k, i := d.find(id)
	if i < 0 {
		return nil
	}
	for j := range d.Trees[k] {
		d.Trees[k][j].States = withoutState(d.Trees[k][j].States, "focused")
	}
	d.Trees[k][i].States = append(d.Trees[k][i].States, "focused")
	// Focused answers this element from now on for its window's process.
	for _, w := range d.Wins {
		if w.Key == k {
			if d.Focus == nil {
				d.Focus = map[int][]*view.RawNode{}
			}
			n := d.Trees[k][i]
			d.Focus[w.PID] = []*view.RawNode{&n}
		}
	}
	return nil
}

func withoutState(states []string, drop string) []string {
	out := states[:0:0]
	for _, s := range states {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
