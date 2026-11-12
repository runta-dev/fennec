package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"fennec/cua/internal/models"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

// Model pick bars.
const (
	PickBar  = 0.7 // the top choice's probability
	PickLead = 0.6 // its lead over the runner-up
	// MaxCandidates caps the elements a pick shows the model.
	MaxCandidates = 80
)

// resolved is how a step's target was settled.
type resolved struct {
	target      *protocol.Target
	how         string
	served      []protocol.Served
	notes       []string
	grounded    bool
	fg          bool
	clickCentre bool
}

func asModelErr(err error, me **models.Error) bool { return errors.As(err, me) }

// modified says whether a click carries a button, count or modifiers.
func modified(s protocol.Step) bool {
	return (s.Button != "" && !strings.EqualFold(s.Button, "left")) || s.Count > 1 || len(s.Mods) > 0
}

// resolve settles a step's target before it acts: image points are mapped,
// selectors are matched by DOM id, recorded fingerprint and name (with the
// menu bar and a late window), then by the recording's fallbacks, a model
// pick and visual grounding.
func (r *planRun) resolve(ctx context.Context, i int, s protocol.Step, kind, mode string) (resolved, *stop) {
	t := targetOf(s)
	if t.Point != nil {
		return r.pointTarget(ctx, *t, mode)
	}
	if t.Ref != "" {
		return resolved{how: "ref"}, nil
	}
	sel, err := view.ParseSelector(t.Select)
	if err != nil {
		perr := engineErr(err)
		return resolved{}, &stop{err: perr}
	}
	w, st := r.targetWindow(ctx, s, *t)
	if st != nil {
		return resolved{}, st
	}
	deadline := r.e.now().Add(LateWait)
	var last *protocol.Error
	opts := view.TreeOptions{Web: s.Web != nil}
	for {
		snap, err := r.snapshot(ctx, w, opts)
		if err != nil {
			return resolved{}, &stop{err: &protocol.Error{Code: protocol.ErrNotResponding, Message: fmt.Sprintf("%q answers no accessibility calls: %v; nothing was sent", w.Title, err), Hint: "wait for the app or quit it"}}
		}
		idx, how, perr := r.pickIn(snap, sel, s)
		if perr == nil && how == "contains" {
			// An exact menu item beats a control that only contains the name.
			if msnap, err := r.snapshot(ctx, w, view.TreeOptions{Menus: true, Web: s.Web != nil}); err == nil {
				if m, err := view.Find(msnap, sel); err == nil && m.Tier == view.TierName {
					for _, k := range m.Index {
						if strings.HasPrefix(msnap.Tree.Nodes[k].Role, "menu") {
							return r.found(ctx, s, kind, msnap, k, "menu")
						}
					}
				}
			}
			how = "name"
		}
		if perr == nil {
			return r.found(ctx, s, kind, snap, idx, how)
		}
		if perr.Code != protocol.ErrTargetNotFound {
			return resolved{}, &stop{err: perr}
		}
		last = perr
		// The menu bar, before any screenshot or model.
		if msnap, err := r.snapshot(ctx, w, view.TreeOptions{Menus: true, Web: s.Web != nil}); err == nil {
			if idx, how, perr := r.pickIn(msnap, sel, s); perr == nil {
				if how == "name" {
					how = "menu"
				}
				return r.found(ctx, s, kind, msnap, idx, how)
			}
		}
		if !r.e.now().Before(deadline) {
			break
		}
		if err := r.e.sleep(ctx, Poll); err != nil {
			return resolved{}, &stop{err: engineErr(err)}
		}
		if !ownWindow(s) && !r.background(mode) {
			if listed, err := r.list(ctx); err == nil {
				if f := frontOf(listed); f != nil && f.Key != w.Key && (r.win == nil || f.PID == r.win.PID) {
					w = *f
				}
			}
		}
	}
	snap, _ := r.snapshot(ctx, w, view.TreeOptions{Menus: true})
	var got resolved
	if s.Recorded != nil && (kind == "click" || kind == "press") {
		return r.recordedClick(ctx, i, s, kind, mode, w, sel)
	}
	if r.e.models() != nil {
		idx, served, st := r.modelPick(ctx, s, kind, mode, snap, sel, t.Select)
		got.served = served
		if st != nil {
			return got, st
		}
		if idx >= 0 {
			f, st := r.found(ctx, s, kind, snap, idx, "model")
			f.served = served
			return f, st
		}
		if (kind == "click" || kind == "press") && r.e.Screen != nil {
			if r.background(mode) && mode != protocol.InputAuto {
				return got, &stop{err: &protocol.Error{Code: protocol.ErrTargetNotFound, Message: last.Message + "; " + noPixels + ", so it was not searched for on screen", Hint: last.Hint}}
			}
			g, st := r.groundTarget(ctx, s, mode, w, sel.Name, false)
			g.served = append(served, g.served...)
			return g, st
		}
	}
	if r.background(mode) {
		last.Message += "; " + noPixels + ", so it was not searched for on screen"
	}
	return got, &stop{err: last}
}

// found turns a matched node into the step's target.
func (r *planRun) found(ctx context.Context, s protocol.Step, kind string, snap view.Snapshot, idx int, how string) (resolved, *stop) {
	n := snap.Tree.Nodes[idx]
	if (kind == "click" || kind == "press") && modified(s) {
		if x, y, ok := centre(n.Bounds); ok {
			return resolved{target: &protocol.Target{Point: &protocol.Point{X: x, Y: y}}, how: how, clickCentre: true}, nil
		}
	}
	return resolved{target: &protocol.Target{Ref: snap.Refs[idx]}, how: how}, nil
}

func centre(b *[4]int) (float64, float64, bool) {
	if b == nil || b[2] <= 0 || b[3] <= 0 {
		return 0, 0, false
	}
	return float64(b[0]) + float64(b[2])/2, float64(b[1]) + float64(b[3])/2, true
}

// pickIn matches a selector in a snapshot: a recorded DOM id first, then a
// recorded fingerprint when the match count is as recorded, then the name.
func (r *planRun) pickIn(snap view.Snapshot, sel view.Selector, s protocol.Step) (int, string, *protocol.Error) {
	nodes := snap.Tree.Nodes
	if s.Web != nil && s.Web.DOMID != "" {
		for i, n := range nodes {
			if n.DOMID == s.Web.DOMID && (len(sel.Roles) == 0 || roleIn(n.Role, sel.Roles)) {
				return i, "dom_id", nil
			}
		}
	}
	if rec := s.Recorded; rec != nil && rec.Of > 0 && rec.Nth > 0 {
		if m, err := view.Find(snap, sel); err == nil && len(m.Index) == rec.Of && rec.Nth <= len(m.Index) {
			return m.Index[rec.Nth-1], "fingerprint", nil
		}
	}
	idx, tier, err := view.Resolve(snap, sel)
	if err != nil {
		perr := engineErr(err)
		if perr.Code == protocol.ErrTargetAmbiguous {
			if inner, ok := nestedField(snap, sel); ok {
				return inner, "name", nil
			}
		}
		return -1, "", perr
	}
	if tier == view.TierContains || tier == view.TierLabelInside {
		return idx, "contains", nil
	}
	return idx, "name", nil
}

// nestedField settles a text field nested in a combo box of the same name:
// one target, the inner editable field.
func nestedField(snap view.Snapshot, sel view.Selector) (int, bool) {
	m, err := view.Find(snap, sel)
	if err != nil || len(m.Index) != 2 {
		return -1, false
	}
	outer, inner := m.Index[0], m.Index[1]
	nodes := snap.Tree.Nodes
	if nodes[outer].Role != "combobox" || (nodes[inner].Role != "textbox" && nodes[inner].Role != "combobox") {
		return -1, false
	}
	for p := nodes[inner].Parent; p >= 0; p = nodes[p].Parent {
		if p == outer {
			return inner, true
		}
	}
	return -1, false
}

func roleIn(role string, roles []string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// targetWindow is the window a selector is matched in: the target's own
// window, the step's, the plan window, else the front one, waited for
// briefly when a key the step before sent is opening it.
func (r *planRun) targetWindow(ctx context.Context, s protocol.Step, t protocol.Target) (view.RawWindow, *stop) {
	listed, err := r.list(ctx)
	if err != nil {
		return view.RawWindow{}, &stop{err: engineErr(err)}
	}
	pick := func(ws []view.RawWindow) (*view.RawWindow, *protocol.Error) {
		switch {
		case t.Window != "":
			w, err := r.windowNamed(ws, t.Window, s.PID)
			return w, err
		case s.Window != "":
			w, err := r.windowNamed(ws, s.Window, s.PID)
			return w, err
		case s.App != "":
			w, err := view.PickApp(ws, s.App)
			if err != nil {
				return nil, engineErr(err)
			}
			return &w, nil
		case s.PID != 0:
			w, err := view.PickWindow(ws, "", s.PID)
			if err != nil {
				return nil, engineErr(err)
			}
			return &w, nil
		case r.win != nil:
			if w := byKey(ws, r.win.Key); w != nil {
				return w, nil
			}
			return nil, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: fmt.Sprintf("the plan's window %q is gone; nothing was sent", r.win.Title), Hint: "observe scope desktop for the open windows"}
		}
		return frontOf(ws), nil
	}
	w, perr := pick(listed)
	if perr != nil {
		return view.RawWindow{}, &stop{err: perr}
	}
	deadline := r.e.now().Add(LateWait)
	for w == nil && r.e.now().Before(deadline) {
		if err := r.e.sleep(ctx, Poll); err != nil {
			break
		}
		if listed, err = r.list(ctx); err == nil {
			w = frontOf(listed)
		}
	}
	if w == nil {
		return view.RawWindow{}, &stop{err: noFront(listed)}
	}
	if w.Stale {
		return view.RawWindow{}, &stop{err: &protocol.Error{Code: protocol.ErrNotResponding, Message: fmt.Sprintf("%q (pid %d) answers no accessibility calls; nothing was sent", w.Title, w.PID), Hint: "wait for the app or quit it"}}
	}
	return *w, nil
}

func (r *planRun) windowNamed(ws []view.RawWindow, name string, pid int) (*view.RawWindow, *protocol.Error) {
	if refLike(name) && r.e.X.Refs != nil {
		e, err := r.e.X.Refs.Lookup(name)
		if err != nil {
			return nil, engineErr(err)
		}
		if w := byKey(ws, e.WindowKey); w != nil {
			return w, nil
		}
		return nil, &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "window " + name + " is gone; nothing was sent"}
	}
	w, err := view.PickWindow(ws, name, pid)
	if err != nil {
		return nil, engineErr(err)
	}
	return &w, nil
}

func refLike(s string) bool {
	if len(s) < 2 || (s[0] != 'w' && s[0] != 'e') {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// noFront is the error of a step with no window to act in.
func noFront(listed []view.RawWindow) *protocol.Error {
	return &protocol.Error{Code: protocol.ErrWindowNotFound, Message: "no focused window to act in; nothing was sent", Hint: "start with a launch step, focus a window, or name the window"}
}

func engineErr(err error) *protocol.Error {
	if err == nil {
		return nil
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		cp := *pe
		return &cp
	}
	var me *models.Error
	if errors.As(err, &me) {
		return me.Protocol()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: protocol.ErrTimeout, Message: "timed out"}
	}
	return &protocol.Error{Code: protocol.ErrInternal, Message: err.Error()}
}

// pointTarget maps an image-space point by the screenshot of its view;
// pixel input passes the vision policy.
func (r *planRun) pointTarget(ctx context.Context, t protocol.Target, mode string) (resolved, *stop) {
	if t.Point.Space != protocol.SpaceImage {
		return resolved{how: "point"}, nil
	}
	if perr := r.e.policy().Vision(true); perr != nil {
		return resolved{}, &stop{err: perr}
	}
	if r.e.Sessions == nil {
		return resolved{}, &stop{err: &protocol.Error{Code: protocol.ErrUnavailable, Message: "image points need captures, which are not available; nothing was sent", Hint: "give a screen point or a semantic target"}}
	}
	session := ""
	if r.call != nil && r.call.Session != nil {
		session = vision.SessionFor(r.call.Session.AgentID)
	}
	shot, err := r.e.Sessions.Pin(session, t.Point.View, 0)
	if err != nil {
		return resolved{}, &stop{err: engineErr(err)}
	}
	if err := vision.OffScreen(shot, "click", [2]float64{t.Point.X, t.Point.Y}); err != nil {
		return resolved{}, &stop{err: engineErr(err)}
	}
	x, y := shot.ToDesktop(t.Point.X, t.Point.Y)
	out := resolved{target: &protocol.Target{Point: &protocol.Point{X: x, Y: y}}, how: "point"}
	if mode == protocol.InputAuto && !r.fg {
		if st := r.takeForeground(ctx); st != nil {
			return out, st
		}
		out.fg = true
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Model picks

type candidate struct {
	idx  int
	ref  string
	desc string
}

// candidates are the showing elements a model may pick from.
func candidates(snap view.Snapshot) []candidate {
	var out []candidate
	for i, n := range snap.Tree.Nodes {
		if i == 0 && snap.RootIsWindow {
			continue
		}
		if !view.Showing(n) {
			continue
		}
		name := view.StripObjects(n.Name)
		if name == "" {
			name = view.Label(snap.Tree.Nodes, i)
		}
		if name == "" && n.Value == nil {
			continue
		}
		out = append(out, candidate{idx: i, ref: snap.Refs[i], desc: view.Describe(snap, i)})
		if len(out) == MaxCandidates {
			break
		}
	}
	return out
}

// modelPick asks the decide model which element a target names: on an
// annotated screenshot first when a vision model is configured, else (or
// when that fails) on the element list. The pick must clear PickBar and
// lead the runner-up by PickLead; a text pick must also share a word with
// the target and not contradict a role it names.
func (r *planRun) modelPick(ctx context.Context, s protocol.Step, kind, mode string, snap view.Snapshot, sel view.Selector, text string) (int, []protocol.Served, *stop) {
	cands := candidates(snap)
	if len(cands) == 0 {
		return -1, nil, nil
	}
	var served []protocol.Served
	if r.e.VisionModel && r.e.Screen != nil && !r.background(mode) {
		idx, sv, st, ok := r.pickOnce(ctx, cands, snap, sel, text, true)
		served = append(served, sv...)
		if st != nil {
			return -1, served, st
		}
		if ok {
			return idx, served, nil
		}
	}
	idx, sv, st, _ := r.pickOnce(ctx, cands, snap, sel, text, false)
	served = append(served, sv...)
	return idx, served, st
}

func (r *planRun) pickOnce(ctx context.Context, cands []candidate, snap view.Snapshot, sel view.Selector, text string, image bool) (int, []protocol.Served, *stop, bool) {
	criteria := map[string]string{"none": "no listed element is the target"}
	lines := []string{fmt.Sprintf("Which element of the window %q is the target %q?", snap.Window.Title, text)}
	var state []any
	if image {
		shot, err := r.e.Screen.Shot(ctx)
		if err != nil {
			return -1, nil, nil, false
		}
		b64, err := pngB64(shot.Frame)
		if err != nil {
			return -1, nil, nil, false
		}
		for _, c := range cands {
			n := snap.Tree.Nodes[c.idx]
			at := ""
			if x, y, ok := centre(n.Bounds); ok {
				at = fmt.Sprintf(" at pixel (%d, %d)", int((x-shot.Origin[0])/shot.Scale), int((y-shot.Origin[1])/shot.Scale))
			}
			criteria[c.ref] = c.desc + at
		}
		state = []any{strings.Join(lines, "\n"), map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + b64}}}
	} else {
		for _, c := range cands {
			criteria[c.ref] = c.desc
		}
		state = []any{strings.Join(lines, "\n")}
	}
	q := map[string]any{"pick": map[string]any{"type": models.Choice, "instructions": "Pick the element the target names, or none.", "criteria": criteria}}
	stateJSON, _ := json.Marshal(state)
	qJSON, _ := json.Marshal(q)
	d, err := r.e.models().Decide(ctx, models.DecideArgs{State: stateJSON, Questions: qJSON, Image: image})
	if err != nil {
		if st := creditsStop(err); st != nil {
			return -1, nil, st, false
		}
		return -1, nil, nil, false
	}
	served := []protocol.Served{d.Served}
	a, ok := d.Answers["pick"]
	if !ok {
		return -1, served, nil, false
	}
	type pc struct {
		ref string
		p   float64
	}
	var ps []pc
	for k, p := range a.Probabilities {
		ps = append(ps, pc{k, p})
	}
	if len(ps) == 0 && a.Choice != "" {
		ps = append(ps, pc{a.Choice, a.Confidence})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].p > ps[j].p || (ps[i].p == ps[j].p && ps[i].ref < ps[j].ref) })
	if len(ps) == 0 || ps[0].ref == "none" || ps[0].p < PickBar {
		return -1, served, nil, false
	}
	second := 0.0
	if len(ps) > 1 {
		second = ps[1].p
	}
	byRef := map[string]candidate{}
	for _, c := range cands {
		byRef[c.ref] = c
	}
	top, ok := byRef[ps[0].ref]
	if !ok {
		return -1, served, nil, false
	}
	if ps[0].p-second < PickLead {
		var tops []string
		for _, x := range ps[:min(3, len(ps))] {
			if c, ok := byRef[x.ref]; ok {
				tops = append(tops, fmt.Sprintf("%s (p %.2f)", c.desc, x.p))
			}
		}
		return -1, served, &stop{err: &protocol.Error{
			Code:    protocol.ErrTargetAmbiguous,
			Message: fmt.Sprintf("no element is named %q, and the model could not tell the candidates apart: %s; nothing was pressed", text, strings.Join(tops, ", ")),
			Hint:    "use a more exact name or a ref",
		}}, false
	}
	if !image && !textPickFits(snap, top.idx, sel, text) {
		return -1, served, nil, false
	}
	return top.idx, served, nil, true
}

// textPickFits guards a text-only pick: the element's own name, value or
// label must share a word with the target; a tab needs the target to say
// tab; a role the target names must be the element's.
func textPickFits(snap view.Snapshot, idx int, sel view.Selector, text string) bool {
	n := snap.Tree.Nodes[idx]
	targetWords := wordSet(sel.Name + " " + text)
	roleWords := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		roleWords[strings.Trim(w, `"'.,`)] = true
	}
	own := view.StripObjects(n.Name) + " " + view.Label(snap.Tree.Nodes, idx)
	if n.Value != nil {
		own += " " + *n.Value
	}
	shared := false
	for w := range wordSet(own) {
		if targetWords[w] && !roleNoun[w] {
			shared = true
			break
		}
	}
	if !shared {
		return false
	}
	if n.Role == "tab" && !roleWords["tab"] {
		return false
	}
	if len(sel.Roles) > 0 && !roleIn(n.Role, sel.Roles) {
		return false
	}
	for w, role := range rolesNamed {
		if roleWords[w] && !strings.HasPrefix(n.Role, role) {
			if w == "button" && (roleWords["radio"] || roleWords["spin"] || roleWords["menu"] || roleWords["popup"] || roleWords["pop-up"]) {
				continue
			}
			return false
		}
	}
	return true
}

// rolesNamed are words of a loose target that name the element's role.
var rolesNamed = map[string]string{"button": "button", "link": "link", "checkbox": "checkbox", "tab": "tab", "radio": "radio", "slider": "slider"}

// roleNoun are role words that never count as a shared name word.
var roleNoun = map[string]bool{"button": true, "link": true, "the": true, "a": true, "tab": true, "field": true, "box": true, "checkbox": true, "radio": true, "menu": true, "item": true}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		out[w] = true
	}
	return out
}

// ---------------------------------------------------------------------------
// Visual grounding

// Grounded check bars.
const (
	CropBar = 0.9  // the close-up crop around the point
	ZoomBar = 0.65 // the upscaled zoom of a small or unknown element
	RingBar = 0.95 // the full screen with a ring at the point
	TextBar = 0.7  // the text check without a vision model
	// SmallArea is the element area below which the zoom check runs.
	SmallArea = 5000
)

// Root roles a grounded click never lands on.
var rootRoles = map[string]bool{"window": true, "dialog": true, "sheet": true, "application": true, "document": true, "webarea": true, "frame": true, "alert": true, "panel": true}

// hit is a hit test's verdict on a point.
type hit struct {
	node *view.RawNode
	code string // "" when the point is fine
	why  string
}

// hitAt hit-tests a point for a click in window w.
func (r *planRun) hitAt(ctx context.Context, x, y int, w view.RawWindow, allowRoot bool) hit {
	node, path, win, covered, err := r.e.X.B.At(ctx, x, y)
	switch {
	case err != nil:
		return hit{code: "read_failed", why: fmt.Sprintf("reading the element at (%d, %d) failed: %v", x, y, err)}
	case node == nil:
		return hit{code: "no_element", why: fmt.Sprintf("no accessible element is at (%d, %d)", x, y)}
	case covered != nil:
		return hit{node: node, code: "covered", why: fmt.Sprintf("the window %q covers (%d, %d)", covered.Title, x, y)}
	}
	desc := describeNode(*node)
	if !allowRoot && (rootRoles[node.Role] || (w.Handle != nil && r.e.X.B.Same(node.Handle, w.Handle))) {
		return hit{node: node, code: "root", why: fmt.Sprintf("the point lands on %s, a window or document root, not a control", desc)}
	}
	inPath := false
	for _, p := range path {
		if w.Handle != nil && r.e.X.B.Same(p.Handle, w.Handle) {
			inPath = true
		}
	}
	switch {
	case win == nil && !inPath:
		return hit{node: node, code: "other_process", why: fmt.Sprintf("%s belongs to an unknown process", desc)}
	case win != nil && win.PID != w.PID && !inPath:
		return hit{node: node, code: "other_process", why: fmt.Sprintf("%s belongs to %s (pid %d), not the plan's app", desc, orStr(win.App, "another app"), win.PID)}
	case win != nil && win.PID == w.PID && win.Key != w.Key && win.OwnerKey != w.Key && !inPath:
		return hit{node: node, code: "other_window", why: fmt.Sprintf("%s is in the window %q, which the plan's window does not own", desc, win.Title)}
	}
	return hit{node: node}
}

func describeNode(n view.RawNode) string {
	d := orStr(n.Role, "element")
	if n.Name != "" {
		d += " " + quote(n.Name)
	}
	return d
}

// groundTarget finds a target on screen with the ground model, hit-tests
// the point, checks it visually and checks it again right before the click.
func (r *planRun) groundTarget(ctx context.Context, s protocol.Step, mode string, w view.RawWindow, text string, recorded bool) (resolved, *stop) {
	var out resolved
	if r.background(mode) {
		if mode != protocol.InputAuto {
			return out, &stop{err: &protocol.Error{Code: protocol.ErrTargetNotFound, Message: fmt.Sprintf("no %q in %q, and %s; nothing was sent", text, w.Title, noPixels)}}
		}
		if st := r.takeForeground(ctx); st != nil {
			return out, st
		}
		out.fg = true
	}
	x, y, served, st := r.groundPoint(ctx, text)
	out.served = served
	if st != nil {
		return out, st
	}
	px, py := int(math.Round(x)), int(math.Round(y))
	h := r.hitAt(ctx, px, py, w, recorded)
	if h.code != "" {
		out.notes = append(out.notes, "ground check: "+h.code+"; "+h.why)
		return out, &stop{err: &protocol.Error{Code: protocol.ErrHitMismatch, Message: fmt.Sprintf("grounding put %q at (%d, %d), but %s; nothing was clicked", text, px, py, h.why), Hint: "observe the window and name the target exactly"}}
	}
	pass, trail, sv, st := r.checkPoint(ctx, x, y, *h.node, text)
	out.served = append(out.served, sv...)
	out.notes = append(out.notes, "ground check: "+trail+"; element "+describeNode(*h.node))
	if st != nil {
		return out, st
	}
	if !pass {
		return out, &stop{err: &protocol.Error{Code: protocol.ErrHitMismatch, Message: fmt.Sprintf("the grounded point (%d, %d) for %q passed no visual check (%s); nothing was clicked", px, py, text, trail), Hint: "observe the window and name the target exactly"}}
	}
	again := r.hitAt(ctx, px, py, w, recorded)
	if again.code != "" || again.node == nil || !r.e.X.B.Same(again.node.Handle, h.node.Handle) {
		why := again.why
		if why == "" {
			why = "another element is there now"
		}
		return out, &stop{err: &protocol.Error{Code: protocol.ErrHitMismatch, Message: fmt.Sprintf("the grounded point (%d, %d) changed right before the click: %s; nothing was clicked", px, py, why)}}
	}
	out.target = &protocol.Target{Point: &protocol.Point{X: float64(px), Y: float64(py)}}
	out.how, out.grounded = "ground", true
	return out, nil
}

// groundPoint asks the ground model where text is on the screen.
func (r *planRun) groundPoint(ctx context.Context, text string) (float64, float64, []protocol.Served, *stop) {
	shot, err := r.e.Screen.Shot(ctx)
	if err != nil {
		return 0, 0, nil, &stop{err: &protocol.Error{Code: protocol.ErrUnavailable, Message: "the screen capture for grounding failed: " + err.Error()}}
	}
	b64, err := pngB64(shot.Frame)
	if err != nil {
		return 0, 0, nil, &stop{err: engineErr(err)}
	}
	g, err := r.e.models().Ground(ctx, models.GroundArgs{PNG: b64, Target: text, Area: models.Area{Width: float64(shot.Frame.W), Height: float64(shot.Frame.H)}})
	if err != nil {
		return 0, 0, nil, &stop{err: engineErr(err)}
	}
	x := shot.Origin[0] + float64(g.X)*shot.Scale
	y := shot.Origin[1] + float64(g.Y)*shot.Scale
	return x, y, []protocol.Served{g.Served}, nil
}

// checkPoint runs the grounded check ladder: the crop (CropBar), the zoom
// for a small or unknown element (ZoomBar), the ringed screen (RingBar);
// without a vision model the text check (TextBar). trail lists every score.
func (r *planRun) checkPoint(ctx context.Context, x, y float64, n view.RawNode, text string) (bool, string, []protocol.Served, *stop) {
	var served []protocol.Served
	var trail []string
	ask := func(prompt string, img *vision.Frame) (float64, *stop) {
		var state []any
		state = append(state, prompt)
		if img != nil {
			b64, err := pngB64(img)
			if err != nil {
				return 0, &stop{err: engineErr(err)}
			}
			state = append(state, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64," + b64}})
		}
		sj, _ := json.Marshal(state)
		qj, _ := json.Marshal(map[string]any{"hit": map[string]any{"type": models.Noul, "instructions": prompt}})
		d, err := r.e.models().Decide(ctx, models.DecideArgs{State: sj, Questions: qj, Image: img != nil})
		if err != nil {
			if st := creditsStop(err); st != nil {
				return 0, st
			}
			return 0, nil
		}
		served = append(served, d.Served)
		return d.Answers["hit"].Probability, nil
	}
	if !r.e.VisionModel || r.e.Screen == nil {
		p, st := ask(fmt.Sprintf("A click would land on %s. Is that the target %q?", describeNode(n), text), nil)
		trail = append(trail, fmt.Sprintf("text %.2f", p))
		return st == nil && p >= TextBar, strings.Join(trail, ", "), served, st
	}
	shot, err := r.e.Screen.Shot(ctx)
	if err != nil {
		return false, "capture failed", served, nil
	}
	crop, at := cropAround(shot, x, y, vision.CheckCropW, vision.CheckCropH)
	p, st := ask(fmt.Sprintf("This is part of the screen; the click point is pixel (%d, %d) of this image. Is the point on the target %q?", at[0], at[1], text), crop)
	trail = append(trail, fmt.Sprintf("crop %.2f", p))
	if st != nil || p >= CropBar {
		return st == nil, strings.Join(trail, ", "), served, st
	}
	area := -1
	if n.Bounds != nil {
		area = n.Bounds[2] * n.Bounds[3]
	}
	if area < 0 || area < SmallArea {
		z, zat := cropAround(shot, x, y, vision.ZoomEdge/2, vision.ZoomEdge*3/8)
		z = upscale(z, vision.ZoomEdge, vision.ZoomEdge*3/4)
		p, st := ask(fmt.Sprintf("This is a close-up of the screen; the click point is near pixel (%d, %d). Is the point on the target %q?", zat[0]*2, zat[1]*2, text), z)
		trail = append(trail, fmt.Sprintf("zoom %.2f", p))
		if st != nil || p >= ZoomBar {
			return st == nil, strings.Join(trail, ", "), served, st
		}
	}
	if !r.e.NoRingCheck {
		ringed := shot.Frame.Clone()
		cx, cy := int((x-shot.Origin[0])/shot.Scale), int((y-shot.Origin[1])/shot.Scale)
		vision.Ring(ringed, cx, cy, vision.RingPoints)
		p, st := ask(fmt.Sprintf("A red ring marks a point on this screen. Is the ringed point on the target %q?", text), ringed)
		trail = append(trail, fmt.Sprintf("ring %.2f", p))
		if st != nil || p >= RingBar {
			return st == nil, strings.Join(trail, ", "), served, st
		}
	}
	return false, strings.Join(trail, ", "), served, nil
}

// cropAround cuts a w by h desktop-unit crop centred on a desktop point,
// placed relative to the capture's origin; a crop at the edge is narrowed
// by the clamped amount. It answers the point's pixel inside the crop.
func cropAround(shot *Shot, x, y float64, w, h int) (*vision.Frame, [2]int) {
	k := shot.Scale
	if !vision.Positive(k) {
		k = 1
	}
	px, py := (x-shot.Origin[0])/k, (y-shot.Origin[1])/k
	pw, ph := float64(w)/k, float64(h)/k
	rect := vision.Rect{X: int(math.Round(px - pw/2)), Y: int(math.Round(py - ph/2)), W: int(math.Round(pw)), H: int(math.Round(ph))}
	f, got, err := shot.Frame.Crop(rect)
	if err != nil {
		return vision.NewFrame(1, 1), [2]int{}
	}
	return f, [2]int{int(math.Round(px)) - got.X, int(math.Round(py)) - got.Y}
}

// upscale stretches a frame to w by h, nearest neighbour.
func upscale(f *vision.Frame, w, h int) *vision.Frame {
	out := vision.NewFrame(w, h)
	for y := 0; y < h; y++ {
		sy := min(f.H-1, y*f.H/h)
		for x := 0; x < w; x++ {
			sx := min(f.W-1, x*f.W/w)
			rr, g, b := f.RGB(sx, sy)
			out.Set(x, y, rr, g, b)
		}
	}
	return out
}

func pngB64(f *vision.Frame) (string, error) {
	data, err := vision.EncodePNG(f)
	if err != nil {
		return "", err
	}
	return vision.Encoded{Data: data, Format: vision.FormatPNG}.Base64(), nil
}
