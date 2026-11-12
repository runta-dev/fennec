// Package policy is fennec-cua's safety layer for input: the input modes
// (foreground, background, auto) and what each step may ask for, the app
// allowlist with its capture blackout, confirm mode, locked-screen input and
// yielding to a user who is using the machine. Every entry point (MCP,
// scripts, CLI) passes it; no agent-facing tool can change it.
package policy

import (
	"encoding/json"
	"fmt"
	"strings"

	"fennec/cua/internal/protocol"
)

// Config is the user's policy, set from the CLI or Fennec's UI only.
type Config struct {
	// Input is the configured input mode: foreground (the default),
	// background or auto.
	Input string `json:"input,omitempty"`
	// Allowlist names the apps agents may see and drive (case-insensitive);
	// empty admits every app.
	Allowlist []string `json:"allowlist,omitempty"`
	// Confirm is off (the default), point (alias coordinate) or all (alias
	// every).
	Confirm string `json:"confirm,omitempty"`
	// LockedInput lets background keys and typing through a locked screen.
	LockedInput bool `json:"locked_input,omitempty"`
}

// Confirm modes.
const (
	ConfirmOff   = "off"
	ConfirmPoint = "point" // steps that can click at a point or element
	ConfirmAll   = "all"   // every action
)

// Load reads a config. Unknown fields and unknown values are errors, so a
// typo never silently weakens the policy.
func Load(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("policy config: %w", err)
	}
	return c.Normalize()
}

// Normalize checks a config and fills its defaults.
func (c Config) Normalize() (Config, error) {
	in, err := NormalizeInput(c.Input)
	if err != nil {
		return Config{}, err
	}
	c.Input = in
	if c.Confirm, err = NormalizeConfirm(c.Confirm); err != nil {
		return Config{}, err
	}
	var apps []string
	for _, a := range c.Allowlist {
		if a = strings.TrimSpace(a); a != "" {
			apps = append(apps, a)
		}
	}
	c.Allowlist = apps
	return c, nil
}

// NormalizeInput reads an input mode; empty is foreground.
func NormalizeInput(mode string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(mode)); m {
	case "":
		return protocol.InputForeground, nil
	case protocol.InputForeground, protocol.InputBackground, protocol.InputAuto:
		return m, nil
	}
	return "", fmt.Errorf("input mode %q is not foreground, background or auto", mode)
}

// NormalizeConfirm reads a confirm mode; empty is off.
func NormalizeConfirm(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ConfirmOff:
		return ConfirmOff, nil
	case ConfirmPoint, "coordinate":
		return ConfirmPoint, nil
	case ConfirmAll, "every":
		return ConfirmAll, nil
	}
	return "", fmt.Errorf("confirm mode %q is not off, coordinate or every", mode)
}

// Policy is the policy in force.
type Policy struct {
	Config
	// LoadErr is set when the config failed to load: every call but status
	// then fails with it.
	LoadErr error
	Allow   Allowlist
	// Activity guards foreground input against a user at the machine.
	Activity *Activity
	// Gate asks the user in confirm mode.
	Gate *Gate
}

// New makes a policy from a config; a nil confirmer can show no dialog.
func New(c Config, confirmer Confirmer) (*Policy, error) {
	c, err := c.Normalize()
	if err != nil {
		return nil, err
	}
	act := &Activity{}
	return &Policy{
		Config:   c,
		Allow:    NewAllowlist(c.Allowlist),
		Activity: act,
		Gate:     &Gate{Mode: c.Confirm, Confirmer: confirmer, Activity: act, Product: "fennec-cua"},
	}, nil
}

// Failed is the policy of a daemon whose config did not load: it holds the
// error every call but status answers.
func Failed(err error) *Policy {
	p, _ := New(Config{}, nil)
	p.LoadErr = err
	return p
}

// Admit is checked before every tool call except status: a config that did
// not load fails every call, and so does any call while a confirm dialog is
// open.
func (p *Policy) Admit(tool string) *protocol.Error {
	if p == nil || tool == protocol.ToolStatus {
		return nil
	}
	if p.LoadErr != nil {
		return &protocol.Error{Code: protocol.ErrUnavailable, Message: "the policy config failed to load: " + p.LoadErr.Error(), Hint: "fix the config in Fennec's settings; status still answers"}
	}
	if p.Gate != nil && p.Gate.Open() {
		return &protocol.Error{Code: protocol.ErrBusy, Message: "a confirm dialog is open; nothing was sent", Hint: "wait for the user to answer it, then try again"}
	}
	return nil
}

// StepMode is the input mode a step uses: the configured mode, narrowed by
// the request's and then the step's input. Under auto a request may pick
// foreground or background; under foreground or background only that mode
// may be asked for. A request for anything else is refused before anything
// is sent.
func StepMode(configured string, requested ...string) (string, *protocol.Error) {
	mode, err := NormalizeInput(configured)
	if err != nil {
		return "", &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error()}
	}
	for _, r := range requested {
		if strings.TrimSpace(r) == "" {
			continue
		}
		want, err := NormalizeInput(r)
		if err != nil {
			return "", &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error() + "; nothing was sent"}
		}
		if want == mode {
			continue
		}
		if mode == protocol.InputAuto {
			mode = want
			continue
		}
		return "", &protocol.Error{
			Code:    protocol.ErrNotAllowed,
			Message: fmt.Sprintf("the step asks for %s input but the policy in force is %s input; nothing was sent", want, mode),
			Hint:    fmt.Sprintf("leave input unset or ask for %s; only the user can change the input mode", mode),
		}
	}
	return mode, nil
}

// Reasons a refusal came before anything was sent; actions report them so
// auto input knows what it may take to the foreground.
const (
	ReasonNoWindow    = "no_window"      // the step has no target window
	ReasonDesktop     = "desktop"        // the desktop is the target
	ReasonHidden      = "hidden"         // the window is minimized or its app hidden
	ReasonOffSpace    = "off_space"      // the window is not among its app's AX windows
	ReasonPointer     = "pointer"        // the window refuses background pointer input
	ReasonAppNoWindow = "app_no_window"  // the named app has no window: never escalated
	ReasonTwins       = "twins"          // twin windows: never escalated
	ReasonLocked      = "locked"         // the screen is locked: never escalated
	ReasonPolicy      = "policy"         // the policy refused: never escalated
	ReasonDriver      = "driver_refused" // the driver refused; judged by its message
)

// Driver messages that macOS background refusals carry before sending.
const (
	macHidden   = "is minimized or its application is hidden"
	macOffSpace = "is not among the process's current AXWindows"
	classRoute  = "Background delivery is not available for target window class"
)

// autoKinds are the steps auto input tries in the background first.
var autoKinds = map[string]bool{"key": true, "type": true, "click": true, "scroll": true, "drag": true, "press": true, "set": true}

// Escalates says whether a background refusal of a step of kind may run once
// in the foreground under auto input: only a refusal known to precede any
// delivered input, and never a press (its action may still land), twins, a
// locked screen or an app without a window.
func Escalates(kind, reason, detail string) bool {
	if kind == "press" || !autoKinds[kind] {
		return false
	}
	switch reason {
	case ReasonNoWindow, ReasonDesktop, ReasonHidden, ReasonOffSpace, ReasonPointer:
		return true
	case ReasonDriver:
		return strings.Contains(detail, macHidden) || strings.Contains(detail, macOffSpace) || strings.Contains(detail, classRoute)
	}
	return false
}

// Attempt is one try of a step in one mode.
type Attempt struct {
	Result protocol.StepResult
	// Reason classifies a refusal (see the Reason constants); Detail is the
	// driver's message, if any.
	Reason string
	Detail string
}

// Auto runs a step under auto input: in the background first, and once in
// the foreground only when the background refusal came before anything was
// sent. An unconfirmed background step is kept as it is.
func Auto(kind string, background, foreground func() Attempt) Attempt {
	bg := background()
	if bg.Result.Status != protocol.StatusRefused || !Escalates(kind, bg.Reason, bg.Detail) {
		return bg
	}
	fg := foreground()
	why := bg.Detail
	if why == "" && bg.Result.Error != nil {
		why = bg.Result.Error.Message
	}
	fg.Result.Input = protocol.InputForeground
	fg.Result.Notes = append([]string{"ran in the foreground: the background route refused before sending anything (" + why + ")"}, fg.Result.Notes...)
	return fg
}

// BackgroundTarget refuses background input that has no target window: key,
// type, scroll and drag need one.
func BackgroundTarget(kind string, hasWindow bool) *protocol.Error {
	if hasWindow {
		return nil
	}
	switch kind {
	case "key", "type", "scroll", "drag":
		return &protocol.Error{
			Code:    protocol.ErrWindowNotFound,
			Message: kind + " under background input needs its target window; nothing was sent and nothing was raised",
			Hint:    "name the window (window, app or pid) the step is for",
		}
	}
	return nil
}

// PointerRefused says whether a driver message refuses background pointer
// input for a window before sending.
func PointerRefused(detail string) bool {
	return strings.Contains(detail, classRoute) || strings.Contains(detail, "refuses background pointer") || strings.Contains(detail, "background_unavailable")
}

// Vision gates vision mode: a screenshot of the whole screen and pixel or
// index input. With an allowlist both are refused; under background input
// vision input needs the foreground; foreground vision input yields to the
// user like any other.
func (p *Policy) Vision(input bool) *protocol.Error {
	if !p.Allow.Empty() {
		return &protocol.Error{Code: protocol.ErrNotAllowed, Message: "vision mode sees and clicks the whole screen, which the app allowlist forbids; nothing was sent", Hint: "use observe outlines and semantic targets"}
	}
	if !input {
		return nil
	}
	if p.Input == protocol.InputBackground {
		return &protocol.Error{Code: protocol.ErrNotAllowed, Message: "vision input needs the foreground, and the policy in force is background input; nothing was sent", Hint: "act on semantic targets (a ref or selector) with a window instead"}
	}
	return p.Activity.Check()
}
