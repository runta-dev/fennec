package ax

// FocusSource reads an app's keyboard focus two ways.
type FocusSource[N any] struct {
	// Direct is the app's focused-element query; ok is false when it
	// yields nothing.
	Direct func() (n N, ok bool)
	// InWindow says whether a directly read element belongs to the window
	// being checked.
	InWindow func(n N) bool
	// Tree reads the window's whole tree in walk order.
	Tree func() ([]N, error)
	// Focused says whether a tree node carries the focused state.
	Focused func(n N) bool
}

// FocusedElement answers the focused element: the direct query's when it
// matches the window, else the first focused node of one full tree read of
// the window, so apps with a weak focus attribute still report focus. ok
// is false when neither finds one.
func FocusedElement[N any](s FocusSource[N]) (n N, ok bool, err error) {
	if d, found := s.Direct(); found && (s.InWindow == nil || s.InWindow(d)) {
		return d, true, nil
	}
	nodes, err := s.Tree()
	if err != nil {
		return n, false, err
	}
	for _, node := range nodes {
		if s.Focused(node) {
			return node, true, nil
		}
	}
	return n, false, nil
}
