// Package view turns what a desktop backend reports into the agent's view:
// window and element refs, the text outline, selectors and matching. It is
// pure Go; the macOS backend (internal/desk) implements Source.
package view

import "context"

// Handle is a backend's opaque element handle (an *ax.Element on macOS).
type Handle any

// RawWindow is one top-level window or sheet as the backend reads it.
type RawWindow struct {
	// Key is the backend's stable identity (window server id, or an element
	// hash for sheets).
	Key    string
	Handle Handle
	App    string
	PID    int
	Title  string
	Role   string // normalized: window, dialog, sheet, panel
	Active bool
	// Bounds in desktop coordinates (pixels of the main display); nil when
	// unknown.
	Bounds *[4]int
	// OwnerKey is the Key of the window a sheet hangs on.
	OwnerKey string
	Space    string // "", or "other" for another Space
	Hidden   string // "", "minimized" or "hidden"
	Stale    bool   // the app did not answer; this is its last known state
}

// RawNode is one element of a window's tree, in walk order (pre-order).
type RawNode struct {
	Handle Handle
	// Key is the backend's stable identity of the element, optional; an
	// element seen again with the same Key in the same window keeps its ref.
	Key    string
	Parent int // index into the walk; -1 for the root
	Depth  int
	Role   string // normalized role, see roles.go
	Name   string
	// Desc is secondary text (AX description or help) used for matching
	// when Name is empty.
	Desc   string
	Value  *string
	States []string // focused, selected, checked, mixed, expanded, disabled, protected, ...
	Bounds *[4]int  // desktop coordinates
	URL    string   // a link's target
	// Page is the address of the web page holding the element (web reads).
	Page  string
	DOMID string
	Keys  string // menu shortcut, such as cmd+s

	// Detail, read only with TreeOptions.Detail: the spoken role, the raw
	// AXDescription, AXIdentifier and AXHelp, and the element's actions
	// other than the press, by their spoken names. A settable value shows
	// as the "settable" state.
	RoleDesc    string
	Description string
	Identifier  string
	Help        string
	Actions     []string
}

// Tree is a window's walk.
type Tree struct {
	Window    RawWindow
	Nodes     []RawNode
	Truncated bool // the 2500-node cap or the depth cap cut it
}

// TreeOptions shape a walk.
type TreeOptions struct {
	Menus bool // include the app's menu bar
	URLs  bool
	Web   bool // web address and DOM ids
	// Detail reads the RawNode detail fields (more calls per element).
	Detail bool
}

// Limits of one tree.
const (
	MaxNodes = 2500
	MaxDepth = 64
)

// Source is what a desktop backend provides.
type Source interface {
	// Windows lists top-level windows and sheets, front to back.
	Windows(ctx context.Context) ([]RawWindow, error)
	Tree(ctx context.Context, w RawWindow, opts TreeOptions) (Tree, error)
	// Read returns one element's value; full reads up to MaxText characters
	// (from the end when tail).
	Read(ctx context.Context, h Handle, full, tail bool) (RawNode, bool, error)
	// At returns the element at a desktop point, its ancestors (innermost
	// first) and the window covering the point if it is not the element's.
	At(ctx context.Context, x, y int) (node *RawNode, path []RawNode, window *RawWindow, covered *RawWindow, err error)
	// Focused returns the keyboard focus within an app.
	Focused(ctx context.Context, pid int) (*RawNode, error)
}

// Text limits.
const (
	PreviewChars = 80
	MaxText      = 65536
)

// PointInWindow is a Source that hit-tests a point within one window's app,
// even where another app's window lies on top.
type PointInWindow interface {
	AtIn(ctx context.Context, w RawWindow, x, y int) (node *RawNode, path []RawNode, window *RawWindow, covered *RawWindow, err error)
}
