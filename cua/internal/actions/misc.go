package actions

import (
	"context"
	"fmt"
	"strings"

	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// key presses a key combination. Background keys need their target window;
// foreground keys to a named window raise it first.
func (c *stepCtx) key(ctx context.Context) policy.Attempt {
	keys, err := KeyNames(c.s.Key)
	if err != nil {
		return refuse(keyError(err), policy.ReasonPolicy)
	}
	w, reason, perr := c.target(ctx)
	if perr != nil {
		return refuse(perr, reason)
	}
	if !c.fg() && w == nil && !c.locked {
		return refuse(policy.BackgroundTarget("key", false), policy.ReasonNoWindow)
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
	if w != nil {
		// A key moves the focus deliberately: typing no longer checks it
		// against the last click.
		c.x.mu.Lock()
		delete(c.x.bgClicks, w.Key)
		c.x.mu.Unlock()
	}
	tool, args := keyCall(keys, c.fg())
	if c.fg() || w == nil {
		args = c.desktopArgs(args)
	} else {
		args = c.windowArgs(*w, args)
	}
	cl := driverCall(ctx, c.x.B, tool, args)
	if cl.sent {
		c.own()
	}
	if cl.err != nil {
		return failed(cl, "the key "+c.s.Key)
	}
	ev := "pressed " + strings.Join(keys, "+")
	var a policy.Attempt
	switch {
	case w != nil && unseen(w) && cl.reply.Effect != "confirmed":
		a = unconfirmed(ev, nil, unseenInput)
	case !c.fg() && cl.reply.Effect != "" && cl.reply.Effect != "confirmed":
		a = unconfirmed(ev, nil, nonEmpty(cl.reply.Advice())...)
	default:
		a = confirmedOr(cl, ev)
	}
	if twin != "" {
		a.Result.Status = protocol.StatusUnconfirmed
		a.Result.Notes = append(a.Result.Notes, twin)
	}
	return a
}

// focus brings a window to the front; of two same-titled windows the
// active one.
func (c *stepCtx) focus(ctx context.Context) policy.Attempt {
	name := strings.TrimSpace(c.s.Focus)
	if name == "" {
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "focus needs a window ref or title; nothing was sent"}, policy.ReasonPolicy)
	}
	w, perr := c.window(ctx, name, c.s.PID)
	if perr != nil {
		return refuse(perr, "")
	}
	if perr := c.x.policy().Allow.Window(w); perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	if w.Active {
		return confirmed(fmt.Sprintf("%q is already in front", w.Title))
	}
	fg := *c
	fg.mode = protocol.InputForeground
	if perr := fg.raise(ctx, *w); perr != nil {
		if perr.Code == protocol.ErrFocusMismatch {
			return unconfirmed("asked "+w.App+" to front "+fmt.Sprintf("%q", w.Title), nil, perr.Message)
		}
		return refuse(perr, "")
	}
	return confirmed(fmt.Sprintf("%q is in front", w.Title))
}

// closeWindow presses a window's close button and reads the window list
// back.
func (c *stepCtx) closeWindow(ctx context.Context) policy.Attempt {
	name := strings.TrimSpace(c.s.Close)
	if name == "" {
		return refuse(&protocol.Error{Code: protocol.ErrInvalid, Message: "close needs a window ref or title; nothing was sent"}, policy.ReasonPolicy)
	}
	w, perr := c.window(ctx, name, c.s.PID)
	if perr != nil {
		return refuse(perr, "")
	}
	if perr := c.x.policy().Allow.Window(w); perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	if securityPrompt(*w) {
		return refuse(securityError(*w), policy.ReasonPolicy)
	}
	if err := c.x.B.CloseWindow(ctx, *w); err != nil {
		return axFailure(element{node: view.RawNode{Role: w.Role, Name: w.Title}, win: *w}, err, "closing the window")
	}
	deadline := c.x.now().Add(CloseWait)
	for {
		listed, perr := c.refresh(ctx)
		if perr == nil {
			gone := true
			for _, l := range listed {
				gone = gone && l.Key != w.Key
			}
			if gone {
				return withChange(confirmed(fmt.Sprintf("%q closed", w.Title)), w.Title)
			}
		}
		if !c.x.now().Before(deadline) {
			return unconfirmed(fmt.Sprintf("pressed the close button of %q", w.Title), nil, "the window is still open; it may be asking to save")
		}
		if err := c.x.sleep(ctx, FrontPoll*2); err != nil {
			return unconfirmed("pressed the close button", nil)
		}
	}
}

func withChange(a policy.Attempt, closed string) policy.Attempt {
	a.Result.Changed = &protocol.Change{Closed: []string{closed}}
	return a
}

// launch asks the system to open an app, which stays in the background.
// Arguments with an option start a new instance with them; other arguments
// (URLs, files) go to the running app as documents. When the driver knows
// no app by that name, the launch is retried once by the name of a running
// app whose windows carry it.
func (c *stepCtx) launch(ctx context.Context) policy.Attempt {
	name := strings.TrimSpace(c.s.Launch)
	if perr := c.x.policy().Allow.Launch(name, c.s.Args); perr != nil {
		return refuse(perr, policy.ReasonPolicy)
	}
	args := map[string]any{"name": name}
	var urls []string
	option := false
	for _, a := range c.s.Args {
		option = option || strings.HasPrefix(strings.TrimSpace(a), "-")
	}
	if option {
		args["creates_new_application_instance"] = true
		args["additional_arguments"] = c.s.Args
	} else {
		urls = append(urls, c.s.Args...)
	}
	if c.s.URL != "" {
		urls = append(urls, c.s.URL)
	}
	if len(urls) > 0 {
		args["urls"] = urls
	}
	cl := driverCall(ctx, c.x.B, "launch_app", args)
	if cl.err != nil && !cl.sent && notFound(cl.msg) {
		if listed, perr := c.windows(ctx); perr == nil {
			if ws := view.AppWindows(listed, name); len(ws) > 0 && ws[0].App != "" && !strings.EqualFold(ws[0].App, name) {
				args["name"] = ws[0].App
				cl = driverCall(ctx, c.x.B, "launch_app", args)
				name = ws[0].App
			}
		}
	}
	if cl.err != nil {
		if !cl.sent && notFound(cl.msg) {
			return refuse(&protocol.Error{Code: protocol.ErrTargetNotFound, Message: fmt.Sprintf("no app named %q; nothing was launched", c.s.Launch), Hint: "use the app's name as Finder shows it"}, policy.ReasonPolicy)
		}
		return failed(cl, "the launch")
	}
	return confirmed("asked macOS to open "+name, "the app stays in the background; focus its window to bring it to the front")
}

func notFound(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "no installed") || strings.Contains(m, "not found") || strings.Contains(m, "no app")
}
