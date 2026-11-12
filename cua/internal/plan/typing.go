package plan

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Terminal read-back.
const (
	// ScreenSettle is how long the screen may take to hold still after a
	// Return before the next line is checked.
	ScreenSettle = 300 * time.Millisecond
	screenPoll   = 50 * time.Millisecond
)

const terminalNote = "terminal: true"

// screen reads a window's text where typing lands: the focused element's
// value, its tail when the value is long.
func (r *planRun) screen(ctx context.Context, pid int) (string, bool) {
	f, err := r.e.X.B.Focused(ctx, pid)
	if err != nil || f == nil {
		return "", false
	}
	v := ""
	if f.Value != nil {
		v = *f.Value
	}
	if f.Handle != nil {
		if n, _, err := r.e.X.B.Read(ctx, f.Handle, true, true); err == nil && n.Value != nil {
			v = *n.Value
		}
	}
	if f.Value == nil && v == "" {
		return "", false
	}
	return v, true
}

// settled reads the screen until two reads agree, within ScreenSettle.
func (r *planRun) settled(ctx context.Context, pid int) (string, bool) {
	last, ok := r.screen(ctx, pid)
	deadline := r.e.now().Add(ScreenSettle)
	for ok && r.e.now().Before(deadline) {
		if r.e.sleep(ctx, screenPoll) != nil {
			break
		}
		next, ok2 := r.screen(ctx, pid)
		if !ok2 {
			return last, ok
		}
		if next == last {
			return next, true
		}
		last = next
	}
	return last, ok
}

// terminalType types into a terminal one line at a time: each line is
// checked against the screen and gets its Return only when it showed
// whole, or when the screen did not change at all (a prompt that does not
// echo, such as a password read). A line that showed only in part stops
// the plan before its Return. A password is typed alone and gets its
// Return only when the screen neither shows it nor changed otherwise.
func (r *planRun) terminalType(ctx context.Context, i int, s protocol.Step, mode string, w view.RawWindow) (protocol.StepResult, *stop) {
	text := strings.ReplaceAll(*s.Type, "\r\n", "\n")
	enter := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	q := r.request(mode)
	sub := func(t string) protocol.Step {
		return protocol.Step{Type: &t, Into: s.Into, AnyFocus: s.AnyFocus, Secret: s.Secret, Window: s.Window, App: s.App, PID: s.PID, Input: s.Input}
	}
	ret := protocol.Step{Key: "return", Window: s.Window, App: s.App, PID: s.PID, Input: s.Input}
	res := protocol.StepResult{Status: protocol.StatusConfirmed, OK: true}
	var notes []string
	confirmedAll := true
	typed := 0
	for k, line := range lines {
		last := k == len(lines)-1
		wantReturn := !last || enter
		if line == "" {
			if !wantReturn {
				continue
			}
			nl := "\n"
			got := r.send(ctx, i, sub(nl), q)
			if !got.OK && got.Status == protocol.StatusRefused {
				return got, &stop{err: got.Error}
			}
			continue
		}
		before, readable := r.settled(ctx, w.PID)
		got := r.send(ctx, i, sub(line), q)
		if got.Input == protocol.InputForeground && mode == protocol.InputAuto {
			r.fg = true
		}
		if got.Status == protocol.StatusRefused || (got.Error != nil && !got.OK) {
			if k > 0 {
				got.Error.Message += fmt.Sprintf("; lines 1 to %d and their Returns were sent", k)
			}
			return got, &stop{err: got.Error}
		}
		typed += len([]rune(line))
		after, readable2 := r.screen(ctx, w.PID)
		// The driver confirms a line it finds anywhere on the screen,
		// an old copy in scrollback included: with both screens read,
		// only a new copy counts.
		shown := got.Status == protocol.StatusConfirmed && !s.Secret && !(readable && readable2 && strings.TrimRight(line, " \t") != "")
		if s.Secret {
			switch {
			case !readable || !readable2:
				notes = append(notes, "the window's text could not be read, so the password's Return was sent unchecked")
			case actions.CountCopies(after, line) > actions.CountCopies(before, line):
				perr := &protocol.Error{Code: protocol.ErrNotShown, Message: "the password shows on the screen (the terminal echoes it), so its Return was not sent", Hint: "clear the line before going on; do not type the password again"}
				return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "typed the password", Error: perr, Notes: []string{terminalNote}}, &stop{err: perr}
			case after != before:
				perr := &protocol.Error{Code: protocol.ErrNotShown, Message: "the screen changed while the password was typed, but no prompt took it, so its Return was not sent", Hint: "check the terminal before going on"}
				return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: "typed the password", Error: perr, Notes: []string{terminalNote}}, &stop{err: perr}
			}
			confirmedAll = false
		} else if !shown {
			switch {
			case !readable || !readable2:
				confirmedAll = false
				notes = append(notes, fmt.Sprintf("line %d could not be read back; it was typed once", k+1))
			case actions.CountCopies(after, line) > actions.CountCopies(before, line):
			case after == before:
				confirmedAll = false
				notes = append(notes, fmt.Sprintf("the terminal did not show line %d (it may not echo, as at a password prompt); it was typed once and got its Return", k+1))
			default:
				msg := fmt.Sprintf("line %d of %d showed only in part on the terminal, so its Return was not sent", k+1, len(lines))
				if k > 0 {
					msg += "; the previous line and its Return were sent"
				}
				perr := &protocol.Error{Code: protocol.ErrNotShown, Message: msg, Hint: "check the terminal before going on; do not type the line again"}
				return protocol.StepResult{Status: protocol.StatusUnconfirmed, Evidence: fmt.Sprintf("typed %d characters", typed), Error: perr, Notes: append(notes, terminalNote)}, &stop{err: perr}
			}
		}
		if wantReturn {
			rr := r.send(ctx, i, ret, q)
			if rr.Status == protocol.StatusRefused || (rr.Error != nil && !rr.OK) {
				return rr, &stop{err: rr.Error}
			}
		}
	}
	res.Evidence = fmt.Sprintf("typed %d characters into the terminal", typed)
	if s.Secret {
		res.Evidence = "typed the password into the terminal"
	}
	if !confirmedAll {
		res.Status = protocol.StatusUnconfirmed
		no := false
		res.Retry = &no
	}
	res.Notes = append(notes, terminalNote)
	return res, nil
}

// autoType types under auto input: in the background first; the text is
// typed again in the foreground only when a read-back proves nothing
// landed (the same readable, unprotected, uncut field holding the same
// value, and the same window in front). Anything less stops the plan after
// exactly one typing call.
func (r *planRun) autoType(ctx context.Context, i int, s protocol.Step) (protocol.StepResult, *stop) {
	w := r.typingWindow(ctx, s)
	before, _ := r.list(ctx)
	var f0 *view.RawNode
	var was windowTexts
	if w != nil {
		if !s.Secret {
			was = r.texts(ctx, *w)
		}
		f0, _ = r.e.X.B.Focused(ctx, w.PID)
	}
	q := r.request(protocol.InputAuto)
	q.WholeText = !s.Secret
	res := r.send(ctx, i, s, q)
	if res.Input == protocol.InputForeground {
		r.fg = true
	}
	if res.Status != protocol.StatusUnconfirmed || res.Error != nil || res.Input == protocol.InputForeground {
		return r.judge(ctx, i, s, "type", protocol.InputAuto, res, before)
	}
	var now windowTexts
	if w != nil && !s.Secret {
		now = r.texts(ctx, *w)
		readBackTyped(&res, was, now, s, w.Title)
		if res.Status == protocol.StatusConfirmed {
			return r.judge(ctx, i, s, "type", protocol.InputAuto, res, before)
		}
	}
	after, _ := r.list(ctx)
	var f1 *view.RawNode
	if w != nil {
		f1, _ = r.e.X.B.Focused(ctx, w.PID)
	}
	why := unprovable(r, ctx, f0, f1, before, after)
	if why == "" && !s.Secret && (!was.whole() || !now.whole()) {
		// A field read only in part may hide text that landed at its end.
		why = "a field of the window was read only in part"
	}
	if why != "" {
		perr := &protocol.Error{Code: protocol.ErrNotShown, Message: "the typed text did not show, and " + why + ", so it was not typed again", Hint: "read the field before typing again"}
		res.Error = perr
		return res, &stop{err: perr}
	}
	if st := r.takeForeground(ctx); st != nil {
		return res, st
	}
	q = r.request(protocol.InputForeground)
	q.WholeText = !s.Secret
	again := r.send(ctx, i, s, q)
	again.Notes = append([]string{"the background typing left the field unchanged, so it was typed once more in the foreground"}, again.Notes...)
	out, st := r.judge(ctx, i, s, "type", protocol.InputForeground, again, after)
	// A step that fell back to the foreground hands no list on.
	r.e.dropLists()
	return out, st
}

// unprovable says why a read-back cannot prove that background typing left
// nothing behind; empty when it proves it.
func unprovable(r *planRun, ctx context.Context, f0, f1 *view.RawNode, before, after []view.RawWindow) string {
	switch {
	case f0 == nil || f1 == nil:
		return "the field could not be read"
	case f0.HasState("protected") || f1.HasState("protected"):
		return "the field is a password field whose contents cannot be seen"
	case f0.Value == nil || f1.Value == nil:
		return "the window shows no field to read"
	case !r.e.X.B.Same(f0.Handle, f1.Handle):
		return "the focus moved"
	case len(*f0.Value) >= view.MaxText || len(*f1.Value) >= view.MaxText:
		return "the field is too long to compare whole"
	case *f0.Value != *f1.Value:
		return "the field changed (the text may have landed in part)"
	}
	if f, g := frontOf(before), frontOf(after); (f == nil) != (g == nil) || (f != nil && f.Key != g.Key) {
		return "the front window changed"
	}
	if f0.Handle != nil {
		if n, cut, err := r.e.X.B.Read(ctx, f0.Handle, true, true); err == nil && cut {
			return "the field is too long to compare whole"
		} else if err == nil && n.Value != nil && *n.Value != *f1.Value && len(*n.Value) >= view.MaxText {
			return "the field is too long to compare whole"
		}
	}
	return ""
}
