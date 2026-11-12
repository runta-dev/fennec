package view

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"fennec/cua/internal/protocol"
)

// Selector is a parsed semantic target.
//
// Grammar, by example:
//
//	button "Save"                    a role and a name
//	"Save"                           a name, any role
//	tab|button "Bold"                role alternatives
//	secure textbox                   a required state (secure is protected)
//	checked checkbox "Wrap"          states: checked, unchecked, mixed, focused,
//	                                 selected, disabled, enabled, expanded,
//	                                 collapsed, pressed, secure
//	button "OK" #2                   the second of several matches
//	textbox "Name" in sheet "Export" within a container or window
//	the Bold button                  unquoted: role words and states are
//	                                 picked out, the rest is the name
type Selector struct {
	Roles  []string
	Name   string
	States []string // required states; "!s" requires s to be absent
	Index  int      // 1-based; 0 when not given
	In     *Selector
}

// String renders the selector in its grammar.
func (s Selector) String() string {
	var parts []string
	for _, st := range s.States {
		parts = append(parts, stateWord(st))
	}
	if len(s.Roles) > 0 {
		parts = append(parts, strings.Join(s.Roles, "|"))
	}
	if s.Name != "" || len(s.Roles) == 0 {
		q, _ := Quote(s.Name, 1<<20)
		parts = append(parts, `"`+q+`"`)
	}
	if s.Index > 0 {
		parts = append(parts, "#"+strconv.Itoa(s.Index))
	}
	if s.In != nil {
		parts = append(parts, "in", s.In.String())
	}
	return strings.Join(parts, " ")
}

// stateWords maps selector words to required states.
var stateWords = map[string]string{
	"secure":     "protected",
	"protected":  "protected",
	"password":   "protected",
	"checked":    "checked",
	"unchecked":  "!checked",
	"mixed":      "mixed",
	"focused":    "focused",
	"selected":   "selected",
	"unselected": "!selected",
	"disabled":   "disabled",
	"enabled":    "!disabled",
	"expanded":   "expanded",
	"collapsed":  "!expanded",
	"pressed":    "pressed",
}

func stateWord(st string) string {
	switch st {
	case "protected":
		return "secure"
	case "!checked":
		return "unchecked"
	case "!selected":
		return "unselected"
	case "!disabled":
		return "enabled"
	case "!expanded":
		return "collapsed"
	}
	return st
}

// roleWords maps selector words to normalized roles.
var roleWords = map[string]string{
	"field": "textbox", "textfield": "textbox", "input": "textbox", "textarea": "textbox", "searchbox": "textbox", "searchfield": "textbox",
	"popup": "combobox", "popupbutton": "combobox", "dropdown": "combobox", "select": "combobox",
	"img": "image", "icon": "image", "picture": "image",
	"label": "text", "statictext": "text",
	"check": "checkbox", "toggle": "switch",
	"radiobutton": "radio", "tabs": "tablist",
	"item": "listitem", "outline": "tree", "outlinerow": "treeitem",
	"menubaritem": "menubaritem", "progress": "progressbar", "stepper": "spinbutton",
	"scrollview": "scrollarea", "webarea": "document", "page": "document",
}

// knownRoles are the normalized role names a selector may use as they are.
var knownRoles = map[string]bool{}

func init() {
	for _, r := range axRoles {
		knownRoles[r] = true
	}
	for _, r := range axSubroles {
		knownRoles[r] = true
	}
	for _, r := range roleDescriptions {
		knownRoles[r] = true
	}
	for _, r := range []string{"window", "dialog", "sheet", "panel", "switch", "tab", "treeitem", "listitem", "option", "listbox"} {
		knownRoles[r] = true
	}
}

// roleOf reads one role word, or "" when it is not a role.
func roleOf(word string) string {
	w := strings.ToLower(word)
	if knownRoles[w] {
		return w
	}
	return roleWords[w]
}

type token struct {
	text   string
	quoted bool
}

func tokenize(s string) ([]token, error) {
	var out []token
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '"' || r == '“' || r == '”':
			var b strings.Builder
			i++
			closed := false
			for i < len(rs) {
				c := rs[i]
				if c == '\\' && i+1 < len(rs) {
					switch rs[i+1] {
					case 'n':
						b.WriteRune('\n')
					case 't':
						b.WriteRune('\t')
					default:
						b.WriteRune(rs[i+1])
					}
					i += 2
					continue
				}
				if c == '"' || c == '”' || c == '“' {
					closed = true
					i++
					break
				}
				b.WriteRune(c)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("selector %q has an unclosed quote", s)
			}
			out = append(out, token{text: b.String(), quoted: true})
		default:
			j := i
			for j < len(rs) && !unicode.IsSpace(rs[j]) && rs[j] != '"' && rs[j] != '“' {
				j++
			}
			out = append(out, token{text: string(rs[i:j])})
			i = j
		}
	}
	return out, nil
}

// ParseSelector reads a selector. Errors are invalid_argument.
func ParseSelector(s string) (Selector, error) {
	toks, err := tokenize(s)
	if err != nil {
		return Selector{}, invalid(err.Error())
	}
	if len(toks) == 0 {
		return Selector{}, invalid("an empty selector names nothing")
	}
	sel, err := parseTokens(toks, s)
	if err != nil {
		return Selector{}, err
	}
	return sel, nil
}

func parseTokens(toks []token, src string) (Selector, error) {
	quoted := false
	for _, t := range toks {
		quoted = quoted || t.quoted
	}
	var sel Selector
	// A scope follows "in" once the target has a quoted name or a role.
	for i, t := range toks {
		if t.quoted || !strings.EqualFold(t.text, "in") || i == 0 || i == len(toks)-1 {
			continue
		}
		head := toks[:i]
		if !hasQuoted(head) && !hasRole(head) {
			continue
		}
		in, err := parseTokens(toks[i+1:], src)
		if err != nil {
			return Selector{}, err
		}
		sel.In = &in
		toks = head
		quoted = hasQuoted(head)
		break
	}
	var nameWords []string
	for _, t := range toks {
		if t.quoted {
			if sel.Name != "" {
				return Selector{}, invalid(fmt.Sprintf("selector %q has two names", src))
			}
			sel.Name = t.text
			continue
		}
		w := t.text
		if strings.HasPrefix(w, "#") {
			n, err := strconv.Atoi(w[1:])
			if err != nil || n < 1 {
				return Selector{}, invalid(fmt.Sprintf("selector %q: %s is not an index such as #2", src, w))
			}
			sel.Index = n
			continue
		}
		if st, ok := stateWords[strings.ToLower(w)]; ok {
			sel.States = append(sel.States, st)
			continue
		}
		if strings.Contains(w, "|") {
			for _, alt := range strings.Split(w, "|") {
				r := roleOf(alt)
				if r == "" {
					return Selector{}, invalid(fmt.Sprintf("selector %q: %q is not a role", src, alt))
				}
				sel.Roles = append(sel.Roles, r)
			}
			continue
		}
		if r := roleOf(w); r != "" {
			sel.Roles = append(sel.Roles, r)
			continue
		}
		if quoted {
			return Selector{}, invalid(fmt.Sprintf("selector %q: %q is not a role or state; quote the whole name", src, w))
		}
		switch strings.ToLower(w) {
		case "the", "a", "an":
			continue
		}
		nameWords = append(nameWords, w)
	}
	if !quoted {
		sel.Name = strings.Join(nameWords, " ")
		if sel.Name == "" && len(sel.Roles) > 1 && !hasPipe(toks) {
			// "Search button": the earlier role words were the name.
			var ws []string
			for _, t := range toks {
				if roleOf(t.text) != "" {
					ws = append(ws, t.text)
				}
			}
			sel.Name = strings.Join(ws[:len(ws)-1], " ")
			sel.Roles = sel.Roles[len(sel.Roles)-1:]
		}
	}
	if sel.Name == "" && len(sel.Roles) == 0 && len(sel.States) == 0 {
		return Selector{}, invalid(fmt.Sprintf("selector %q names nothing", src))
	}
	return sel, nil
}

func hasQuoted(ts []token) bool {
	for _, t := range ts {
		if t.quoted {
			return true
		}
	}
	return false
}

func hasRole(ts []token) bool {
	for _, t := range ts {
		if !t.quoted && roleOf(t.text) != "" {
			return true
		}
	}
	return false
}

func invalid(msg string) *protocol.Error {
	return &protocol.Error{Code: protocol.ErrInvalid, Message: msg, Hint: `selectors look like button "Save", "Save", textbox "Name" in sheet "Export", or button "OK" #2`}
}

// ---------------------------------------------------------------------------
// Matching

// Match tiers, best first.
const (
	TierExact       = "exact"    // the name as given
	TierName        = "name"     // the normalized name
	TierContains    = "contains" // the normalized name contains the query
	TierLabel       = "label"    // the description, or an unnamed control's inherited label
	TierLabelInside = "label-contains"
	TierRole        = "role" // no name given: role and states alone
)

// Matches are the nodes a selector picked, at one tier.
type Matches struct {
	Index []int
	Tier  string
}

// matchMode says how matching treats elements that do not show.
type matchMode int

const (
	modeFind matchMode = iota // every element counts
	modeAct                   // showing elements first; hidden menu entries as a last resort
)

// Find returns the nodes of s matching sel at the best tier, in walk order,
// at most MaxFound of them. It searches the whole tree, menus included, and
// does not report ambiguity.
func Find(s Snapshot, sel Selector) (Matches, error) {
	m, err := match(s, sel, modeFind)
	if err != nil {
		return Matches{}, err
	}
	if len(m.Index) == 0 {
		return Matches{}, notFound(s, sel)
	}
	if sel.Index > 0 {
		if sel.Index > len(m.Index) {
			return Matches{}, indexOut(s, sel, len(m.Index))
		}
		m.Index = []int{m.Index[sel.Index-1]}
	}
	if len(m.Index) > MaxFound {
		m.Index = m.Index[:MaxFound]
	}
	return m, nil
}

// Resolve returns the one node an action should take for sel. Only showing
// elements count, except that a hidden menu entry is taken when nothing
// showing matches. Several matches without an index are ambiguous; none is
// not found, with the closest candidates as the hint.
func Resolve(s Snapshot, sel Selector) (int, string, error) {
	m, err := match(s, sel, modeAct)
	if err != nil {
		return -1, "", err
	}
	switch {
	case len(m.Index) == 0:
		return -1, "", notFound(s, sel)
	case sel.Index > 0:
		if sel.Index > len(m.Index) {
			return -1, "", indexOut(s, sel, len(m.Index))
		}
		return m.Index[sel.Index-1], m.Tier, nil
	case len(m.Index) > 1:
		return -1, "", ambiguous(s, sel, m.Index)
	}
	return m.Index[0], m.Tier, nil
}

func match(s Snapshot, sel Selector, mode matchMode) (Matches, error) {
	nodes := s.Tree.Nodes
	allowed, err := scope(s, sel)
	if err != nil {
		return Matches{}, err
	}
	var pool []int
	for i := range nodes {
		if i == 0 && s.RootIsWindow {
			continue
		}
		if allowed != nil && !allowed[i] {
			continue
		}
		if !roleOK(nodes[i].Role, sel.Roles) || !statesOK(nodes[i], sel.States) {
			continue
		}
		pool = append(pool, i)
	}
	if sel.Name == "" {
		return pick(nodes, [][]int{pool}, []string{TierRole}, mode), nil
	}
	want := Norm(sel.Name)
	if want == "" {
		return Matches{}, nil
	}
	tiers := make([][]int, 5)
	names := []string{TierExact, TierName, TierContains, TierLabel, TierLabelInside}
	for _, i := range pool {
		n := nodes[i]
		name := StripObjects(n.Name)
		switch have := Norm(name); {
		case name != "" && strings.TrimSpace(name) == strings.TrimSpace(sel.Name):
			tiers[0] = append(tiers[0], i)
		case have != "" && have == want:
			tiers[1] = append(tiers[1], i)
		case have != "" && strings.Contains(have, want) && looseOK(nodes, i):
			tiers[2] = append(tiers[2], i)
		default:
			label := Norm(Label(nodes, i))
			switch {
			case label != "" && label == want:
				tiers[3] = append(tiers[3], i)
			case label != "" && strings.Contains(label, want) && looseOK(nodes, i):
				tiers[4] = append(tiers[4], i)
			}
		}
	}
	if mode == modeFind {
		// A find lists every normalized match; only an action narrows by
		// the name as given.
		tiers[1] = mergeSorted(tiers[0], tiers[1])
		tiers[0] = nil
	}
	return pick(nodes, tiers, names, mode), nil
}

func mergeSorted(a, b []int) []int {
	out := append(append([]int{}, a...), b...)
	sort.Ints(out)
	return out
}

// pick takes the best tier; in act mode the best tier with a showing match,
// else the best with a hidden menu entry.
func pick(nodes []RawNode, tiers [][]int, names []string, mode matchMode) Matches {
	if mode == modeFind {
		for t, idx := range tiers {
			if len(idx) > 0 {
				return Matches{Index: idx, Tier: names[t]}
			}
		}
		return Matches{}
	}
	for _, keep := range []func(RawNode) bool{Showing, func(n RawNode) bool { return menuRoles[n.Role] }} {
		for t, idx := range tiers {
			var got []int
			for _, i := range idx {
				if keep(nodes[i]) {
					got = append(got, i)
				}
			}
			if len(got) > 0 {
				return Matches{Index: got, Tier: names[t]}
			}
		}
	}
	return Matches{}
}

// looseOK keeps items outside any list, table or tree from matching on a
// fragment of their text.
func looseOK(nodes []RawNode, i int) bool {
	if !itemRoles[nodes[i].Role] {
		return true
	}
	for p := nodes[i].Parent; p >= 0 && p < len(nodes); p = nodes[p].Parent {
		if containerRoles[nodes[p].Role] {
			return true
		}
	}
	return false
}

// Label is the secondary text a node matches by: its description, or for an
// unnamed node without one, the name or description of the nearest
// ancestors that hold only it.
func Label(nodes []RawNode, i int) string {
	n := nodes[i]
	if d := StripObjects(n.Desc); d != "" {
		return d
	}
	if n.Name != "" {
		return ""
	}
	child := i
	for p := n.Parent; p >= 0 && p < len(nodes); p = nodes[p].Parent {
		if !onlyChild(nodes, p, child) {
			return ""
		}
		if name := StripObjects(nodes[p].Name); name != "" {
			return name
		}
		if d := StripObjects(nodes[p].Desc); d != "" {
			return d
		}
		child = p
	}
	return ""
}

// onlyChild says whether child is p's only child.
func onlyChild(nodes []RawNode, p, child int) bool {
	for j := p + 1; j < len(nodes) && nodes[j].Depth > nodes[p].Depth; j++ {
		if nodes[j].Parent == p && j != child {
			return false
		}
	}
	return true
}

func roleOK(role string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if w == role || (w == "menu" && role == "menubaritem") {
			return true
		}
	}
	return false
}

func statesOK(n RawNode, want []string) bool {
	for _, st := range want {
		if strings.HasPrefix(st, "!") {
			if n.HasState(st[1:]) {
				return false
			}
		} else if !n.HasState(st) {
			return false
		}
	}
	return true
}

// scope returns the nodes a selector's "in" part allows (nil for all).
func scope(s Snapshot, sel Selector) (map[int]bool, error) {
	if sel.In == nil {
		return nil, nil
	}
	in := *sel.In
	// The window itself.
	if (len(in.Roles) == 0 || anyRole(in.Roles, windowRoles)) && in.In == nil && in.Name != "" {
		for tier := 0; tier < TitleTiers; tier++ {
			if TitleMatches(tier, s.Window.Title, in.Name) {
				return nil, nil
			}
		}
	}
	m, err := match(s, in, modeFind)
	if err != nil {
		return nil, err
	}
	if len(m.Index) == 0 {
		return nil, &protocol.Error{
			Code:    protocol.ErrTargetNotFound,
			Message: fmt.Sprintf("no %s in %s", in.String(), s.WindowRef),
			Hint:    "observe the window to see its containers, or name the window by ref",
		}
	}
	if in.Index > 0 {
		if in.Index > len(m.Index) {
			return nil, indexOut(s, in, len(m.Index))
		}
		m.Index = []int{m.Index[in.Index-1]}
	}
	allowed := map[int]bool{}
	for _, root := range m.Index {
		for j := root + 1; j < len(s.Tree.Nodes) && s.Tree.Nodes[j].Depth > s.Tree.Nodes[root].Depth; j++ {
			allowed[j] = true
		}
	}
	return allowed, nil
}

func anyRole(roles []string, set map[string]bool) bool {
	for _, r := range roles {
		if set[r] {
			return true
		}
	}
	return false
}

// Describe renders a node for errors and hints: `e9 button "Save…"`.
func Describe(s Snapshot, i int) string {
	n := s.Tree.Nodes[i]
	out := s.Refs[i] + " " + orDefault(n.Role, "generic")
	if name := StripObjects(n.Name); name != "" {
		out += " " + Field(name, NameChars)
	} else if l := Label(s.Tree.Nodes, i); l != "" {
		out += " (" + Field(l, NameChars) + ")"
	}
	return out
}

func describeAll(s Snapshot, idx []int, max int) string {
	var parts []string
	for k, i := range idx {
		if k == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(idx)-max))
			break
		}
		parts = append(parts, Describe(s, i))
	}
	return strings.Join(parts, ", ")
}

func ambiguous(s Snapshot, sel Selector, idx []int) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.ErrTargetAmbiguous,
		Message: fmt.Sprintf("%d elements match %s in %s", len(idx), sel.String(), s.WindowRef),
		Hint:    "candidates: " + describeAll(s, idx, 10) + `; use a ref, add #N, or narrow with in`,
	}
}

func indexOut(s Snapshot, sel Selector, n int) *protocol.Error {
	return &protocol.Error{
		Code:    protocol.ErrTargetNotFound,
		Message: fmt.Sprintf("%s asks for match #%d but %d match in %s", sel.String(), sel.Index, n, s.WindowRef),
		Hint:    "use an index from 1 to " + strconv.Itoa(n),
	}
}

// notFound names the closest candidates in the hint.
func notFound(s Snapshot, sel Selector) *protocol.Error {
	e := &protocol.Error{
		Code:    protocol.ErrTargetNotFound,
		Message: fmt.Sprintf("no %s in %s", sel.String(), s.WindowRef),
	}
	if c := Closest(s, sel, 3); len(c) > 0 {
		e.Hint = "closest: " + describeAll(s, c, 3)
	} else {
		e.Hint = "observe the window for its elements; the menu bar shows with menus: true"
	}
	return e
}

// Closest ranks nodes by how near their name is to the selector's: shared
// words, then a shared prefix, then the role; at most max of them.
func Closest(s Snapshot, sel Selector, max int) []int {
	nodes := s.Tree.Nodes
	want := Norm(sel.Name)
	ww := strings.Fields(want)
	type cand struct{ i, score int }
	var cs []cand
	for i, n := range nodes {
		if i == 0 && s.RootIsWindow {
			continue
		}
		name := Norm(StripObjects(n.Name))
		if name == "" {
			name = Norm(Label(nodes, i))
		}
		score := 0
		if want != "" && name != "" {
			hw := strings.Fields(name)
			for _, w := range ww {
				for _, h := range hw {
					if w == h {
						score += 4
					} else if strings.HasPrefix(h, w) || strings.HasPrefix(w, h) {
						score += 2
					}
				}
			}
			if score == 0 && commonPrefix(name, want) >= 3 {
				score = 1
			}
		}
		if score == 0 {
			if want != "" || len(sel.Roles) == 0 {
				continue
			}
		}
		if roleOK(n.Role, sel.Roles) && len(sel.Roles) > 0 {
			score += 3
		} else if want == "" {
			continue
		}
		cs = append(cs, cand{i, score})
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].score > cs[b].score })
	var out []int
	for _, c := range cs {
		if len(out) == max {
			break
		}
		out = append(out, c.i)
	}
	return out
}

func commonPrefix(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return n
}

func hasPipe(ts []token) bool {
	for _, t := range ts {
		if !t.quoted && strings.Contains(t.text, "|") {
			return true
		}
	}
	return false
}
