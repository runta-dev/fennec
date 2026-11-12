package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// element looks up a ref target: its node read fresh and its window.
func (r *planRun) element(ctx context.Context, t *protocol.Target) (view.RawNode, view.Entry, *protocol.Error) {
	if t == nil || t.Ref == "" || r.e.X.Refs == nil {
		return view.RawNode{}, view.Entry{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "the target did not resolve to an element"}
	}
	e, err := r.e.X.Refs.Lookup(t.Ref)
	if err != nil {
		return view.RawNode{}, view.Entry{}, engineErr(err)
	}
	n, _, err := r.e.X.B.Read(ctx, e.Handle, false, false)
	if err != nil {
		return view.RawNode{}, e, view.StaleRef(t.Ref)
	}
	if n.Handle == nil {
		n.Handle = e.Handle
	}
	return n, e, nil
}

func stateOf(n view.RawNode) string {
	switch {
	case n.HasState("mixed"):
		return "mixed"
	case n.HasState("checked") || n.HasState("on"):
		return "on"
	}
	return "off"
}

// toggle sets a check box, switch or toggle button: it reads the state,
// presses only when it differs, and reads it back; a mixed box is pressed
// again until it reads the wanted state.
func (r *planRun) toggle(ctx context.Context, i int, s protocol.Step, mode string) (protocol.StepResult, *stop) {
	var want bool
	_ = json.Unmarshal(*s.Value, &want)
	n, _, perr := r.element(ctx, s.Set)
	if perr != nil {
		return refused(perr), &stop{err: perr}
	}
	found := stateOf(n)
	if found == onOff(want) {
		return okResult(fmt.Sprintf("%s is already %s", describeNode(n), found)), nil
	}
	press := protocol.Step{Press: s.Set, Window: s.Window, App: s.App, PID: s.PID, Input: s.Input}
	var last protocol.StepResult
	state := found
	for k := 0; k < 3; k++ {
		last = r.send(ctx, i, press, r.request(mode))
		if !last.OK && last.Status == protocol.StatusRefused {
			return last, &stop{err: last.Error}
		}
		if last.Input == protocol.InputForeground && mode == protocol.InputAuto {
			r.fg = true
		}
		now, _, perr := r.element(ctx, s.Set)
		if perr != nil {
			res := protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: fmt.Sprintf("found %s %s, pressed it", describeNode(n), found), Error: perr}
			return res, &stop{err: perr}
		}
		next := stateOf(now)
		if next == onOff(want) {
			return okResult(fmt.Sprintf("found %s %s; it reads %s after %d press(es)", describeNode(n), found, next, k+1)), nil
		}
		if next == state {
			perr := &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s is still %s after the press", describeNode(n), next), Hint: "observe it; the app may need another way to change it"}
			res := protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: fmt.Sprintf("found %s %s, pressed it once", describeNode(n), found), Error: perr}
			return res, &stop{err: perr}
		}
		state = next
	}
	perr = &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s reads %s, not %s, after three presses", describeNode(n), state, onOff(want))}
	return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "found " + found, Error: perr}, &stop{err: perr}
}

// setText sets a field, a spin button or a popup's option. done is false
// when the step should go through the actions executor as it is.
func (r *planRun) setText(ctx context.Context, i int, s protocol.Step, mode string) (protocol.StepResult, *stop, bool) {
	n, e, perr := r.element(ctx, s.Set)
	if perr != nil {
		return refused(perr), &stop{err: perr}, true
	}
	want := valueText(s.Value)
	if r.popup(ctx, n, e) {
		res, st := r.pickOption(ctx, i, s, mode, n, e, want)
		return res, st, true
	}
	before, _ := r.list(ctx)
	res := r.send(ctx, i, s, r.request(mode))
	if res.Status == protocol.StatusRefused && (n.Role == "spinbutton" || n.Role == "slider") && !r.background(mode) && r.stepTo(ctx, n, want) {
		// The value could not be written (nothing was sent); the
		// control's increment and decrement actions reached it.
		res = okResult(fmt.Sprintf("%s reads %q, stepped by its increment and decrement actions", describeNode(n), want))
	}
	if res.Status == protocol.StatusUnconfirmed && res.Error == nil {
		got := ""
		if now, _, err := r.e.X.B.Read(ctx, n.Handle, true, false); err == nil && now.Value != nil {
			got = *now.Value
		}
		secure := n.HasState("protected") || s.Secret
		switch {
		case secure && utf8.RuneCountInString(got) == utf8.RuneCountInString(want):
			// A secure field reads back as mask characters: its length is
			// all that can be checked, and neither side is ever quoted.
			res.Status, res.OK, res.Retry = protocol.StatusConfirmed, true, nil
			res.Evidence = fmt.Sprintf("%s holds %d characters, as many as were set", describeNode(n), utf8.RuneCountInString(got))
			res.Notes = nil
		case secure:
			perr := &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s holds %d characters after set, not %d", describeNode(n), utf8.RuneCountInString(got), utf8.RuneCountInString(want)), Hint: "clear the field before setting the password again"}
			res.Error, res.Notes = perr, nil
			return res, &stop{err: perr}, true
		case ValueEquals(n.Role, got, want):
			res.Status, res.OK, res.Retry = protocol.StatusConfirmed, true, nil
			res.Evidence = fmt.Sprintf("%s reads %q", describeNode(n), got)
			res.Notes = nil
		case (n.Role == "spinbutton" || n.Role == "slider") && r.stepTo(ctx, n, want):
			// The control took no written value; its own increment and
			// decrement actions walked it to the wanted one.
			res.Status, res.OK, res.Retry = protocol.StatusConfirmed, true, nil
			res.Evidence = fmt.Sprintf("%s reads %q, stepped by its increment and decrement actions", describeNode(n), want)
			res.Notes = nil
		default:
			perr := &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s reads %q after set, not %q", describeNode(n), view.Clip(got, 80, false), want), Hint: "read the field before setting it again"}
			res.Error = perr
			return res, &stop{err: perr}, true
		}
	}
	out, st := r.judge(ctx, i, s, "set", mode, res, before)
	return out, st, true
}

func valueText(v *json.RawMessage) string {
	if v == nil {
		return ""
	}
	var s string
	if json.Unmarshal(*v, &s) == nil {
		return s
	}
	var num json.Number
	if json.Unmarshal(*v, &num) == nil {
		return num.String()
	}
	return string(*v)
}

// pickOption opens a popup's list and presses the option of that name; an
// option already chosen is left alone, and a missing one fails naming the
// options there are.
func (r *planRun) pickOption(ctx context.Context, i int, s protocol.Step, mode string, n view.RawNode, e view.Entry, want string) (protocol.StepResult, *stop) {
	if view.Norm(view.StripObjects(n.Name)) == view.Norm(want) || (n.Value != nil && view.Norm(*n.Value) == view.Norm(want)) {
		return okResult(fmt.Sprintf("%s already shows %q", describeNode(n), want)), nil
	}
	// Press the element already resolved, by its ref: the selector again
	// could pick a label that shares the name.
	openTarget := s.Set
	if e.Ref != "" {
		openTarget = &protocol.Target{Ref: e.Ref}
	}
	open := r.send(ctx, i, protocol.Step{Press: openTarget, Input: s.Input}, r.request(mode))
	if !open.OK && open.Status == protocol.StatusRefused {
		return open, &stop{err: open.Error}
	}
	var options []string
	var hit *protocol.Target
	// The list opens asynchronously (a macOS popup's menu appears some tens
	// of milliseconds after its press): read until options show, up to 1 s.
	deadline := time.Now().Add(optionWait)
	for {
		options, hit = r.readOptions(ctx, e, want)
		if len(options) > 0 || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-time.After(optionPoll):
		case <-ctx.Done():
		}
	}
	if hit == nil && len(options) == 0 {
		// No list showed: a macOS popup whose menu a synthetic press cannot
		// keep open (seen live). Pick by typing, as a person can.
		return r.typeSelect(ctx, i, s, mode, n, e, want)
	}
	if hit == nil {
		perr := &protocol.Error{Code: protocol.ErrTargetNotFound, Message: fmt.Sprintf("%s has no option %q; nothing more was pressed", describeNode(n), want), Hint: "options: " + orStr(strings.Join(options, ", "), "none listed")}
		return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "opened " + describeNode(n), Error: perr}, &stop{err: perr}
	}
	res := r.send(ctx, i, protocol.Step{Press: hit, Input: s.Input}, r.request(mode))
	if !res.OK && res.Status == protocol.StatusRefused {
		return res, &stop{err: res.Error}
	}
	// The control may show the new value a moment after the option's press:
	// read until it does, up to the same bound as a typed pick.
	if got, ok := r.readUntil(ctx, n, want); ok {
		return okResult(fmt.Sprintf("picked %q in %s", want, describeNode(n))), nil
	} else if got != "" {
		perr := &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s reads %q after picking %q", describeNode(n), got, want)}
		return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "pressed the option", Error: perr}, &stop{err: perr}
	}
	return protocol.StepResult{OK: true, Status: protocol.StatusUnconfirmed, Evidence: fmt.Sprintf("pressed the option %q", want)}, nil
}

// Menu step timing: how long an opened menu settles before its entries
// are pressed or read, and how long a toggle's mark may take to flip.
const (
	MenuSettle   = 120 * time.Millisecond
	MenuReadBack = time.Second
)

// menuToggle runs a menu step on macOS: it opens each menu along the path
// by its accessibility action, rereading the opened menu before matching
// the next entry (an entry may retitle itself as its menu opens), reads a
// toggle's mark only while its menu is open, presses only when it differs,
// reads the mark again, and closes every menu it left open.
func (r *planRun) menuToggle(ctx context.Context, i int, s protocol.Step, mode string) (protocol.StepResult, *stop, bool) {
	var path []string
	for _, p := range strings.Split(s.Menu, ">") {
		if p = strings.TrimSpace(p); p != "" {
			path = append(path, p)
		}
	}
	listed, err := r.list(ctx)
	if err != nil {
		perr := engineErr(err)
		return refused(perr), &stop{err: perr}, true
	}
	var w *view.RawWindow
	switch {
	case s.Window != "" || s.App != "" || s.PID != 0:
		got, st := r.targetWindow(ctx, s, protocol.Target{})
		if st != nil {
			return refused(st.err), st, true
		}
		w = &got
	case r.win != nil:
		w = byKey(listed, r.win.Key)
	}
	if w == nil {
		w = frontOf(listed)
	}
	if w == nil {
		// A window that is still coming to the front is waited for, as
		// for any other step.
		if st := r.awaitFront(ctx); st != nil {
			return refused(st.err), st, true
		}
		if listed, err = r.list(ctx); err == nil {
			w = frontOf(listed)
		}
	}
	if w == nil {
		perr := noFront(listed)
		return refused(perr), &stop{err: perr}, true
	}
	if perr := r.e.policy().Allow.Window(w); perr != nil {
		return refused(perr), &stop{err: perr}, true
	}
	if perr := r.e.policy().Activity.Check(); perr != nil {
		return refused(perr), &stop{err: perr}, true
	}
	var want *bool
	if s.Value != nil {
		var b bool
		if err := json.Unmarshal(*s.Value, &b); err != nil {
			perr := &protocol.Error{Code: protocol.ErrInvalid, Message: "a menu value is true or false"}
			return refused(perr), &stop{err: perr}, true
		}
		want = &b
	}
	end := r.acting()
	defer end()
	opened := 0
	var openers []view.Handle // the entries pressed to open a menu, outermost first
	// closeMenus cancels each menu the step opened, innermost first, by
	// its accessibility action: an Escape key does not always reach a menu
	// AppKit opened for accessibility, and a menu left open swallows every
	// later press. A menu that cannot be found gets an Escape.
	closeMenus := func() {
		if opened == 0 {
			return
		}
		var nodes []view.RawNode
		if snap, err := r.snapshot(ctx, *w, view.TreeOptions{Menus: true}); err == nil {
			nodes = snap.Tree.Nodes
		}
		for k := len(openers) - 1; k >= 0; k-- {
			if m := childMenu(r.e.X.B, nodes, openers[k]); m != nil && r.e.X.B.Perform(ctx, m, "AXCancel") == nil {
				continue
			}
			_, _ = r.e.X.B.Driver(ctx, "press_key", map[string]any{"key": "escape", "scope": "desktop", "delivery_mode": protocol.InputForeground})
		}
		opened, openers = 0, nil
	}
	// openPath opens each menu along the path, rereading the opened menu
	// before matching the next entry, and answers the last entry as its
	// open menu shows it. AppKit validates a menu (check marks, titles)
	// as it opens, and a press sent before the menu is up can be lost, so
	// each open settles first.
	openPath := func() (view.RawNode, *protocol.Error) {
		var prev view.Handle
		for k, name := range path {
			var found *view.RawNode
			for try := 0; try < 2 && found == nil; try++ {
				snap, err := r.snapshot(ctx, *w, view.TreeOptions{Menus: true})
				if err != nil {
					return view.RawNode{}, &protocol.Error{Code: protocol.ErrNotResponding, Message: err.Error()}
				}
				found = menuEntry(r.e.X.B, snap.Tree.Nodes, prev, name)
			}
			if found == nil {
				return view.RawNode{}, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: fmt.Sprintf("no menu item %q in %s's menus; nothing was pressed", strings.Join(path[:k+1], " > "), orStr(w.App, "the app")), Hint: "observe with menus: true"}
			}
			if k == len(path)-1 {
				return *found, nil
			}
			if err := r.e.X.B.Perform(ctx, found.Handle, "AXPress"); err != nil {
				return view.RawNode{}, &protocol.Error{Code: protocol.ErrUnavailable, Message: fmt.Sprintf("opening the menu %q failed: %v", name, err)}
			}
			opened++
			openers = append(openers, found.Handle)
			prev = found.Handle
			if err := r.e.sleep(ctx, MenuSettle); err != nil {
				return view.RawNode{}, engineErr(err)
			}
		}
		return view.RawNode{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "a menu step names a path such as \"View > Status Bar\""}
	}
	item, perr := openPath()
	if perr != nil {
		closeMenus()
		return refused(perr), &stop{err: perr}, true
	}
	if want != nil && item.HasState("checked") == *want {
		closeMenus()
		return okResult(fmt.Sprintf("%s is already %s", s.Menu, onOff(*want))), nil, true
	}
	// The entry is pressed with its menus closed again: on macOS 26 a
	// press sent into an open menu is often lost, while a validated entry
	// takes its accessibility action from the closed menu bar.
	closeMenus()
	if err := r.e.sleep(ctx, MenuSettle); err != nil {
		perr := engineErr(err)
		return refused(perr), &stop{err: perr}, true
	}
	if err := r.e.X.B.Perform(ctx, item.Handle, "AXPress"); err != nil {
		perr := &protocol.Error{Code: protocol.ErrUnavailable, Message: fmt.Sprintf("the menu item %q failed: %v; nothing happened", s.Menu, err)}
		return refused(perr), &stop{err: perr}, true
	}
	r.e.policy().Activity.Own()
	if want == nil {
		res := okResult("ran " + s.Menu + " by its accessibility action")
		return res, nil, true
	}
	// The pressed item's mark reads as it was until its menu is validated
	// again: open the menus once more and read it there, for up to
	// MenuReadBack while the app applies the command.
	deadline := r.e.now().Add(MenuReadBack)
	readOK := false
	for {
		if err := r.e.sleep(ctx, MenuSettle); err != nil {
			break
		}
		got, perr := openPath()
		r.e.policy().Activity.Own()
		closeMenus()
		if perr == nil {
			readOK = true
			if got.HasState("checked") == *want {
				return okResult(fmt.Sprintf("%s reads %s", s.Menu, onOff(*want))), nil, true
			}
		}
		if !r.e.now().Before(deadline) {
			break
		}
	}
	if readOK {
		perr := &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s is still %s after the press", s.Menu, onOff(!*want))}
		return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "pressed " + s.Menu, Error: perr}, &stop{err: perr}, true
	}
	perr = &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s could not be read back as %s", s.Menu, onOff(*want))}
	return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "pressed " + s.Menu, Error: perr}, &stop{err: perr}, true
}

// childMenu is the menu an entry opens: its child of role menu.
func childMenu(b actions.Backend, nodes []view.RawNode, opener view.Handle) view.Handle {
	for _, n := range nodes {
		if n.Role == "menu" && n.Parent >= 0 && n.Parent < len(nodes) && b.Same(nodes[n.Parent].Handle, opener) {
			return n.Handle
		}
	}
	return nil
}

// menuEntry finds the first menu entry of that name under prev (anywhere
// for the bar).
func menuEntry(b actions.Backend, nodes []view.RawNode, prev view.Handle, name string) *view.RawNode {
	under := func(i int) bool {
		if prev == nil {
			return true
		}
		for p := nodes[i].Parent; p >= 0; p = nodes[p].Parent {
			if b.Same(nodes[p].Handle, prev) {
				return true
			}
		}
		return false
	}
	for k, n := range nodes {
		if !strings.HasPrefix(n.Role, "menu") || !under(k) {
			continue
		}
		if view.Norm(view.StripObjects(n.Name)) == view.Norm(name) {
			nn := n
			return &nn
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Web pages

// webCheck holds a step to its recorded page: the browser's page tree is
// turned on, and the address (origin and path) must match within
// PageWait; a page with no document yet is waited for up to DocWait.
func (r *planRun) webCheck(ctx context.Context, i int, s protocol.Step) *stop {
	listed, err := r.list(ctx)
	if err != nil {
		return &stop{err: engineErr(err)}
	}
	var w *view.RawWindow
	if r.win != nil {
		w = byKey(listed, r.win.Key)
	}
	if w == nil {
		w = frontOf(listed)
	}
	if w == nil {
		return &stop{err: noFront(listed)}
	}
	if r.e.WebTree != nil && r.e.X.B.Chromium(w.PID) {
		_, _ = r.e.WebTree(ctx, w.PID)
	}
	want := pageKey(s.Web.URL)
	start := r.e.now()
	got := ""
	for {
		snap, err := r.snapshot(ctx, *w, view.TreeOptions{Web: true})
		got = ""
		if err == nil {
			for _, n := range snap.Tree.Nodes {
				if n.Page != "" {
					got = n.Page
					break
				}
			}
		}
		if got != "" && pageKey(got) == want {
			return nil
		}
		limit := PageWait
		if got == "" {
			limit = DocWait
		}
		if r.e.now().Sub(start) >= limit {
			break
		}
		if err := r.e.sleep(ctx, Poll); err != nil {
			return &stop{err: engineErr(err)}
		}
	}
	if got == "" {
		return &stop{recorded: true, err: &protocol.Error{Code: protocol.ErrPageMismatch, Message: fmt.Sprintf("%q shows no address (no document) after %s; the step was recorded on %s, so nothing was clicked", w.Title, DocWait, s.Web.URL), Hint: "load the page first"}}
	}
	return &stop{recorded: true, err: &protocol.Error{Code: protocol.ErrPageMismatch, Message: fmt.Sprintf("the page is %s, but the step was recorded on %s; nothing was clicked", got, s.Web.URL), Hint: "the site may have redirected (to a sign-in page, say): finish from the remaining steps"}}
}

// Web waits.
const (
	PageWait = 3 * time.Second  // for the address to match
	DocWait  = 10 * time.Second // for a page with no document yet
)

// pageKey is a URL's origin and path: query, fragment and a trailing slash
// do not count.
func pageKey(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimRight(u, "/")
	if i := strings.Index(u, "://"); i >= 0 {
		host := u[i+3:]
		rest := ""
		if j := strings.Index(host, "/"); j >= 0 {
			host, rest = host[:j], host[j:]
		}
		return strings.ToLower(u[:i+3]+host) + rest
	}
	return u
}

// popup says whether a combo box is a popup button: no editable text field
// inside it.
func (r *planRun) popup(ctx context.Context, n view.RawNode, e view.Entry) bool {
	if n.Role != "combobox" {
		return false
	}
	listed, err := r.list(ctx)
	if err != nil {
		return false
	}
	w := byKey(listed, e.WindowKey)
	if w == nil {
		return false
	}
	tree, err := r.e.X.B.Tree(ctx, *w, view.TreeOptions{})
	if err != nil {
		return false
	}
	at := -1
	for i, x := range tree.Nodes {
		if r.e.X.B.Same(x.Handle, n.Handle) {
			at = i
		}
		if at >= 0 && i > at && x.Role == "textbox" {
			for p := x.Parent; p >= 0; p = tree.Nodes[p].Parent {
				if p == at {
					return false
				}
			}
		}
	}
	return at >= 0
}

// How long pickOption waits for an opened list to show its options.
const (
	optionWait = time.Second
	optionPoll = 100 * time.Millisecond
)

// readOptions lists the options an opened popup or combo box shows, in the
// element's window and then in the front window (a list can open as a
// window of its own), and the ref of the one named want.
func (r *planRun) readOptions(ctx context.Context, e view.Entry, want string) ([]string, *protocol.Target) {
	listed, _ := r.list(ctx)
	wins := []view.RawWindow{}
	if w := byKey(listed, e.WindowKey); w != nil {
		wins = append(wins, *w)
	}
	if f := frontOf(listed); f != nil && f.Key != e.WindowKey {
		wins = append(wins, *f)
	}
	var options []string
	var hit *protocol.Target
	for _, w := range wins {
		snap, err := r.snapshot(ctx, w, view.TreeOptions{})
		if err != nil {
			continue
		}
		for k, o := range snap.Tree.Nodes {
			if o.Role != "option" && o.Role != "menuitem" && o.Role != "listitem" {
				continue
			}
			if o.Role == "menuitem" && !view.Showing(o) {
				continue // an item of a closed menu, such as the menu bar's
			}
			name := view.StripObjects(o.Name)
			options = append(options, quote(name))
			if hit == nil && view.Norm(name) == view.Norm(want) {
				hit = &protocol.Target{Ref: snap.Refs[k]}
			}
		}
		if hit != nil {
			break
		}
	}
	return options, hit
}

// How long a type-selected value may take to read back.
const typeSelectWait = 1500 * time.Millisecond

// typeSelect picks an option by focusing the element and typing the
// option's name, which a macOS popup button takes as a selection. The value
// is read back until it shows the option, so a slow update is not reported
// as a failure.
func (r *planRun) typeSelect(ctx context.Context, i int, s protocol.Step, mode string, n view.RawNode, e view.Entry, want string) (protocol.StepResult, *stop) {
	end := r.acting()
	err := r.e.X.B.FocusElement(ctx, e.Handle)
	end()
	if err != nil {
		perr := &protocol.Error{Code: protocol.ErrTargetNotFound, Message: fmt.Sprintf("%s showed no options and takes no focus to pick %q by typing; nothing was typed", describeNode(n), want)}
		return protocol.StepResult{Status: protocol.StatusRefused, Error: perr}, &stop{err: perr}
	}
	text := want
	res := r.send(ctx, i, protocol.Step{Type: &text, Into: &protocol.Target{Ref: e.Ref}, Input: s.Input}, r.request(mode))
	if !res.OK && res.Status == protocol.StatusRefused {
		return res, &stop{err: res.Error}
	}
	got, ok := r.readUntil(ctx, n, want)
	if ok {
		return okResult(fmt.Sprintf("picked %q in %s by typing its name", want, describeNode(n))), nil
	}
	perr := &protocol.Error{Code: protocol.ErrExpectFailed, Message: fmt.Sprintf("%s reads %q after typing %q to pick it", describeNode(n), got, want), Hint: "the popup may not list that option; observe it with the menu open"}
	return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "typed the option's name", Error: perr}, &stop{err: perr}
}

// readUntil reads an element until its value (or name) is want, for up to
// typeSelectWait. It returns the last reading and whether it matched; an
// empty reading means the element could not be read.
func (r *planRun) readUntil(ctx context.Context, n view.RawNode, want string) (string, bool) {
	got := ""
	deadline := time.Now().Add(typeSelectWait)
	for {
		if now, _, err := r.e.X.B.Read(ctx, n.Handle, false, false); err == nil {
			name := view.StripObjects(now.Name)
			got = name
			if now.Value != nil {
				got = *now.Value
			}
			if view.Norm(got) == view.Norm(want) || view.Norm(name) == view.Norm(want) {
				return got, true
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return got, false
		}
		select {
		case <-time.After(optionPoll):
		case <-ctx.Done():
		}
	}
}

// stepTo walks a spin button or slider that refuses a written value to a
// number by its AXIncrement and AXDecrement actions, reading it after each
// step; it stops when a step changes nothing or passes the number.
func (r *planRun) stepTo(ctx context.Context, n view.RawNode, want string) bool {
	target, ok := view.NumberOf(want)
	if !ok {
		return false
	}
	read := func() (float64, string, bool) {
		now, _, err := r.e.X.B.Read(ctx, n.Handle, true, false)
		if err != nil || now.Value == nil {
			return 0, "", false
		}
		f, ok := view.NumberOf(*now.Value)
		return f, *now.Value, ok
	}
	cur, text, ok := read()
	for k := 0; ok && k < maxSteps; k++ {
		if ValueEquals(n.Role, text, want) {
			return true
		}
		action, up := "AXIncrement", cur < target
		if !up {
			action = "AXDecrement"
		}
		if r.e.X.B.Perform(ctx, n.Handle, action) != nil {
			return false
		}
		r.e.policy().Activity.Own()
		next, nextText, ok2 := read()
		if !ok2 || next == cur || (up && next > target) || (!up && next < target) {
			return ok2 && ValueEquals(n.Role, nextText, want)
		}
		cur, text = next, nextText
	}
	return ok && ValueEquals(n.Role, text, want)
}

// maxSteps bounds the increment and decrement actions of one set.
const maxSteps = 200
