package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// element is a resolved element target.
type element struct {
	node view.RawNode
	h    view.Handle
	win  view.RawWindow
	ref  string
}

func (e element) describe() string {
	d := e.node.Role
	if e.node.Name != "" {
		d += fmt.Sprintf(" %q", e.node.Name)
	}
	if e.ref != "" {
		d += " (" + e.ref + ")"
	}
	return d
}

// element resolves a ref or selector target to an element and its window.
func (c *stepCtx) element(ctx context.Context, t protocol.Target) (element, *protocol.Error) {
	listed, perr := c.windows(ctx)
	if perr != nil {
		return element{}, perr
	}
	if t.Ref != "" {
		if c.x.Refs == nil {
			return element{}, view.StaleRef(t.Ref)
		}
		e, err := c.x.Refs.Lookup(t.Ref)
		if err != nil {
			return element{}, asProtocol(err)
		}
		if e.Window {
			return element{}, &protocol.Error{Code: protocol.ErrInvalid, Message: t.Ref + " is a window, not an element; nothing was sent"}
		}
		var win *view.RawWindow
		for i := range listed {
			if listed[i].Key == e.WindowKey {
				win = &listed[i]
				break
			}
		}
		if win == nil {
			return element{}, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: "the window of " + t.Ref + " is gone or not admitted; nothing was sent", Hint: "observe again"}
		}
		if perr := asProtocol(view.Actionable(*win, protocol.InputAuto)); perr != nil {
			return element{}, perr
		}
		n, _, err := c.x.B.Read(ctx, e.Handle, false, false)
		if err != nil {
			return element{}, view.StaleRef(t.Ref)
		}
		if n.Handle == nil {
			n.Handle = e.Handle
		}
		return element{node: n, h: e.Handle, win: *win, ref: t.Ref}, nil
	}
	if t.Select == "" {
		return element{}, &protocol.Error{Code: protocol.ErrInvalid, Message: c.kind + " needs an element: a ref or a selector"}
	}
	sel, err := view.ParseSelector(t.Select)
	if err != nil {
		return element{}, asProtocol(err)
	}
	var w *view.RawWindow
	if t.Window != "" {
		w, perr = c.window(ctx, t.Window, c.s.PID)
	} else {
		w, _, perr = c.target(ctx)
		if w == nil && perr == nil {
			w = c.front(ctx)
		}
	}
	if perr != nil {
		return element{}, perr
	}
	if w == nil {
		return element{}, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "no front window to find " + t.Select + " in; nothing was sent", Hint: "name the window"}
	}
	tree, err := c.x.B.Tree(ctx, *w, view.TreeOptions{Menus: true})
	if err != nil {
		return element{}, &protocol.Error{Code: protocol.ErrNotResponding, Message: err.Error()}
	}
	if c.x.Refs == nil {
		c.x.Refs = view.NewRegistry(0)
	}
	walk := c.x.Refs.Begin()
	defer walk.End()
	snap := walk.Snapshot(tree)
	i, _, err := view.Resolve(snap, sel)
	if err != nil {
		return element{}, asProtocol(err)
	}
	n := tree.Nodes[i]
	return element{node: n, h: n.Handle, win: *w, ref: snap.Refs[i]}, nil
}

// Roles a press selects, and roles whose press is an AX action in front.
var (
	selectRoles    = map[string]bool{"tab": true, "option": true, "radio": true, "listitem": true, "treeitem": true}
	pressableRoles = map[string]bool{"button": true, "checkbox": true, "switch": true, "link": true}
)

func axFailure(e element, err error, what string) policy.Attempt {
	code := axCode(err)
	if code == AXCannotComplete && (what == "the press action" || strings.HasPrefix(what, "the menu item")) {
		// An app that opens a modal dialog from the action does not
		// answer until the dialog closes: the press counts as done,
		// unproven, and is never followed by a click or sent again.
		return unconfirmed(fmt.Sprintf("pressed %s by its accessibility action; the app did not answer in time, as when it opens a modal dialog", e.describe()), nil,
			"the app did not answer the press in time; observe before pressing again")
	}
	if code == AXCannotComplete {
		return unconfirmed("", &protocol.Error{
			Code:    protocol.ErrNotResponding,
			Message: fmt.Sprintf("%s on %s timed out and may still land: %v", what, e.describe(), err),
			Hint:    "read the element back before trying again",
		})
	}
	return refuse(&protocol.Error{Code: protocol.ErrUnavailable, Message: fmt.Sprintf("%s on %s failed: %v; nothing happened", what, e.describe(), err)}, policy.ReasonPolicy)
}

// press presses an element the way a user would: it selects tabs and items,
// runs menu items and front buttons by their accessibility action, and
// otherwise clicks the element's centre when a hit test finds it on top,
// running its action when something covers it. Under background input it
// only runs the action, and never clicks after the action failed.
func (c *stepCtx) press(ctx context.Context) policy.Attempt {
	e, perr := c.element(ctx, *c.s.Press)
	if perr != nil {
		return refuse(perr, "")
	}
	if a, ok := c.security(e); !ok {
		return a
	}
	role := e.node.Role
	if selectRoles[role] {
		if err := c.x.B.Select(ctx, e.h); err == nil {
			if n, _, err := c.x.B.Read(ctx, e.h, false, false); err == nil && n.HasState("selected") {
				return confirmed("selected " + e.describe())
			}
			return unconfirmed("selected "+e.describe()+" through accessibility", nil, "it does not read back as selected")
		}
	}
	if strings.HasPrefix(role, "menuitem") {
		if err := c.x.B.Perform(ctx, e.h, "AXPress"); err != nil {
			return axFailure(e, err, "the menu item's action")
		}
		return confirmed("ran " + e.describe() + " by its accessibility action")
	}
	if !c.fg() {
		if err := c.x.B.Perform(ctx, e.h, "AXPress"); err != nil {
			return axFailure(e, err, "the press action")
		}
		return confirmed("pressed " + e.describe() + " by its accessibility action")
	}
	if pressableRoles[role] && e.win.Active {
		err := c.x.B.Perform(ctx, e.h, "AXPress")
		if err == nil {
			return confirmed("pressed " + e.describe() + " by its accessibility action")
		}
		if axCode(err) == AXCannotComplete {
			return axFailure(e, err, "the press action")
		}
		// The app refused the action: a click may still work.
	}
	if x, y, ok := c.reachable(ctx, e); ok {
		a := c.clickWindow(ctx, e.win, x, y, "left", 1, nil)
		if a.Result.Status != protocol.StatusRefused {
			a.Result.Evidence = fmt.Sprintf("clicked the centre of %s at (%d, %d), where a hit test found it on top", e.describe(), x, y)
			a.Result.How = "click"
		}
		return a
	}
	if err := c.x.B.Perform(ctx, e.h, "AXPress"); err != nil {
		return axFailure(e, err, "the press action")
	}
	return confirmed("pressed " + e.describe() + " by its accessibility action")
}

// perform runs one of the element's named accessibility actions; a name
// the element does not list is refused before anything is sent.
func (c *stepCtx) perform(ctx context.Context) policy.Attempt {
	e, perr := c.element(ctx, *c.s.Perform)
	if perr != nil {
		return refuse(perr, "")
	}
	if a, ok := c.security(e); !ok {
		return a
	}
	names, err := c.x.B.ActionNames(ctx, e.h)
	if err != nil {
		return refuse(&protocol.Error{Code: protocol.ErrNotResponding, Message: fmt.Sprintf("reading the actions of %s failed: %v; nothing was sent", e.describe(), err)}, policy.ReasonPolicy)
	}
	raw, ok := view.ActionName(names, c.s.Action)
	if !ok {
		listed := view.SecondaryActions(names)
		msg := fmt.Sprintf("%s has no action %q; nothing was sent", e.describe(), c.s.Action)
		if len(listed) > 0 {
			msg = fmt.Sprintf("%s has no action %q (it lists %s); nothing was sent", e.describe(), c.s.Action, strings.Join(listed, ", "))
		}
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: msg}, policy.ReasonPolicy)
	}
	spoken := view.SpokenAction(raw)
	if err := c.x.B.Perform(ctx, e.h, raw); err != nil {
		return axFailure(e, err, "the "+spoken+" action")
	}
	return confirmed("ran the " + spoken + " action of " + e.describe())
}

// selectText selects a character range of a text element's value, or
// places the caret when the length is zero.
func (c *stepCtx) selectText(ctx context.Context) policy.Attempt {
	e, perr := c.element(ctx, *c.s.SelectText)
	if perr != nil {
		return refuse(perr, "")
	}
	if a, ok := c.security(e); !ok {
		return a
	}
	if c.s.Range == nil || c.s.Range[0] < 0 || c.s.Range[1] < 0 {
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "select_text needs range [offset, length]; nothing was sent"}, policy.ReasonPolicy)
	}
	loc, n := c.s.Range[0], c.s.Range[1]
	if err := c.x.B.SelectRange(ctx, e.h, loc, n); err != nil {
		return axFailure(e, err, "the text selection")
	}
	if n == 0 {
		return confirmed(fmt.Sprintf("placed the caret at character %d of %s", loc, e.describe()))
	}
	return confirmed(fmt.Sprintf("selected characters %d to %d of %s", loc, loc+n, e.describe()))
}

// reachable is the element's centre when a hit test there lands on the
// element, inside it or on one of its ancestors.
func (c *stepCtx) reachable(ctx context.Context, e element) (int, int, bool) {
	x, y, ok := center(e.node.Bounds)
	if !ok || !view.Showing(e.node) {
		return 0, 0, false
	}
	hit, path, _, covered, err := c.x.B.At(ctx, x, y)
	if err != nil || hit == nil {
		return 0, 0, false
	}
	if covered != nil && covered.PID != e.win.PID {
		return 0, 0, false
	}
	if c.x.B.Same(hit.Handle, e.h) {
		return x, y, true
	}
	chain, err := c.x.B.Ancestors(ctx, e.h)
	if err == nil {
		for _, a := range chain {
			if c.x.B.Same(hit.Handle, a.Handle) {
				return x, y, true
			}
		}
	}
	// The hit is inside the element.
	for _, a := range path {
		if c.x.B.Same(a.Handle, e.h) {
			return x, y, true
		}
	}
	return 0, 0, false
}

// security refuses input aimed at a macOS security or consent prompt.
func (c *stepCtx) security(e element) (policy.Attempt, bool) {
	if securityPrompt(e.win) {
		return refuse(securityError(e.win), policy.ReasonPolicy), false
	}
	return policy.Attempt{}, true
}

// Security and consent prompts fennec-cua never drives.
var securityApps = map[string]bool{
	"securityagent": true, "coreservicesuiagent": true, "universalaccessauthwarn": true,
	"usernotificationcenter": true, "tccd": true, "authorizationhost": true,
}

func securityPrompt(w view.RawWindow) bool {
	return securityApps[strings.ToLower(strings.ReplaceAll(w.App, " ", ""))]
}

func securityError(w view.RawWindow) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.ErrSecurityPrompt,
		Message: fmt.Sprintf("%q is a macOS security or consent prompt (%s), which fennec-cua never drives; nothing was sent", w.Title, w.App),
		Hint:    "ask the user to answer the prompt",
	}
}

// set gives a field its value, a check box or switch its state, or a popup
// its option, and reads it back.
func (c *stepCtx) set(ctx context.Context) policy.Attempt {
	if c.s.Value == nil {
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "set needs a value"}, policy.ReasonPolicy)
	}
	e, perr := c.element(ctx, *c.s.Set)
	if perr != nil {
		return refuse(perr, "")
	}
	if a, ok := c.security(e); !ok {
		return a
	}
	var on bool
	if json.Unmarshal(*c.s.Value, &on) == nil {
		if e.node.HasState("checked") == on {
			return confirmed(fmt.Sprintf("%s is already %s", e.describe(), onOff(on)))
		}
		if err := c.x.B.Perform(ctx, e.h, "AXPress"); err != nil {
			return axFailure(e, err, "the toggle")
		}
		n, _, err := c.x.B.Read(ctx, e.h, false, false)
		if err == nil && n.HasState("checked") == on {
			return confirmed(fmt.Sprintf("%s reads %s", e.describe(), onOff(on)))
		}
		return unconfirmed("toggled "+e.describe(), nil, "it does not read back as "+onOff(on))
	}
	var want string
	if json.Unmarshal(*c.s.Value, &want) != nil {
		var num json.Number
		if err := json.Unmarshal(*c.s.Value, &num); err != nil {
			return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "set takes a string, number or bool value"}, policy.ReasonPolicy)
		}
		want = num.String()
	}
	if err := c.x.B.SetValue(ctx, e.h, want); err != nil {
		return axFailure(e, err, "setting the value")
	}
	n, _, err := c.x.B.Read(ctx, e.h, true, false)
	if err == nil && n.Value != nil && *n.Value == want {
		return confirmed(fmt.Sprintf("%s reads %q", e.describe(), want))
	}
	got := "nothing"
	if err == nil && n.Value != nil {
		got = fmt.Sprintf("%q", view.Clip(*n.Value, 80, false))
	}
	return unconfirmed("set "+e.describe(), nil, "it reads "+got+" afterwards; the field may have formatted it")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// menu runs a menu item by its path ("File > Export > PDF") through
// accessibility, without opening the menus above it. A value asks for an
// on/off state, read before and after.
func (c *stepCtx) menu(ctx context.Context) policy.Attempt {
	var path []string
	for _, p := range strings.Split(c.s.Menu, ">") {
		if p = strings.TrimSpace(p); p != "" {
			path = append(path, p)
		}
	}
	if len(path) == 0 {
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "menu needs a path such as File > Save"}, policy.ReasonPolicy)
	}
	w, reason, perr := c.target(ctx)
	if perr != nil {
		return refuse(perr, reason)
	}
	if w == nil {
		w = c.front(ctx)
	}
	if w == nil {
		return refuse(&protocol.Error{Code: protocol.ErrWindowNotFound, Message: "no front window whose menus to use; nothing was sent"}, policy.ReasonNoWindow)
	}
	tree, err := c.x.B.Tree(ctx, *w, view.TreeOptions{Menus: true})
	if err != nil {
		return refuse(&protocol.Error{Code: protocol.ErrNotResponding, Message: err.Error()}, "")
	}
	i := findMenu(tree.Nodes, path)
	if i < 0 {
		return refuse(&protocol.Error{Code: protocol.ErrTargetNotFound, Message: fmt.Sprintf("no menu item %q in %s's menus; nothing was sent", c.s.Menu, w.App), Hint: "observe with menus: true"}, "")
	}
	e := element{node: tree.Nodes[i], h: tree.Nodes[i].Handle, win: *w}
	var want *bool
	if c.s.Value != nil {
		var b bool
		if err := json.Unmarshal(*c.s.Value, &b); err != nil {
			return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "a menu value is true or false"}, policy.ReasonPolicy)
		}
		want = &b
		if e.node.HasState("checked") == b {
			return confirmed(fmt.Sprintf("%s is already %s", c.s.Menu, onOff(b)))
		}
	}
	if err := c.x.B.Perform(ctx, e.h, "AXPress"); err != nil {
		return axFailure(e, err, "the menu item")
	}
	if want == nil {
		return confirmed("ran " + c.s.Menu)
	}
	n, _, err := c.x.B.Read(ctx, e.h, false, false)
	if err == nil && n.HasState("checked") == *want {
		return confirmed(fmt.Sprintf("%s reads %s", c.s.Menu, onOff(*want)))
	}
	return unconfirmed("ran "+c.s.Menu, nil, "its state could not be read back as "+onOff(*want))
}

// findMenu finds the item at the end of a menu path: each name a menu
// element inside the previous one.
func findMenu(nodes []view.RawNode, path []string) int {
	under := func(i, anc int) bool {
		for p := nodes[i].Parent; p >= 0; p = nodes[p].Parent {
			if p == anc {
				return true
			}
		}
		return anc < 0
	}
	prev := -1
	for _, name := range path {
		found := -1
		for i, n := range nodes {
			if !strings.HasPrefix(n.Role, "menu") || !under(i, prev) {
				continue
			}
			if view.Norm(n.Name) == view.Norm(name) {
				found = i
				break
			}
		}
		if found < 0 {
			return -1
		}
		prev = found
	}
	return prev
}
