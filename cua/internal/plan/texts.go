package plan

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
)

// Background read-back of a window's fields.
const (
	// ReadsAtOnce caps the field reads in flight.
	ReadsAtOnce = 4
	// ValueChars is the length from which a field's tree value may be only
	// its start (a tree read cuts long values), so its end is read.
	ValueChars = 200
)

// fieldRoles take typed text.
var fieldRoles = map[string]bool{"textbox": true, "textarea": true, "searchbox": true, "combobox": true, "spinbutton": true, "document": true}

// fieldText is one element's text as a read-back saw it.
type fieldText struct {
	value *string
	field bool
	// cut marks a value read only in part.
	cut bool
}

// windowTexts are a window's element texts by element identity.
type windowTexts map[string]fieldText

// texts reads the text of every element of a window: a value from the tree
// as it is, except that a field whose value is missing or may be cut there
// (ValueChars or longer) is read again, its end (where a terminal takes
// input) up to view.MaxText characters, with at most ReadsAtOnce reads in
// flight. nil when the window cannot be read.
func (r *planRun) texts(ctx context.Context, w view.RawWindow) windowTexts {
	tree, err := r.e.X.B.Tree(ctx, w, view.TreeOptions{})
	if err != nil {
		return nil
	}
	out := windowTexts{}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		slot = make(chan struct{}, ReadsAtOnce)
	)
	for k, n := range tree.Nodes {
		id := n.Key
		if id == "" {
			id = "#" + strconv.Itoa(k)
		}
		field := fieldRoles[n.Role]
		if n.Value != nil && !(field && utf8.RuneCountInString(*n.Value) >= ValueChars) {
			out[id] = fieldText{value: n.Value, field: field}
			continue
		}
		if !field || n.Handle == nil {
			continue
		}
		wg.Add(1)
		go func(id string, h view.Handle) {
			defer wg.Done()
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
				return
			}
			got, cut, err := r.e.X.B.Read(ctx, h, true, true)
			<-slot
			t := fieldText{field: true}
			if err == nil {
				t.value, t.cut = got.Value, cut || (got.Value != nil && utf8.RuneCountInString(*got.Value) >= view.MaxText)
			}
			mu.Lock()
			out[id] = t
			mu.Unlock()
		}(id, n.Handle)
	}
	wg.Wait()
	return out
}

// whole says whether every text was read whole.
func (t windowTexts) whole() bool {
	if t == nil {
		return false
	}
	for _, x := range t {
		if x.cut {
			return false
		}
	}
	return true
}

// shows says whether text shows more often in some element after than
// before, white space and case aside.
func shows(before, after windowTexts, text string) bool {
	if before == nil || after == nil {
		return false
	}
	want := squash(text)
	if want == "" {
		return false
	}
	count := func(t fieldText) int {
		if t.value == nil {
			return 0
		}
		return strings.Count(squash(*t.value), want)
	}
	for id, t := range after {
		if count(t) > count(before[id]) {
			return true
		}
	}
	return false
}

func squash(s string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s))
}

// readBackTyped confirms an unconfirmed background type step whose text now
// shows in its window, by the window's texts before and after.
func readBackTyped(res *protocol.StepResult, was, now windowTexts, s protocol.Step, title string) {
	if res.Status != protocol.StatusUnconfirmed || res.Error != nil || s.Secret || s.Type == nil {
		return
	}
	if !shows(was, now, *s.Type) {
		return
	}
	res.Status, res.OK, res.Retry = protocol.StatusConfirmed, true, nil
	res.Evidence = strings.TrimPrefix(res.Evidence+fmt.Sprintf("; the text shows in %q", title), "; ")
}

// blankCheck holds a replayed type step whose recording typed into an
// empty field: when that field (the step's into, else the focused field
// of its window) already holds text, nothing is typed.
func (r *planRun) blankCheck(ctx context.Context, s protocol.Step) *stop {
	if s.Recorded == nil || !s.Recorded.Blank {
		return nil
	}
	w := r.typingWindow(ctx, s)
	if w == nil {
		return nil
	}
	var n *view.RawNode
	switch {
	case s.Into != nil && s.Into.Ref != "":
		if got, _, perr := r.element(ctx, s.Into); perr == nil {
			n = &got
		}
	case s.Into != nil && s.Into.Select != "":
		sel, err := view.ParseSelector(s.Into.Select)
		if err != nil {
			return nil
		}
		snap, err := r.treeOf(ctx, *w)
		if err != nil {
			return nil
		}
		if idx, _, err := view.Resolve(snap, sel); err == nil {
			got := snap.Tree.Nodes[idx]
			n = &got
		}
	default:
		n, _ = r.e.X.B.Focused(ctx, w.PID)
	}
	if n == nil || n.Value == nil || strings.TrimSpace(*n.Value) == "" {
		return nil
	}
	return &stop{recorded: true, err: &protocol.Error{
		Code:    protocol.ErrExpectFailed,
		Message: fmt.Sprintf("%s in %q is not empty, but the recording typed into an empty field there, and typing would add to its text; nothing was typed", describeNode(*n), w.Title),
		Hint:    "open a new document (a key step such as cmd+n) or empty the field, then continue from this step",
	}}
}
