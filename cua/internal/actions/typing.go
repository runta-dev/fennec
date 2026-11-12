package actions

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"fennec/cua/internal/driver"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Read-back after typing the driver did not confirm.
const (
	ReadbackPoll      = 10 * time.Millisecond
	ReadbackMax       = 2 * time.Second        // the whole window
	ReadbackStill     = 500 * time.Millisecond // changed without growing, then still
	ReadbackUntouched = 700 * time.Millisecond // never took the text
)

// typeArgs is a type_text call's target and pacing.
func (c *stepCtx) typeArgs(w *view.RawWindow, text string, chromium bool) map[string]any {
	args := map[string]any{"text": text, "literal": true}
	if c.fg() || w == nil {
		args = c.desktopArgs(args)
	} else {
		args = c.windowArgs(*w, args)
	}
	switch {
	case c.fg() && BMP(text):
		args["delay_ms"] = 0
	case !c.fg() && chromium:
		args["delay_ms"] = 1
	}
	return args
}

// returnArgs is the Return key between lines, to the same target.
func (c *stepCtx) returnArgs(w *view.RawWindow) map[string]any {
	args := map[string]any{"key": "return"}
	if c.fg() || w == nil {
		return c.desktopArgs(args)
	}
	return c.windowArgs(*w, args)
}

// typeText types text literally, line by line with Return between lines;
// a trailing newline goes with the last line in the same call.
func (c *stepCtx) typeText(ctx context.Context) policy.Attempt {
	text := StripProtocolTail(*c.s.Type)
	if text == "" {
		return confirmed("nothing to type")
	}
	var clickNote []string
	if c.s.Into != nil && c.s.Into.Point != nil {
		// Type at a point: click there first, then type into the focus.
		cc := *c
		cc.s.Click = c.s.Into
		cc.kind = "click"
		a := cc.click(ctx)
		if a.Result.Status == protocol.StatusRefused || a.Result.Error != nil {
			return a
		}
		clickNote = append(clickNote, a.Result.Evidence)
	}
	w, reason, perr := c.target(ctx)
	if perr != nil {
		return refuse(perr, reason)
	}
	if !c.fg() && w == nil && !c.locked {
		return refuse(policy.BackgroundTarget("type", false), policy.ReasonNoWindow)
	}
	allowWin := w
	if allowWin == nil {
		allowWin = c.front(ctx)
	}
	if perr := c.x.policy().Allow.Window(allowWin); perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	if allowWin != nil && securityPrompt(*allowWin) {
		return refuse(securityError(*allowWin), policy.ReasonPolicy)
	}
	twin, perr := c.twins(ctx, w)
	if perr != nil {
		return refuse(perr, policy.ReasonTwins)
	}
	if c.fg() {
		if w != nil {
			if perr := c.raise(ctx, *w); perr != nil {
				return refuse(perr, "")
			}
		} else if perr := c.x.policy().Activity.Check(); perr != nil {
			return refuse(perr, "")
		}
	}
	pid := 0
	if allowWin != nil {
		pid = allowWin.PID
	}
	var notes []string
	notes = append(notes, clickNote...)
	focusOK := c.s.AnyFocus || (c.s.Into != nil && c.s.Into.Point != nil) || c.locked
	if c.s.Into != nil && c.s.Into.Point == nil {
		if perr := c.waitFocus(ctx, pid, *c.s.Into); perr != nil {
			return refuse(perr, "")
		}
		focusOK = true
	}
	var before *string
	if !focusOK && !c.fg() {
		f, perr := c.focusCheck(ctx, w)
		if perr != nil {
			return refuse(perr, "")
		}
		if f != nil {
			before = f.Value
		}
	} else if pid != 0 && !c.locked {
		if f, err := c.focused(ctx, pid, SettleBudget); err == nil && f != nil {
			before = f.Value
		}
	}
	if c.s.AnyFocus {
		notes = append(notes, "the focus was not verified: check the field before Return or Send")
	}
	chromium := pid != 0 && c.x.B.Chromium(pid)

	secret := c.s.Secret
	body, enter := text, false
	if secret && strings.HasSuffix(body, "\n") {
		body, enter = strings.TrimSuffix(body, "\n"), true
	}
	lines := strings.Split(body, "\n")
	if strings.HasSuffix(body, "\n") && len(lines) > 1 {
		// "a\nb\n": the last line carries the trailing newline.
		lines = lines[:len(lines)-1]
		lines[len(lines)-1] += "\n"
	}
	if c.req.WholeText && !secret {
		lines = []string{body}
	}
	total := utf8.RuneCountInString(body)
	landed := 0
	allConfirmed, terminal := true, false
	var advice []string
	for i, line := range lines {
		for _, chunk := range Chunks(line) {
			if chunk == "" {
				continue
			}
			tctx, cancel := context.WithTimeout(ctx, driver.TypeTimeout(chunk))
			cl := driverCall(tctx, c.x.B, "type_text", c.typeArgs(w, chunk, chromium))
			cancel()
			if cl.sent {
				c.own()
			}
			terminal = terminal || cl.reply.Terminal
			if cl.err != nil {
				if i == 0 && landed == 0 && !cl.sent {
					a := failed(cl, "typing")
					return a
				}
				return c.typeFailure(cl, i, len(lines), landed, total, chunk, terminal)
			}
			landed += utf8.RuneCountInString(chunk)
			if cl.reply.Effect != "confirmed" {
				allConfirmed = false
				if s := cl.reply.Advice(); s != "" {
					advice = append(advice, s)
				}
				if cl.reply.Terminal && !c.fg() && i < len(lines)-1 {
					perr := &protocol.Error{
						Code:    protocol.ErrNotShown,
						Message: fmt.Sprintf("line %d of %d went to a terminal that did not confirm it (%s); its Return was not sent, so the half-typed command did not run", i+1, len(lines), cl.reply.Advice()),
						Hint:    "check the terminal before going on; do not type the line again",
					}
					if i > 0 {
						perr.Message += fmt.Sprintf("; %d earlier line(s) were typed and entered", i)
					}
					return c.typedNotes(unconfirmed(fmt.Sprintf("typed %d of %d characters", landed, total), perr), terminal)
				}
			}
		}
		if i < len(lines)-1 {
			cl := driverCall(ctx, c.x.B, "press_key", c.returnArgs(w))
			if cl.sent {
				c.own()
			}
			if cl.err != nil {
				return c.typeFailure(cl, i+1, len(lines), landed, total, "", terminal)
			}
		}
	}
	evidence := fmt.Sprintf("typed %d characters", total)
	var a policy.Attempt
	switch {
	case c.locked:
		a = unconfirmed(evidence, nil)
	case allConfirmed:
		a = confirmed(evidence)
		c.settle(ctx, pid, text)
	case unseen(w):
		a = unconfirmed(evidence, nil, unseenTyped)
	default:
		a = c.readback(ctx, pid, before, body, terminal, evidence, advice)
	}
	if secret && enter {
		if a.Result.Error == nil && c.shows(ctx, pid, body) {
			a = unconfirmed(evidence, &protocol.Error{Code: protocol.ErrFocusMismatch, Message: "the password shows in the window, so its Return was not sent", Hint: "check where the text went"})
		} else if a.Result.Error == nil {
			cl := driverCall(ctx, c.x.B, "press_key", c.returnArgs(w))
			if cl.sent {
				c.own()
			}
			if cl.err != nil {
				return c.typedNotes(failed(cl, "the Return after the password"), terminal)
			}
		}
	}
	if twin != "" {
		a.Result.Status = protocol.StatusUnconfirmed
		a.Result.Notes = append(a.Result.Notes, twin+"; confirm the text before submitting")
	}
	if c.s.AnyFocus && a.Result.Status == protocol.StatusConfirmed {
		a.Result.Status = protocol.StatusUnconfirmed
	}
	a.Result.Notes = append(notes, a.Result.Notes...)
	return c.typedNotes(a, terminal)
}

func (c *stepCtx) typedNotes(a policy.Attempt, terminal bool) policy.Attempt {
	if terminal {
		a.Result.Notes = append(a.Result.Notes, "terminal: true")
	}
	return a
}

// typeFailure is a type that failed part way: it says how much went in and
// marks the step possibly landed.
func (c *stepCtx) typeFailure(cl call, line, lines, landed, total int, chunk string, terminal bool) policy.Attempt {
	a := failed(cl, "typing")
	if n := delivered(cl.reply, chunk); n > 0 {
		landed += n
	}
	if a.Result.Error == nil {
		a.Result.Error = &protocol.Error{Code: protocol.ErrInternal, Message: "typing failed: " + cl.msg}
	}
	a.Result.Status = protocol.StatusUnconfirmed
	msg := fmt.Sprintf("; %d of %d characters landed", landed, total)
	if lines > 1 {
		msg = fmt.Sprintf("; %d of %d lines were typed before it", line, lines) + msg
	}
	a.Result.Error.Message += msg
	a.Result.Error.Hint = "do not type the text again: read the field back and type only what is missing"
	a.Result.Evidence = fmt.Sprintf("typed %d of %d characters", landed, total)
	return c.typedNotes(a, terminal)
}

// delivered is how much of a failed chunk landed, from the driver's
// delivered or skipped counts; zero when it gives neither.
func delivered(r Reply, chunk string) int {
	n := utf8.RuneCountInString(chunk)
	switch {
	case r.DeliveredChars != nil:
		return min(*r.DeliveredChars, n)
	case r.SkippedChars != nil:
		return max(n-*r.SkippedChars, 0)
	}
	return 0
}

// focused reads an app's keyboard focus within a budget, so a lookup that
// never answers cannot hold the step.
func (c *stepCtx) focused(ctx context.Context, pid int, budget time.Duration) (*view.RawNode, error) {
	fctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	type res struct {
		n   *view.RawNode
		err error
	}
	ch := make(chan res, 1)
	go func() {
		n, err := c.x.B.Focused(fctx, pid)
		ch <- res{n, err}
	}()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-fctx.Done():
		return nil, fctx.Err()
	}
}

// waitFocus re-reads the focus every FocusPoll for up to FocusWait until it
// matches into; a password step needs a secure field.
func (c *stepCtx) waitFocus(ctx context.Context, pid int, into protocol.Target) *protocol.Error {
	var want *element
	var sel *view.Selector
	if into.Ref != "" {
		e, perr := c.element(ctx, into)
		if perr != nil {
			return perr
		}
		want = &e
		pid = e.win.PID
	} else {
		s, err := view.ParseSelector(into.Select)
		if err != nil {
			return asProtocol(err)
		}
		sel = &s
	}
	deadline := c.x.now().Add(FocusWait)
	var last *view.RawNode
	for {
		f, err := c.focused(ctx, pid, FocusPoll)
		if err == nil && f != nil {
			last = f
			if c.focusMatches(*f, want, sel) {
				return nil
			}
		}
		if !c.x.now().Before(deadline) {
			break
		}
		if err := c.x.sleep(ctx, FocusPoll); err != nil {
			return asProtocol(err)
		}
	}
	target := into.Ref + into.Select
	if last == nil {
		return &protocol.Error{Code: protocol.ErrFocusMismatch, Message: fmt.Sprintf("no element has the keyboard focus after %s, so %s cannot be typed into; nothing was typed", FocusWait, target), Hint: "click or focus the field first"}
	}
	if c.s.Secret && !last.HasState("protected") {
		holder := "window"
		if ws, err := c.x.B.Windows(ctx); err == nil {
			for _, w := range ws {
				if w.PID == pid && w.Active {
					holder = w.Role
					break
				}
			}
		}
		return &protocol.Error{Code: protocol.ErrFocusMismatch, Message: fmt.Sprintf("the focused %s %q is not a password field (the focus is held by a %s); nothing was typed", last.Role, last.Name, holder), Hint: "close what holds the focus or click the password field"}
	}
	return &protocol.Error{Code: protocol.ErrFocusMismatch, Message: fmt.Sprintf("the keyboard focus is on %s %q, not on %s; nothing was typed", last.Role, last.Name, target), Hint: "click the field first, or observe to see what holds the focus"}
}

func (c *stepCtx) focusMatches(f view.RawNode, want *element, sel *view.Selector) bool {
	if c.s.Secret && !f.HasState("protected") {
		return false
	}
	if want != nil {
		return c.x.B.Same(f.Handle, want.h)
	}
	f.Parent = -1
	snap := view.Snapshot{Tree: view.Tree{Nodes: []view.RawNode{f}}, Refs: []string{""}}
	m, err := view.Find(snap, *sel)
	return err == nil && len(m.Index) > 0
}

// focusCheck reads the focus before background typing: it must be a text
// field, and after a background click into the window it must be the
// clicked field.
func (c *stepCtx) focusCheck(ctx context.Context, w *view.RawWindow) (*view.RawNode, *protocol.Error) {
	f, err := c.focused(ctx, w.PID, FocusWait)
	if err != nil || f == nil || !textRoles[f.Role] {
		return nil, &protocol.Error{
			Code:    protocol.ErrFocusMismatch,
			Message: fmt.Sprintf("%s shows no focused text field; nothing was typed", orApp(w.App)),
			Hint:    "confirm the field in a screenshot, then retry with any_focus: true (the result is then unconfirmed)",
		}
	}
	c.x.mu.Lock()
	click, ok := c.x.bgClicks[w.Key]
	c.x.mu.Unlock()
	if ok && f.Bounds != nil && !inside(f.Bounds, click.x, click.y) {
		return nil, &protocol.Error{
			Code:    protocol.ErrFocusMismatch,
			Message: fmt.Sprintf("the focus did not move to the clicked field: %s %q holds it; nothing was typed", f.Role, f.Name),
			Hint:    "click the field again, or move the focus with a key such as tab",
		}
	}
	return f, nil
}

// settle waits at most SettleBudget for the focused element to show the
// typed tail, so the next step sees the text.
func (c *stepCtx) settle(ctx context.Context, pid int, text string) {
	tail := TypedTail(text)
	if pid == 0 || tail == "" {
		return
	}
	deadline := c.x.now().Add(SettleBudget)
	for c.x.now().Before(deadline) {
		left := deadline.Sub(c.x.now())
		f, err := c.focused(ctx, pid, left)
		if err == nil && f != nil && f.Value != nil && strings.Contains(*f.Value, tail) {
			return
		}
		if c.x.sleep(ctx, SettlePoll) != nil {
			return
		}
	}
}

// readback watches the focused element after typing the driver did not
// confirm. A terminal confirms only with more copies of the text than
// before; a field confirms when it shows the text, and is answered
// unconfirmed once it holds still ReadbackStill after changing without
// growing, or stays untouched for ReadbackUntouched. Nothing is retyped.
func (c *stepCtx) readback(ctx context.Context, pid int, before *string, text string, terminal bool, evidence string, advice []string) policy.Attempt {
	adv := strings.Join(advice, " ")
	if pid == 0 {
		return unconfirmed(evidence, nil, nonEmpty(adv)...)
	}
	if terminal {
		if before == nil {
			return unconfirmed(evidence, nil, "the terminal screen could not be read before typing, so the text is not confirmed; check the screen before going on")
		}
		f, err := c.focused(ctx, pid, FocusWait)
		if err == nil && f != nil && f.Value != nil && CountCopies(*f.Value, text) > CountCopies(*before, text) {
			return confirmed(evidence + "; the terminal shows it")
		}
		return unconfirmed(evidence, nil, "the terminal does not show the text (it may not echo, as at a password prompt); it was typed once, so check the screen before going on")
	}
	want := TypedTail(text)
	if want == "" {
		want = strings.TrimRight(text, "\n")
	}
	start := c.x.now()
	prev := ""
	if before != nil {
		prev = *before
	}
	orig := prev
	changed := start
	for {
		f, err := c.focused(ctx, pid, ReadbackPoll*5)
		if err != nil && ctx.Err() == nil && c.x.now().Sub(start) < ReadbackMax {
			// A slow read (a web page's tree) is read again within the
			// read-back budget rather than given up at once.
			if c.x.sleep(ctx, ReadbackPoll) != nil {
				return unconfirmed(evidence, nil, nonEmpty(adv)...)
			}
			continue
		}
		if err != nil {
			return unconfirmed(evidence, nil, nonEmpty("the text was delivered, but reading the field back failed ("+err.Error()+"); do not type it again", adv)...)
		}
		var v string
		if err == nil && f != nil && f.Value != nil {
			v = *f.Value
		}
		if want != "" && strings.Contains(v, want) {
			return confirmed(fmt.Sprintf("%s; the field reads %q", evidence, view.Clip(v, 40, true)))
		}
		if f != nil && (f.HasState("protected") || masked(v)) && v != orig {
			// A password field reads back as mask characters: its growth
			// by the typed length is the proof, and it is never quoted.
			if utf8.RuneCountInString(v)-utf8.RuneCountInString(orig) == utf8.RuneCountInString(want) {
				return confirmed(fmt.Sprintf("%s; the password field grew by %d characters", evidence, utf8.RuneCountInString(want)))
			}
		}
		now := c.x.now()
		if v != prev {
			prev, changed = v, now
		}
		switch {
		case now.Sub(start) >= ReadbackMax:
			return unconfirmed(evidence, nil, nonEmpty("the field did not show the text within 2 s; do not type it again", adv)...)
		case v == orig && now.Sub(changed) >= ReadbackUntouched:
			if strings.TrimSpace(v) == "" {
				return unconfirmed(evidence, nil, nonEmpty("the field reads blank, which proves neither that the text arrived nor that it did not; do not type it again", adv)...)
			}
			return unconfirmed(evidence, nil, nonEmpty("the field did not change; the text may not have arrived, do not type it again unchecked", adv)...)
		case v != orig && utf8.RuneCountInString(v) <= utf8.RuneCountInString(orig) && now.Sub(changed) >= ReadbackStill:
			return unconfirmed(evidence, nil, nonEmpty("the field changed without growing (it may have rewritten the text); it was not typed again", adv)...)
		case v != orig && now.Sub(changed) >= ReadbackStill:
			return unconfirmed(evidence, nil, nonEmpty(fmt.Sprintf("the field reads %q, not the typed text; it was not typed again", view.Clip(v, 40, true)), adv)...)
		}
		if c.x.sleep(ctx, ReadbackPoll) != nil {
			return unconfirmed(evidence, nil, nonEmpty(adv)...)
		}
	}
}

// shows says whether the focused element shows text in the clear.
func (c *stepCtx) shows(ctx context.Context, pid int, text string) bool {
	if pid == 0 {
		return false
	}
	f, err := c.focused(ctx, pid, SettleBudget)
	return err == nil && f != nil && f.Value != nil && !f.HasState("protected") && strings.Contains(*f.Value, text)
}

// masked says whether a value is all mask characters, as a password field
// reads back.
func masked(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if r != '•' && r != '●' && r != '*' {
			return false
		}
	}
	return true
}
