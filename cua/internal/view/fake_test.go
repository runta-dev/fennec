package view

import (
	"context"
	"errors"
	"testing"

	"fennec/cua/internal/protocol"
)

// fakeSource is a desktop in memory.
type fakeSource struct {
	windows []RawWindow
	trees   map[string]Tree // by window key
	reads   map[Handle]RawNode
	at      fakeAt

	treeCalls []TreeOptions
	treeKeys  []string
	readCalls int
}

type fakeAt struct {
	node    *RawNode
	path    []RawNode
	window  *RawWindow
	covered *RawWindow
}

func (f *fakeSource) Windows(context.Context) ([]RawWindow, error) {
	return append([]RawWindow(nil), f.windows...), nil
}

func (f *fakeSource) Tree(_ context.Context, w RawWindow, opts TreeOptions) (Tree, error) {
	f.treeCalls = append(f.treeCalls, opts)
	f.treeKeys = append(f.treeKeys, w.Key)
	t, ok := f.trees[w.Key]
	if !ok {
		return Tree{}, errors.New("no tree")
	}
	t.Window = w
	if !opts.Menus {
		t.Nodes = withoutMenus(t.Nodes)
	}
	return t, nil
}

// withoutMenus drops menu bar subtrees, as a backend walk without menus does.
func withoutMenus(nodes []RawNode) []RawNode {
	var out []RawNode
	index := map[int]int{}
	skipDepth := -1
	for i, n := range nodes {
		if skipDepth >= 0 && n.Depth > skipDepth {
			continue
		}
		skipDepth = -1
		if n.Role == "menubar" {
			skipDepth = n.Depth
			continue
		}
		index[i] = len(out)
		if n.Parent >= 0 {
			n.Parent = index[n.Parent]
		}
		out = append(out, n)
	}
	return out
}

func (f *fakeSource) Read(_ context.Context, h Handle, full, tail bool) (RawNode, bool, error) {
	f.readCalls++
	n, ok := f.reads[h]
	if !ok {
		return RawNode{}, false, errors.New("gone")
	}
	return n, false, nil
}

func (f *fakeSource) At(context.Context, int, int) (*RawNode, []RawNode, *RawWindow, *RawWindow, error) {
	return f.at.node, f.at.path, f.at.window, f.at.covered, nil
}

func (f *fakeSource) Focused(context.Context, int) (*RawNode, error) { return nil, nil }

// spec is one node of a test tree.
type spec struct {
	depth  int
	role   string
	name   string
	desc   string
	value  *string
	states []string
	bounds *[4]int
	url    string
	keys   string
}

// build turns specs (in pre-order, with depths) into nodes with parents and
// distinct handles.
func build(specs ...spec) []RawNode {
	nodes := make([]RawNode, len(specs))
	var stack []int
	for i, s := range specs {
		for len(stack) > 0 && specs[stack[len(stack)-1]].depth >= s.depth {
			stack = stack[:len(stack)-1]
		}
		parent := -1
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		nodes[i] = RawNode{
			Handle: &handle{i}, Parent: parent, Depth: s.depth, Role: s.role, Name: s.name,
			Desc: s.desc, Value: s.value, States: s.states, Bounds: s.bounds, URL: s.url, Keys: s.keys,
		}
		stack = append(stack, i)
	}
	return nodes
}

type handle struct{ n int }

func str(s string) *string { return &s }

func box(x, y, w, h int) *[4]int { return &[4]int{x, y, w, h} }

// snap walks a tree in a fresh registry.
func snap(t *testing.T, w RawWindow, nodes []RawNode) Snapshot {
	t.Helper()
	walk := NewRegistry(0).Begin()
	defer walk.End()
	return walk.Snapshot(Tree{Window: w, Nodes: nodes})
}

// code is an error's protocol code ("" for nil).
func code(t *testing.T, err error) protocol.ErrorCode {
	t.Helper()
	if err == nil {
		return ""
	}
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a protocol error", err)
	}
	return pe.Code
}

func hint(err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		return pe.Hint
	}
	return ""
}

func ctx() context.Context { return context.Background() }

// obsReq builds observe requests tersely.
type obsReq struct {
	scope, window, find string
	menus, nodes        bool
	text, tail, web     bool
	target              *protocol.Target
}

func (r obsReq) req() protocol.ObserveRequest {
	return protocol.ObserveRequest{
		Scope: r.scope, Window: r.window, Find: r.find, Menus: r.menus, Nodes: r.nodes,
		Text: r.text, Tail: r.tail, Web: r.web, Target: r.target,
	}
}
