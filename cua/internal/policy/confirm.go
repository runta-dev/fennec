package policy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"fennec/cua/internal/protocol"
)

// Confirmer asks the user whether an action may run. Fennec's UI implements
// it; it answers an error when no dialog can be shown.
type Confirmer interface {
	Confirm(ctx context.Context, question string) (bool, error)
}

// ErrNoDialog means the confirmer cannot show a dialog.
var ErrNoDialog = errors.New("no confirm dialog can be shown")

// DenyAll answers no to every question.
type DenyAll struct{}

// Confirm answers no.
func (DenyAll) Confirm(context.Context, string) (bool, error) { return false, nil }

// AllowAll answers yes to every question.
type AllowAll struct{}

// Confirm answers yes.
func (AllowAll) Confirm(context.Context, string) (bool, error) { return true, nil }

// pointKinds are the step kinds that can end in a click at a point or element.
var pointKinds = map[string]bool{"click": true, "press": true, "drag": true}

// Gated says whether confirm mode asks before a step.
func Gated(mode string, s protocol.Step) bool {
	kind, err := s.Kind()
	if err != nil || kind == "wait" || kind == "expect" {
		return false
	}
	switch mode {
	case ConfirmAll:
		return true
	case ConfirmPoint:
		// Typing at a point clicks there first.
		return pointKinds[kind] || (kind == "type" && s.Into != nil && s.Into.Point != nil)
	}
	return false
}

// NeedsConfirm says whether a list of steps asks once: when any step is gated.
func NeedsConfirm(mode string, steps []protocol.Step) bool {
	for _, s := range steps {
		if Gated(mode, s) {
			return true
		}
	}
	return false
}

// Question limits.
const (
	TextClip   = 120 // a single step's typed text
	StepClip   = 40  // each step of a list
	ListBudget = 200 // the whole list
)

// Escape shows control characters, line and paragraph separators and bidi
// controls as \u{...}, so an agent cannot add lines to or reorder the text
// the user reads.
func Escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unsafeRune(r) {
			fmt.Fprintf(&b, `\u{%X}`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func unsafeRune(r rune) bool {
	switch {
	case r == ' ' || r == ' ' || r == '؜' || r == '‎' || r == '‏':
		return true
	case r >= '‪' && r <= '‮', r >= '⁦' && r <= '⁩':
		return true
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// quoted escapes and quotes text the agent supplied.
func quoted(s string, n int) string {
	return `"` + clip(Escape(s), n) + `"`
}

var plainWord = func(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_') {
			return false
		}
	}
	return true
}

// Verb shows an action name: a plain word as it is, anything else quoted
// and escaped.
func Verb(name string) string {
	if plainWord(name) {
		return name
	}
	return quoted(name, StepClip)
}

// Describe says what one step does, for the confirm question. target names
// where it goes: the window title, else the process id, else the app in
// background mode, else "the focused window".
func Describe(s protocol.Step, target string, textClip int) string {
	kind, _ := s.Kind()
	var b strings.Builder
	b.WriteString(Verb(kind))
	where := func(t *protocol.Target) {
		switch {
		case t == nil:
		case t.Point != nil:
			fmt.Fprintf(&b, " at (%d, %d)", int(t.Point.X), int(t.Point.Y))
			if t.Point.Space != "" && t.Point.Space != protocol.SpaceScreen {
				b.WriteString(" in " + t.Point.Space + " pixels")
			}
		case t.Ref != "":
			b.WriteString(" " + t.Ref)
		case t.Select != "":
			b.WriteString(" " + quoted(t.Select, StepClip))
		}
	}
	switch kind {
	case "click", "press", "scroll", "set":
		where(firstTarget(s))
	case "drag":
		where(s.Drag)
		b.WriteString(" to")
		where(s.To)
		if s.To != nil && s.To.Point != nil {
			// "to at (x, y)" reads as "to (x, y)".
			txt := strings.Replace(b.String(), " to at (", " to (", 1)
			b.Reset()
			b.WriteString(txt)
		}
	case "type":
		text := *s.Type
		if target != "" {
			b.WriteString(" in " + quoted(target, StepClip) + ":")
		}
		if s.Secret {
			text = "••••••"
		}
		b.WriteString(" " + quoted(text, textClip))
		if s.Into != nil {
			b.WriteString(" into")
			where(s.Into)
		}
	case "key":
		if target != "" {
			b.WriteString(" in " + quoted(target, StepClip) + ":")
		}
		b.WriteString(" " + quoted(s.Key, StepClip))
	case "menu":
		b.WriteString(" " + quoted(s.Menu, StepClip))
	case "focus", "close", "launch":
		b.WriteString(" " + quoted(s.Focus+s.Close+s.Launch, StepClip))
	}
	if len(s.Mods) > 0 {
		b.WriteString(" holding " + Escape(strings.Join(s.Mods, "+")))
	}
	if target != "" && kind != "launch" && kind != "type" && kind != "key" {
		b.WriteString(" in " + quoted(target, StepClip))
	}
	return b.String()
}

func firstTarget(s protocol.Step) *protocol.Target {
	for _, t := range []*protocol.Target{s.Click, s.Press, s.Scroll, s.Set, s.Perform, s.SelectText} {
		if t != nil {
			return t
		}
	}
	return nil
}

// Question is the confirm question for steps: "Allow <product> to <what>?",
// one step in full, several listed step by step until the budget, then
// "(+N more)".
func Question(product string, steps []protocol.Step, target string) string {
	if len(steps) == 1 {
		return "Allow " + product + " to " + Describe(steps[0], target, TextClip) + "?"
	}
	var parts []string
	used := 0
	for i, s := range steps {
		part := fmt.Sprintf("%d. %s", i+1, clip(Describe(s, target, StepClip), StepClip))
		if used+len(part) > ListBudget {
			parts = append(parts, "(+"+strconv.Itoa(len(steps)-i)+" more)")
			break
		}
		used += len(part)
		parts = append(parts, part)
	}
	return "Allow " + product + " to run " + strconv.Itoa(len(steps)) + " steps: " + strings.Join(parts, "; ") + "?"
}

// Gate asks the user in confirm mode, one dialog at a time.
type Gate struct {
	Mode      string
	Confirmer Confirmer
	// Activity learns that the user's answer was not interference.
	Activity *Activity
	Product  string

	mu   sync.Mutex
	open bool
}

// Open says whether a dialog is showing.
func (g *Gate) Open() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open
}

// Ask asks about steps when confirm mode gates any of them. A no is
// declined; a dialog that cannot be shown fails with a hint to turn confirm
// off. Either way nothing was sent.
func (g *Gate) Ask(ctx context.Context, steps []protocol.Step, target string) *protocol.Error {
	if g == nil || !NeedsConfirm(g.Mode, steps) {
		return nil
	}
	product := g.Product
	if product == "" {
		product = "fennec-cua"
	}
	return g.ask(ctx, Question(product, steps, target))
}

// AskRecord asks before an agent starts a recording in confirm mode; a
// recording the user starts from the app's own menu is not asked about.
func (g *Gate) AskRecord(ctx context.Context, byAgent bool) *protocol.Error {
	if g == nil || !byAgent || g.Mode == ConfirmOff || g.Mode == "" {
		return nil
	}
	product := g.Product
	if product == "" {
		product = "fennec-cua"
	}
	return g.ask(ctx, "Allow "+product+" to record your input?")
}

func (g *Gate) ask(ctx context.Context, question string) *protocol.Error {
	if g.Confirmer == nil {
		return noDialog()
	}
	g.mu.Lock()
	if g.open {
		g.mu.Unlock()
		return &protocol.Error{Code: protocol.ErrBusy, Message: "a confirm dialog is open; nothing was sent"}
	}
	g.open = true
	g.mu.Unlock()
	ok, err := g.Confirmer.Confirm(ctx, question)
	g.mu.Lock()
	g.open = false
	g.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return &protocol.Error{Code: protocol.ErrTimeout, Message: "no answer to the confirm dialog; nothing was sent"}
		}
		return noDialog()
	}
	// The click on the dialog is the user's, but not interference.
	g.Activity.Own()
	if !ok {
		return &protocol.Error{Code: protocol.ErrDeclined, Message: "the user declined; nothing was sent", Hint: "ask the user before trying again"}
	}
	return nil
}

func noDialog() *protocol.Error {
	return &protocol.Error{Code: protocol.ErrUnavailable, Message: "confirm mode is on but no confirm dialog can be shown; nothing was sent", Hint: "turn confirm off in Fennec's settings, or open Fennec so it can ask"}
}

// Turn takes the desk turn and then asks, so a dialog never opens while
// earlier actions still type and an action that times out waiting for the
// turn never opens one. It returns the turn's release on success.
func (g *Gate) Turn(ctx context.Context, acquire func(context.Context) (func(), error), steps []protocol.Step, target string) (func(), error) {
	release, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	if perr := g.Ask(ctx, steps, target); perr != nil {
		release()
		return nil, perr
	}
	return release, nil
}
