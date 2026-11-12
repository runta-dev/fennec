package view

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"fennec/cua/internal/protocol"
)

// MaxRefs caps the ref table (window and element refs together).
const MaxRefs = 50000

var refShape = regexp.MustCompile(`^([ew])([0-9]+)$`)

// Entry is what a ref stands for.
type Entry struct {
	Ref    string
	Window bool
	// Handle is the backend element (or window) handle.
	Handle Handle
	// WindowKey is the window's backend key; for an element, its window's.
	WindowKey string
	// WindowRef is the element's window ref (the ref itself for a window).
	WindowRef string
	// Node is the element as it was read (elements only); its name is the
	// original one, with embedded-object markers kept.
	Node RawNode
	// View is the view version that last handed the ref out.
	View int
}

// Registry hands out window refs (w1, ...) and element refs (e1, ...) that
// map to backend handles, and counts view versions (v1, ...).
//
// Nothing is evicted while the table holds at most its cap. A walk that
// pushes it over the cap keeps every ref it hands out; the table is trimmed
// only when a later walk begins and no other walk or action holds it, and
// the trim keeps only the refs of the windows listed last. Element refs that
// were dropped fail with a stale_ref error that asks for a fresh observe.
type Registry struct {
	mu      sync.Mutex
	limit   int
	next    int
	view    int
	holds   int
	entries map[int]*Entry  // by ref number
	byKey   map[string]int  // identity -> ref number
	listed  map[string]bool // window keys of the last listing
}

// NewRegistry makes a registry capped at limit entries (MaxRefs when 0).
func NewRegistry(limit int) *Registry {
	if limit <= 0 {
		limit = MaxRefs
	}
	return &Registry{limit: limit, entries: map[int]*Entry{}, byKey: map[string]int{}, listed: map[string]bool{}}
}

// Len is the number of refs held.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// View is the current view version, such as v7.
func (r *Registry) View() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return "v" + strconv.Itoa(r.view)
}

// Walk is one snapshot, find or hit test handing out refs. Call End when it
// is done.
type Walk struct {
	r    *Registry
	view int
	once sync.Once
}

// Begin starts a walk: it trims the table first when it is over its cap and
// nothing else holds it, then opens a new view version.
func (r *Registry) Begin() *Walk {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.holds == 0 && len(r.entries) > r.limit {
		r.trim()
	}
	r.holds++
	r.view++
	return &Walk{r: r, view: r.view}
}

// Hold keeps the table from being trimmed while an action resolves and uses
// refs; call the returned release when it is done.
func (r *Registry) Hold() (release func()) {
	r.mu.Lock()
	r.holds++
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			r.holds--
			r.mu.Unlock()
		})
	}
}

// trim keeps only the refs of the windows listed last.
func (r *Registry) trim() {
	for n, e := range r.entries {
		if !e.Window || !r.listed[e.WindowKey] {
			delete(r.entries, n)
		}
	}
	for k, n := range r.byKey {
		if _, ok := r.entries[n]; !ok {
			delete(r.byKey, k)
		}
	}
}

// View is the walk's view version.
func (w *Walk) View() string { return "v" + strconv.Itoa(w.view) }

// End closes the walk; refs it handed out stay until a later trim.
func (w *Walk) End() {
	w.once.Do(func() {
		w.r.mu.Lock()
		w.r.holds--
		w.r.mu.Unlock()
	})
}

// Windows records a window listing as the windows a trim keeps and returns
// their refs, in order.
func (w *Walk) Windows(ws []RawWindow) []string {
	r := w.r
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listed = make(map[string]bool, len(ws))
	out := make([]string, len(ws))
	for i, rw := range ws {
		r.listed[rw.Key] = true
		out[i] = r.windowLocked(rw, w.view)
	}
	return out
}

// Window returns a window's ref without changing the listing.
func (w *Walk) Window(rw RawWindow) string {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	return w.r.windowLocked(rw, w.view)
}

func (r *Registry) windowLocked(rw RawWindow, view int) string {
	id := "w\x00" + rw.Key
	if n, ok := r.byKey[id]; ok && rw.Key != "" {
		if e := r.entries[n]; e != nil {
			e.Handle, e.View = rw.Handle, view
			return e.Ref
		}
	}
	r.next++
	ref := "w" + strconv.Itoa(r.next)
	r.entries[r.next] = &Entry{Ref: ref, Window: true, Handle: rw.Handle, WindowKey: rw.Key, WindowRef: ref, View: view}
	if rw.Key != "" {
		r.byKey[id] = r.next
	}
	return ref
}

// Element returns the ref of an element of window win (its ref and key). An
// element with a Key seen before in that window keeps its ref.
func (w *Walk) Element(winRef, winKey string, n RawNode) string {
	r := w.r
	r.mu.Lock()
	defer r.mu.Unlock()
	id := ""
	if n.Key != "" {
		id = "e\x00" + winKey + "\x00" + n.Key
		if num, ok := r.byKey[id]; ok {
			if e := r.entries[num]; e != nil {
				e.Handle, e.Node, e.View, e.WindowRef = n.Handle, n, w.view, winRef
				return e.Ref
			}
		}
	}
	r.next++
	ref := "e" + strconv.Itoa(r.next)
	r.entries[r.next] = &Entry{Ref: ref, Handle: n.Handle, WindowKey: winKey, WindowRef: winRef, Node: n, View: w.view}
	if id != "" {
		r.byKey[id] = r.next
	}
	return ref
}

// Lookup resolves a ref. A malformed ref is invalid_argument; a ref the table
// no longer holds is stale_ref, with a hint to observe again.
func (r *Registry) Lookup(ref string) (Entry, error) {
	m := refShape.FindStringSubmatch(ref)
	if m == nil {
		return Entry{}, &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("%q is not a ref such as e12 or w3", ref)}
	}
	num, err := strconv.Atoi(m[2])
	if err != nil {
		return Entry{}, &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("%q is not a ref such as e12 or w3", ref)}
	}
	r.mu.Lock()
	e, ok := r.entries[num]
	r.mu.Unlock()
	if !ok || (m[1] == "w") != e.Window {
		return Entry{}, StaleRef(ref)
	}
	return *e, nil
}

// StaleRef is the error for a ref the table no longer holds.
func StaleRef(ref string) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.ErrStaleRef,
		Message: ref + " is from an older view and no longer resolves",
		Hint:    "observe again and use the fresh refs",
	}
}
