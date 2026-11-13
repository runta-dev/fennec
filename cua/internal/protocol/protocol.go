// Package protocol defines fennec-cua's agent-facing MCP tools: their
// arguments, results and errors. These Go types are the schema's source of
// truth; MCP input schemas are generated from them.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Engine tool names. Six tools cover what the engine does; agents reach them
// through the REPL (the cua and fennec objects), not as MCP tools.
const (
	ToolObserve = "observe"
	ToolAct     = "act"
	ToolWait    = "wait"
	ToolRun     = "run"
	ToolFlow    = "flow"
	ToolStatus  = "status"
	// ToolApps lists running and recently used apps; the REPL's app
	// inventory reads it.
	ToolApps = "apps"
	// ToolPaste pastes text through the pasteboard (the REPL's paste).
	ToolPaste = "paste"
	// ToolBrowser carries the REPL's browser calls (cua.getBrowser,
	// createBrowserTab, tabs and their locators) to ego lite.
	ToolBrowser = "browser"
)

// Tools lists the engine's six tools.
var Tools = []string{ToolObserve, ToolAct, ToolWait, ToolRun, ToolFlow, ToolStatus}

// MCP tool names: the REPL surface (docs/codex-parity.md).
const (
	ToolJS      = "js"
	ToolJSReset = "js_reset"
	// ToolTurnEnded is the host's notice that the model's turn is over; it
	// is callable but never listed.
	ToolTurnEnded = "turn_ended"
)

// MCPTools lists every tool in the order tools/list answers them.
var MCPTools = []string{ToolJS, ToolJSReset}

// JSRequest is the js tool's input.
type JSRequest struct {
	// Code runs in the session's persistent JavaScript runtime.
	Code string `json:"code,omitempty"`
	// Title is a short present-progressive label for a person watching
	// ("Checking the form"); it never changes behaviour.
	Title string `json:"title,omitempty"`
	// CompletionSummary, sent alone, records a three-to-six word past-tense
	// headline of the finished UI task.
	CompletionSummary string `json:"completion_summary,omitempty"`
}

// AppsRequest asks for the app inventory.
type AppsRequest struct{}

// AppInfo is one app of the inventory.
type AppInfo struct {
	Name     string `json:"name"`
	BundleID string `json:"bundle_id,omitempty"`
	Path     string `json:"path,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Running  bool   `json:"running"`
	LastUsed string `json:"last_used,omitempty"`
	UseCount int    `json:"use_count,omitempty"`
}

// AppsResult is the inventory: apps and the admitted windows.
type AppsResult struct {
	Apps    []AppInfo `json:"apps"`
	Windows []Window  `json:"windows,omitempty"`
	Errors  []string  `json:"errors,omitempty"`
}

// ---------------------------------------------------------------------------
// Targets

var refPattern = regexp.MustCompile(`^[ew][0-9]+$`)

// Target names what a step acts on: exactly one of a ref from the latest
// view, a semantic selector, or a point.
//
// In JSON a target may be a bare string: "e12" and "w3" are refs, anything
// else is a selector such as `button "Save"` or `textbox "Name" in sheet
// "Save As"`.
type Target struct {
	Ref    string `json:"ref,omitempty"`
	Select string `json:"select,omitempty"`
	Point  *Point `json:"point,omitempty"`
	// Window narrows a selector or a point to one window, by ref or title.
	Window string `json:"window,omitempty"`
}

// Point is a location. Space says which pixels it counts: "screen" (desktop
// coordinates, the default), "window" (pixels of the window image the last
// observe returned) or "image" (pixels of the last vision screenshot).
type Point struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Space string  `json:"space,omitempty"`
	// View pins image-space points to the screenshot of that view, so a
	// later capture with another scale does not move the point.
	View string `json:"view,omitempty"`
}

// Point spaces.
const (
	SpaceScreen = "screen"
	SpaceWindow = "window"
	SpaceImage  = "image"
)

// UnmarshalJSON accepts a target object or its string shorthand.
func (t *Target) UnmarshalJSON(data []byte) error {
	if s := bytes.TrimSpace(data); len(s) > 0 && s[0] == '"' {
		var text string
		if err := json.Unmarshal(s, &text); err != nil {
			return err
		}
		*t = TargetFromString(text)
		return nil
	}
	type plain Target
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*t = Target(p)
	return t.Validate()
}

// TargetFromString reads the string shorthand.
func TargetFromString(s string) Target {
	s = strings.TrimSpace(s)
	if refPattern.MatchString(s) {
		return Target{Ref: s}
	}
	return Target{Select: s}
}

// Validate checks that exactly one of Ref, Select and Point is set.
func (t Target) Validate() error {
	n := 0
	if t.Ref != "" {
		n++
		if !refPattern.MatchString(t.Ref) {
			return fmt.Errorf("ref %q is not a ref such as e12 or w3", t.Ref)
		}
	}
	if t.Select != "" {
		n++
	}
	if t.Point != nil {
		n++
		switch t.Point.Space {
		case "", SpaceScreen, SpaceWindow, SpaceImage:
		default:
			return fmt.Errorf("point space %q is not screen, window or image", t.Point.Space)
		}
	}
	if n != 1 {
		return fmt.Errorf("a target needs exactly one of ref, select and point")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared shapes

// Window is one top-level window or sheet.
type Window struct {
	Ref    string  `json:"ref"`
	App    string  `json:"app,omitempty"`
	Title  string  `json:"title"`
	Role   string  `json:"role"` // window, dialog, sheet, ...
	PID    int     `json:"pid,omitempty"`
	Active bool    `json:"active"`
	Bounds *[4]int `json:"bounds,omitempty"` // x, y, w, h in desktop coordinates
	Owner  string  `json:"owner,omitempty"`  // ref of the window a sheet hangs on
	Space  string  `json:"space,omitempty"`  // "other" when on another Space
	Hidden string  `json:"hidden,omitempty"` // "minimized" or "hidden" app
	Stale  bool    `json:"stale,omitempty"`  // app did not answer; last known state
}

// Node is one accessibility element.
type Node struct {
	Ref    string   `json:"ref"`
	Role   string   `json:"role"`
	Name   string   `json:"name,omitempty"`
	Value  *string  `json:"value,omitempty"`
	States []string `json:"states,omitempty"` // focused, selected, checked, disabled, protected, ...
	Bounds *[4]int  `json:"bounds,omitempty"`
	Parent string   `json:"parent,omitempty"`
	Depth  int      `json:"depth"`
	URL    string   `json:"url,omitempty"`    // a link's target
	DOMID  string   `json:"dom_id,omitempty"` // a web element's DOM id
	// Truncated is set when Value holds only part of the element's text.
	Truncated bool `json:"truncated,omitempty"`

	// Detail fields, read only when an observe asks for detail.
	// RoleDesc is the role as macOS speaks it ("standard window",
	// "pop up button").
	RoleDesc    string `json:"role_desc,omitempty"`
	Description string `json:"description,omitempty"` // AXDescription
	Identifier  string `json:"identifier,omitempty"`  // AXIdentifier
	Help        string `json:"help,omitempty"`        // AXHelp
	// Actions are the element's accessibility actions other than the
	// press, by their spoken names ("Raise", "Show Menu").
	Actions []string `json:"actions,omitempty"`
	// AXRole is the raw accessibility role ("AXGroup").
	AXRole string `json:"ax_role,omitempty"`
	// ValueType is the kind of AXValue: "boolean", "integer", "float",
	// "string", or "unknown" for a value of another kind; empty when the
	// element has no value.
	ValueType   string `json:"value_type,omitempty"`
	Placeholder string `json:"placeholder,omitempty"` // AXPlaceholderValue
	// SelectedText is the element's selected text (AXSelectedText), read
	// for text elements.
	SelectedText string `json:"selected_text,omitempty"`
}

// Image is a capture.
type Image struct {
	MIME   string `json:"mime"` // image/png, image/jpeg, image/webp
	Data   string `json:"data"` // base64
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// Map converts image pixels to desktop coordinates:
	// desktop = origin + pixel * scale.
	Origin [2]float64 `json:"origin"`
	Scale  float64    `json:"scale"`
	// Stale is set when the window may not have repainted (another Space,
	// minimized, hidden).
	Stale bool `json:"stale,omitempty"`
}

// Mark is one numbered element of a vision screenshot.
type Mark struct {
	N      int        `json:"n"`
	Ref    string     `json:"ref,omitempty"`
	Role   string     `json:"role"`
	Name   string     `json:"name,omitempty"`
	Center [2]float64 `json:"center"` // image pixels
}

// Change says what an action or wait changed on the desktop.
type Change struct {
	Opened []string `json:"opened,omitempty"` // window titles
	Closed []string `json:"closed,omitempty"`
	Front  string   `json:"front,omitempty"` // the new front window, when it changed
	Focus  *Node    `json:"focus,omitempty"` // the element with keyboard focus afterwards, when read
}

// Usage counts model calls per model.
type Usage map[string]ModelUsage

// ModelUsage counts one model's calls.
type ModelUsage struct {
	Calls        int `json:"calls"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

// ---------------------------------------------------------------------------
// Errors

// Error is every tool's failure shape.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Hint is the next thing the agent should try.
	Hint string `json:"hint,omitempty"`
	// Observe is the current view when it helps the agent recover.
	Observe *ObserveResult `json:"observe,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// ErrorCode is a stable failure class.
type ErrorCode string

// Error codes.
const (
	ErrInvalid          ErrorCode = "invalid_argument"
	ErrTargetNotFound   ErrorCode = "target_not_found"
	ErrTargetAmbiguous  ErrorCode = "target_ambiguous"
	ErrWindowNotFound   ErrorCode = "window_not_found"
	ErrStaleRef         ErrorCode = "stale_ref" // the ref is from an older view
	ErrOffSpace         ErrorCode = "off_space" // window on another Space
	ErrCovered          ErrorCode = "covered"   // another window is on top of the target
	ErrNotResponding    ErrorCode = "app_not_responding"
	ErrFocusMismatch    ErrorCode = "focus_mismatch" // typing would go elsewhere
	ErrPageMismatch     ErrorCode = "page_mismatch"  // web step on another page
	ErrExpectFailed     ErrorCode = "expect_failed"
	ErrNotAllowed       ErrorCode = "not_allowed" // app allowlist
	ErrDeclined         ErrorCode = "declined"    // the user declined in confirm mode
	ErrStopped          ErrorCode = "stopped"     // the user stopped fennec-cua
	ErrBusy             ErrorCode = "busy"
	ErrTimeout          ErrorCode = "timeout"
	ErrPermission       ErrorCode = "permission_missing" // Accessibility or Screen Recording
	ErrSecurityPrompt   ErrorCode = "security_prompt"    // macOS protects the target from synthetic input
	ErrModelUnavailable ErrorCode = "model_unavailable"
	ErrCreditsExhausted ErrorCode = "model_credits_exhausted"
	ErrHitMismatch      ErrorCode = "hit_mismatch" // the hit test at the point found another element, a root, or another app
	ErrScreenLocked     ErrorCode = "screen_locked"
	ErrNotShown         ErrorCode = "not_shown"   // the text or window did not show after the step
	ErrUnavailable      ErrorCode = "unavailable" // not available in this build or without the driver
	ErrRunFailed        ErrorCode = "run_failed"  // the script failed; the report says how
	ErrInternal         ErrorCode = "internal"
)

// ---------------------------------------------------------------------------
// observe

// Observe scopes.
const (
	ScopeDesktop = "desktop" // windows only
	ScopeWindow  = "window"  // a window's outline (the default)
	ScopeElement = "element" // one element's value and text
	ScopePoint   = "point"   // what is at a point
)

// ObserveRequest asks what is on screen.
type ObserveRequest struct {
	Scope  string  `json:"scope,omitempty"`
	Window string  `json:"window,omitempty"` // ref or title; default the front window
	Target *Target `json:"target,omitempty"` // for scope element (ref or selector) and point (point)
	// Find limits the view to elements matching a selector, with their window.
	Find string `json:"find,omitempty"`
	// Nodes adds structured nodes next to the outline.
	Nodes bool `json:"nodes,omitempty"`
	// Menus includes the app's menu bar.
	Menus bool `json:"menus,omitempty"`
	// Text reads an element's whole text (up to 65536 characters); Tail
	// keeps its last characters instead of its first.
	Text bool `json:"text,omitempty"`
	Tail bool `json:"tail,omitempty"`
	// Image adds a capture: none (default), window, screen or region.
	Image  string  `json:"image,omitempty"`
	Region *[4]int `json:"region,omitempty"`
	// Vision returns a screen capture with numbered elements for
	// pixel-driven agents.
	Vision  bool `json:"vision,omitempty"`
	MaxEdge int  `json:"max_edge,omitempty"`
	// Web turns on the page accessibility tree of a Chromium or Electron app
	// for this session; it is turned off again when the session ends and
	// no other holder needs it. Steps with a web check do this by themselves.
	Web bool `json:"web,omitempty"`
	// Detail reads each node's role description, description, identifier,
	// help, accessibility actions and whether its value is settable (as the
	// "settable" state); it implies Nodes.
	Detail bool `json:"detail,omitempty"`
}

// ObserveResult is a view. Refs in it stay valid until the view changes.
type ObserveResult struct {
	View      string   `json:"view"`
	Window    *Window  `json:"window,omitempty"`
	Windows   []Window `json:"windows,omitempty"`
	Outline   string   `json:"outline,omitempty"`
	Nodes     []Node   `json:"nodes,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Element   *Node    `json:"element,omitempty"`
	// At answers scope point: the element there, its ancestors, and the
	// window covering the point when it is not the target's own.
	At *Hit `json:"at,omitempty"`
	// Unresponsive names a front window whose app answers no accessibility.
	Unresponsive *Window  `json:"unresponsive,omitempty"`
	Image        *Image   `json:"image,omitempty"`
	Marks        []Mark   `json:"marks,omitempty"`
	Notes        []string `json:"notes,omitempty"`
	// Resolved says how a find was answered: by name match or by a model
	// pick, with the model calls that served it.
	Resolved *Resolution `json:"resolved,omitempty"`
}

// Hit is what a point shows.
type Hit struct {
	Element *Node   `json:"element,omitempty"`
	Path    []Node  `json:"path,omitempty"`
	Window  *Window `json:"window,omitempty"`
	Covered *Window `json:"covered,omitempty"`
}

// ---------------------------------------------------------------------------
// act

// Input modes.
const (
	InputForeground = "foreground"
	InputBackground = "background"
	InputAuto       = "auto"
)

// ActRequest runs steps in order and stops at the first one that fails or
// whose expectation does not hold.
type ActRequest struct {
	Steps []Step `json:"steps"`
	// Window is the window the steps act in by default.
	Window string `json:"window,omitempty"`
	// Params fill {{name}} placeholders in step text.
	Params map[string]string `json:"params,omitempty"`
	// Input may narrow the configured input mode (auto to foreground), never widen it.
	Input string `json:"input,omitempty"`
	// TimeoutS bounds the whole call; default 90, at most 110.
	TimeoutS int `json:"timeout_s,omitempty"`
	// Observe returns the view after the last step even on success.
	Observe bool `json:"observe,omitempty"`
}

// Step is one action. Exactly one action field is set.
type Step struct {
	Press  *Target      `json:"press,omitempty"`  // the element's own action, or a hit-checked click
	Set    *Target      `json:"set,omitempty"`    // replace a field's value, check a box, pick an option; with Value
	Click  *Target      `json:"click,omitempty"`  // a pointer click; Button, Count, Mods apply
	Type   *string      `json:"type,omitempty"`   // text into the focused field, or Into
	Key    string       `json:"key,omitempty"`    // a combination such as cmd+s
	Scroll *Target      `json:"scroll,omitempty"` // with Direction, Amount
	Drag   *Target      `json:"drag,omitempty"`   // from here to To
	Menu   string       `json:"menu,omitempty"`   // "File > Export > PDF"
	Focus  string       `json:"focus,omitempty"`  // bring a window to the front
	Close  string       `json:"close,omitempty"`  // close a window
	Launch string       `json:"launch,omitempty"` // start or front an app; Args, URL apply
	Wait   *WaitRequest `json:"wait,omitempty"`
	// Perform runs the element's named accessibility action (Action, such
	// as "AXShowMenu" or its spoken name "Show Menu").
	Perform *Target `json:"perform,omitempty"`
	Action  string  `json:"action,omitempty"`
	// SelectText selects Range (character offset and length) of a text
	// element's value through accessibility; a zero length places the
	// caret.
	SelectText *Target `json:"select_text,omitempty"`
	Range      *[2]int `json:"range,omitempty"`

	// Value is the desired value for Set (string, number, or bool for a
	// check box or switch) and the desired on/off state for a Menu toggle.
	Value *json.RawMessage `json:"value,omitempty"`
	// Into is the field that must hold the keyboard focus before Type sends
	// anything; a selector may require the secure state for a password
	// field. AnyFocus types without that check, for apps that expose no focus.
	Into     *Target `json:"into,omitempty"`
	AnyFocus bool    `json:"any_focus,omitempty"`
	// Secret marks text as a password: it never appears in traces, errors,
	// saved flows or the remaining steps, and its Return is sent only when
	// the window does not show it.
	Secret    bool     `json:"secret,omitempty"`
	Button    string   `json:"button,omitempty"` // left, right, middle
	Count     int      `json:"count,omitempty"`
	Mods      []string `json:"mods,omitempty"`
	Direction string   `json:"direction,omitempty"`
	Amount    int      `json:"amount,omitempty"`
	To        *Target  `json:"to,omitempty"`
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"`

	// App and PID address an app's front window by name or process when no
	// window ref or title is known.
	App string `json:"app,omitempty"`
	PID int    `json:"pid,omitempty"`
	// Window overrides the request's window for this step.
	Window string `json:"window,omitempty"`
	// Input asks for a delivery mode for this step only. It may narrow the
	// configured mode (auto to foreground) and is refused before anything is
	// sent when it would widen it.
	Input string `json:"input,omitempty"`
	// Web requires the plan window to show a page (origin and path; query,
	// fragment and trailing slash ignored) before the step acts, and prefers
	// the element with the DOM id.
	Web *WebCheck `json:"web,omitempty"`

	Expect *Expect `json:"expect,omitempty"`
	Intent string  `json:"intent,omitempty"` // why, for traces and escalation
	// Recorded carries what a recording saw, for replay fallbacks.
	Recorded *Recorded `json:"recorded,omitempty"`
}

// WebCheck pins a step to a web page.
type WebCheck struct {
	URL   string `json:"url"`
	DOMID string `json:"dom_id,omitempty"`
}

// Kind names the step's action. A step with only Expect is an "expect" step:
// a check without an action.
func (s Step) Kind() (string, error) {
	kinds := []struct {
		name string
		set  bool
	}{
		{"press", s.Press != nil}, {"set", s.Set != nil}, {"click", s.Click != nil},
		{"type", s.Type != nil}, {"key", s.Key != ""}, {"scroll", s.Scroll != nil},
		{"drag", s.Drag != nil}, {"menu", s.Menu != ""}, {"focus", s.Focus != ""},
		{"close", s.Close != ""}, {"launch", s.Launch != ""}, {"wait", s.Wait != nil},
		{"perform", s.Perform != nil}, {"select_text", s.SelectText != nil},
	}
	found := ""
	for _, k := range kinds {
		if !k.set {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("a step has one action, not both %s and %s", found, k.name)
		}
		found = k.name
	}
	if found == "" {
		if s.Expect != nil {
			return "expect", nil
		}
		return "", fmt.Errorf("a step needs an action or an expect")
	}
	return found, nil
}

// Expect is a condition checked after a step.
type Expect struct {
	// WindowOpens matches only a window that was not there before the step.
	WindowOpens string `json:"window_opens,omitempty"`
	WindowGone  string `json:"window_gone,omitempty"`
	// Front waits for a window to come to the front by itself (default 5 s);
	// it never focuses a window that stays behind. Recorded drafts use it
	// for the window the recording saw after the step. Under background
	// input it accepts the window opening in the plan's app.
	Front  string           `json:"front,omitempty"`
	Exists *Target          `json:"exists,omitempty"`
	Gone   *Target          `json:"gone,omitempty"`
	Reads  *Target          `json:"reads,omitempty"` // with Equals
	Equals *json.RawMessage `json:"equals,omitempty"`
	// Looks is a yes/no question a vision model answers about the window.
	Looks     string `json:"looks,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

// Recorded is a recording's evidence for one click, or for one type step.
type Recorded struct {
	Text   string      `json:"text"`
	Crop   string      `json:"crop,omitempty"` // base64 PNG around the point
	Scale  float64     `json:"scale,omitempty"`
	Offset *[2]float64 `json:"offset,omitempty"`
	At     *[2]float64 `json:"at,omitempty"`   // point relative to the window
	Size   *[2]float64 `json:"size,omitempty"` // window size then
	Root   bool        `json:"root,omitempty"`
	// Element fingerprints the clicked element among same-named ones.
	Nth int `json:"nth,omitempty"`
	Of  int `json:"of,omitempty"`
	// Blank marks a type step whose recording typed into an empty field:
	// the replay types nothing when that field (the step's into, else the
	// focused field) already holds text.
	Blank bool `json:"blank,omitempty"`
}

// Step statuses.
const (
	StatusConfirmed   = "confirmed"   // the effect was read back
	StatusUnconfirmed = "unconfirmed" // sent; the effect could not be proven; do not resend unchecked
	StatusRefused     = "refused"     // nothing was sent
	StatusSkipped     = "skipped"     // not reached
)

// StepResult is one step's outcome.
type StepResult struct {
	Index    int     `json:"index"`
	Action   string  `json:"action"`
	OK       bool    `json:"ok"`
	Status   string  `json:"status"`
	Evidence string  `json:"evidence,omitempty"`
	Changed  *Change `json:"changed,omitempty"`
	// How says how a target was reached: ref, select, crop, ground, point.
	How   string `json:"how,omitempty"`
	Input string `json:"input,omitempty"` // the mode the step actually used
	Ms    int    `json:"ms"`
	Error *Error `json:"error,omitempty"`
	// Retry is false when resending the step could repeat an effect.
	Retry *bool `json:"retry,omitempty"`
	// Notes carry facts the agent needs, such as typing through a locked
	// screen or a window that may not have repainted.
	Notes []string `json:"notes,omitempty"`
	// Served lists the model calls that resolved or checked the step.
	Served []Served `json:"served,omitempty"`
}

// Served names one model call.
type Served struct {
	Role  string `json:"role"` // decide, verify, ground, ask
	Model string `json:"model"`
	Build string `json:"build,omitempty"`
	ID    string `json:"id,omitempty"`
}

// Caveat flags a step whose only proof was an accessibility action's reply
// and that no later step read back.
type Caveat struct {
	Step int    `json:"step"`
	Note string `json:"note"`
}

// ActResult is the outcome of an act call.
type ActResult struct {
	OK        bool           `json:"ok"`
	View      string         `json:"view"`
	Steps     []StepResult   `json:"steps"`
	StoppedAt *int           `json:"stopped_at,omitempty"`
	Error     *Error         `json:"error,omitempty"`
	Observe   *ObserveResult `json:"observe,omitempty"`
	// Remaining are the steps from the stopped one on, without recorded
	// crops and with secrets redacted, so the agent can finish the task.
	Remaining []Step   `json:"remaining,omitempty"`
	Caveats   []Caveat `json:"caveats,omitempty"`
	// Chain names the session's chain of passing plans this call extended;
	// flow save with from "last" stores it. A plan run after a flow stopped,
	// in the same window within 10 minutes, repairs that flow's chain.
	Chain string `json:"chain,omitempty"`
	// SavedAs names the flow a passing plan was saved as, when automatic
	// saving is on (the default).
	SavedAs string `json:"saved_as,omitempty"`
	Usage   Usage  `json:"usage,omitempty"`
}

// ---------------------------------------------------------------------------
// wait

// WaitRequest waits until a condition holds or the timeout passes.
type WaitRequest struct {
	Expect
	// Still waits until the screen stops changing for HoldMs.
	Still  bool `json:"still,omitempty"`
	HoldMs int  `json:"hold_ms,omitempty"`
	// Changed waits for any change of the window list.
	Changed bool `json:"changed,omitempty"`
}

// WaitResult says whether the condition held.
type WaitResult struct {
	Met      bool           `json:"met"`
	Ms       int            `json:"ms"`
	Changed  *Change        `json:"changed,omitempty"`
	Volatile int            `json:"volatile,omitempty"` // screen regions ignored as animated
	Observe  *ObserveResult `json:"observe,omitempty"`
}

// ---------------------------------------------------------------------------
// run

// RunRequest runs one TypeScript or JavaScript module whose main(params) gets
// the cua API.
type RunRequest struct {
	Code     string         `json:"code,omitempty"`
	Path     string         `json:"path,omitempty"` // absolute, on this machine
	Params   map[string]any `json:"params,omitempty"`
	TimeoutS int            `json:"timeout_s,omitempty"` // default 90, at most 110
}

// Run failure kinds.
const (
	RunImport    = "import" // the module does not compile or load
	RunNoMain    = "no_main"
	RunException = "exception"
	RunTimeout   = "timeout"
	RunStopped   = "stopped"
	RunCrash     = "crash" // an error escaped main after it returned
)

// RunResult is a run's report; logs survive timeouts and stops.
type RunResult struct {
	Result     any       `json:"result,omitempty"`
	Logs       string    `json:"logs"`
	DurationMs int       `json:"duration_ms"`
	Error      *RunError `json:"error,omitempty"`
	Usage      Usage     `json:"usage,omitempty"`
}

// RunError says how a run failed.
type RunError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// flow

// Flow operations.
const (
	FlowSave        = "save"
	FlowList        = "list"
	FlowShow        = "show"
	FlowRun         = "run"
	FlowDelete      = "delete"
	FlowRecordStart = "record_start"
	FlowRecordNote  = "record_note"
	FlowRecordStop  = "record_stop"
)

// FlowRequest manages saved flows and recordings.
type FlowRequest struct {
	Op          string            `json:"op"`
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Steps       []Step            `json:"steps,omitempty"`  // save
	Params      map[string]string `json:"params,omitempty"` // run: values; record_stop: names to values seen
	Apps        []string          `json:"apps,omitempty"`   // record_start: apps to record
	Text        string            `json:"text,omitempty"`   // record_note
	// Query ranks saved flows by how well they fit a task description.
	Query string `json:"query,omitempty"`
	// From "last" saves the session's latest chain of passing plans
	// instead of Steps.
	From string `json:"from,omitempty"`
}

// FlowResult answers a flow operation.
type FlowResult struct {
	Flows []FlowInfo `json:"flows,omitempty"` // list
	Flow  *Flow      `json:"flow,omitempty"`  // show, save, record_stop (the draft)
	Run   *ActResult `json:"run,omitempty"`   // run
	ID    string     `json:"id,omitempty"`    // record_start
	// Ran is false when a save stored the steps of the plan that just
	// passed without running them again.
	Ran *bool `json:"ran,omitempty"`
	// Pick answers a list with a query: the flow the model named, if any,
	// and the probability of its top choice.
	Pick *FlowPick `json:"pick,omitempty"`
}

// FlowPick is a find's answer.
type FlowPick struct {
	Flow string  `json:"flow,omitempty"`
	P    float64 `json:"p"`
}

// FlowInfo is a list entry.
type FlowInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Params      []string `json:"params,omitempty"`
	App         string   `json:"app,omitempty"`
}

// Flow is a saved or drafted plan.
type Flow struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Params      map[string]FlowParam `json:"params,omitempty"`
	Steps       []Step               `json:"steps"`
	Notes       []FlowNote           `json:"notes,omitempty"`
	Gaps        []int                `json:"gaps,omitempty"`      // steps where the recording skipped input
	Recording   string               `json:"recording,omitempty"` // the recording file a draft came from
}

// FlowParam describes one parameter.
type FlowParam struct {
	Example string `json:"example,omitempty"`
	Secret  bool   `json:"secret,omitempty"`
}

// FlowNote is a recording note placed before a step.
type FlowNote struct {
	Before int    `json:"before"`
	Text   string `json:"text"`
}

// ---------------------------------------------------------------------------
// status

// StatusResult describes the host.
type StatusResult struct {
	Version     string            `json:"version"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	Permissions map[string]string `json:"permissions"` // accessibility, screen_recording: granted, denied, unknown
	Input       string            `json:"input"`
	Stopped     bool              `json:"stopped"`
	Allowlist   []string          `json:"allowlist,omitempty"`
	Confirm     string            `json:"confirm,omitempty"` // off, point, all
	Run         bool              `json:"run"`               // whether the run tool is enabled
	Locked      bool              `json:"locked"`            // the screen is locked
	Screen      [2]int            `json:"screen"`            // main display size in desktop coordinates
}

// Resolution explains how a target was found.
type Resolution struct {
	How    string   `json:"how"` // name, model, crop, ground, point
	Served []Served `json:"served,omitempty"`
	Ms     int      `json:"ms"`
}

// JSONSchema says that a target is a ref or selector string, or an object.
func (Target) JSONSchema() map[string]any {
	point := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x":     map[string]any{"type": "number"},
			"y":     map[string]any{"type": "number"},
			"space": map[string]any{"type": "string", "enum": []string{SpaceScreen, SpaceWindow, SpaceImage}},
			"view":  map[string]any{"type": "string"},
		},
		"required":             []string{"x", "y"},
		"additionalProperties": false,
	}
	return map[string]any{"anyOf": []any{
		map[string]any{"type": "string", "description": `a ref such as "e12" or "w3", or a selector such as 'button "Save"'`},
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ref":    map[string]any{"type": "string"},
				"select": map[string]any{"type": "string"},
				"point":  point,
				"window": map[string]any{"type": "string"},
			},
			"additionalProperties": false,
		},
	}}
}
