package predict

// Lock is a verified composer found on the server's screen.
type Lock struct {
	State State
	// Top and Bottom are the first and last screen rows of the composer text.
	Top, Bottom int
	// Box is the profile's own geometry (borders, chrome rows, columns).
	Box any
}

// Paint is a predicted composer: full replacement rows and the cursor.
type Paint struct {
	Rows      map[int][]Cell
	CursorRow int
	CursorCol int
	// DrawnCursor: the composer draws its own cursor in Rows (Kimi Code);
	// the terminal cursor stays hidden.
	DrawnCursor bool
}

// EditFunc applies one predicted edit.
type EditFunc func(State) State

// EditContext gives a profile what it needs to map a key.
type EditContext struct {
	// ImageReady is true when the image paste shortcut will attach an image.
	ImageReady bool
	// Pending is the number of predicted edits not yet confirmed; profiles may
	// refuse to predict layout-sensitive keys deep into an unconfirmed chain.
	Pending int
}

// Profile models one agent's composer. Implementations are stateful per
// attachment (for example, session-wide paste counters) and are only used
// under the engine's lock.
type Profile interface {
	Name() string
	// Locate finds and parses the composer, and self-checks the parse by
	// rendering it back. ok is false when no composer is shown.
	Locate(s *Screen) (Lock, bool)
	// Match finds the composer and checks that st renders exactly as shown.
	// The engine uses it when the screen alone is ambiguous (for example,
	// spaces before a newline are invisible) but its own model explains it.
	Match(s *Screen, st State) (Lock, bool)
	// Edit maps a key typed while the predicted state is st. A nil EditFunc
	// means the key is a barrier: the outcome is unknown until the server
	// shows it.
	Edit(k Key, st State, ctx EditContext) EditFunc
	// Render places st on the screen relative to the locked composer.
	// ok is false when the result cannot be predicted (for example, growth
	// into rows the profile cannot vouch for).
	Render(lock Lock, st State, s *Screen) (Paint, bool)
	// Same reports whether two states look identical on screen, which is
	// what reconciliation compares.
	Same(a, b State, s *Screen) bool
}
