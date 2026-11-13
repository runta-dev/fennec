package ego

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The page's accessibility tree as Chromium reports it through
// Accessibility.getFullAXTree, and the DOM facts the state text and the
// actions need (ids, tags, input types).

// axValue is CDP's AXValue.
type axValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

func (v *axValue) str() string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s
	}
	var f float64
	if json.Unmarshal(v.Value, &f) == nil {
		return trimFloat(f)
	}
	var b bool
	if json.Unmarshal(v.Value, &b) == nil {
		if b {
			return "true"
		}
		return "false"
	}
	return strings.Trim(string(v.Value), `"`)
}

func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

type axSource struct {
	Type         string   `json:"type"`
	Attribute    string   `json:"attribute"`
	NativeSource string   `json:"nativeSource"`
	Value        *axValue `json:"value"`
	Superseded   bool     `json:"superseded"`
}

type axName struct {
	axValue
	Sources []axSource `json:"sources"`
}

type axProp struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

type rawAXNode struct {
	NodeID      string   `json:"nodeId"`
	Ignored     bool     `json:"ignored"`
	Role        *axValue `json:"role"`
	Name        *axName  `json:"name"`
	Description *axValue `json:"description"`
	Value       *axValue `json:"value"`
	Properties  []axProp `json:"properties"`
	ParentID    string   `json:"parentId"`
	ChildIDs    []string `json:"childIds"`
	BackendID   int      `json:"backendDOMNodeId"`
	FrameID     string   `json:"frameId"`
}

// AXNode is one node of a page's accessibility tree.
type AXNode struct {
	ID       string
	Parent   *AXNode
	Children []*AXNode
	Ignored  bool
	Role     string // Chromium's role: "button", "StaticText", "RootWebArea"
	Name     string
	// NameFrom is where the name came from: attribute (aria-label, alt,
	// title), relatedElement (a label), contents, placeholder.
	NameFrom string
	NameAttr string // the attribute an attribute name came from
	Desc     string
	Value    string
	Backend  int
	Props    map[string]string
}

// Prop reads a property ("focused", "expanded", "checked", "url", ...).
func (n *AXNode) Prop(name string) (string, bool) {
	v, ok := n.Props[name]
	return v, ok
}

// True says whether a boolean property is set.
func (n *AXNode) True(name string) bool { return n.Props[name] == "true" }

// AXTree is a parsed tree.
type AXTree struct {
	Root  *AXNode
	Nodes []*AXNode // document order
	byID  map[string]*AXNode
}

// ParseAXTree reads Accessibility.getFullAXTree's reply.
func ParseAXTree(data json.RawMessage) (*AXTree, error) {
	var reply struct {
		Nodes []rawAXNode `json:"nodes"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("the accessibility tree does not read: %w", err)
	}
	t := &AXTree{byID: map[string]*AXNode{}}
	raw := map[string]*rawAXNode{}
	for i := range reply.Nodes {
		r := &reply.Nodes[i]
		n := &AXNode{ID: r.NodeID, Ignored: r.Ignored, Backend: r.BackendID, Props: map[string]string{}}
		if r.Role != nil {
			n.Role = r.Role.str()
		}
		if r.Name != nil {
			n.Name = r.Name.str()
			for _, s := range r.Name.Sources {
				if s.Value == nil || s.Superseded {
					continue
				}
				n.NameFrom, n.NameAttr = s.Type, s.Attribute
				if n.NameAttr == "" {
					n.NameAttr = s.NativeSource
				}
				break
			}
			if n.NameFrom == "" && n.Name != "" {
				n.NameFrom = "contents"
			}
		}
		n.Desc = r.Description.str()
		n.Value = r.Value.str()
		for _, p := range r.Properties {
			n.Props[p.Name] = p.Value.str()
		}
		t.byID[n.ID] = n
		raw[n.ID] = r
	}
	for i := range reply.Nodes {
		r := &reply.Nodes[i]
		n := t.byID[r.NodeID]
		if r.ParentID != "" {
			n.Parent = t.byID[r.ParentID]
		}
		for _, c := range r.ChildIDs {
			if cn := t.byID[c]; cn != nil {
				n.Children = append(n.Children, cn)
			}
		}
	}
	for i := range reply.Nodes {
		if n := t.byID[reply.Nodes[i].NodeID]; n.Parent == nil && t.Root == nil {
			t.Root = n
		}
	}
	if t.Root == nil {
		return nil, fmt.Errorf("the page has no accessibility tree yet")
	}
	// Document order: a pre-order walk from the root.
	var walk func(n *AXNode)
	seen := map[*AXNode]bool{}
	walk = func(n *AXNode) {
		if seen[n] {
			return
		}
		seen[n] = true
		t.Nodes = append(t.Nodes, n)
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(t.Root)
	return t, nil
}

// DOMNode is one node of DOM.getDocument's tree.
type DOMNode struct {
	NodeID   int
	Backend  int
	Type     int
	Name     string // localName
	Attrs    map[string]string
	Parent   *DOMNode
	Children []*DOMNode
	Value    string // nodeValue
	Shadow   bool
}

type rawDOMNode struct {
	NodeID       int          `json:"nodeId"`
	BackendID    int          `json:"backendNodeId"`
	NodeType     int          `json:"nodeType"`
	LocalName    string       `json:"localName"`
	NodeValue    string       `json:"nodeValue"`
	Attributes   []string     `json:"attributes"`
	Children     []rawDOMNode `json:"children"`
	ShadowRoots  []rawDOMNode `json:"shadowRoots"`
	ContentDoc   *rawDOMNode  `json:"contentDocument"`
	TemplateCont *rawDOMNode  `json:"templateContent"`
}

// DOM is a parsed document.
type DOM struct {
	Root      *DOMNode
	ByBackend map[int]*DOMNode
	Order     []*DOMNode
}

// ParseDOM reads DOM.getDocument's reply (depth -1, pierce).
func ParseDOM(data json.RawMessage) (*DOM, error) {
	var reply struct {
		Root rawDOMNode `json:"root"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("the document does not read: %w", err)
	}
	d := &DOM{ByBackend: map[int]*DOMNode{}}
	var build func(r *rawDOMNode, parent *DOMNode, shadow bool) *DOMNode
	build = func(r *rawDOMNode, parent *DOMNode, shadow bool) *DOMNode {
		n := &DOMNode{NodeID: r.NodeID, Backend: r.BackendID, Type: r.NodeType, Name: r.LocalName, Value: r.NodeValue, Parent: parent, Attrs: map[string]string{}, Shadow: shadow}
		for i := 0; i+1 < len(r.Attributes); i += 2 {
			n.Attrs[r.Attributes[i]] = r.Attributes[i+1]
		}
		d.ByBackend[n.Backend] = n
		d.Order = append(d.Order, n)
		for i := range r.ShadowRoots {
			n.Children = append(n.Children, build(&r.ShadowRoots[i], n, true))
		}
		for i := range r.Children {
			n.Children = append(n.Children, build(&r.Children[i], n, shadow))
		}
		if r.ContentDoc != nil {
			n.Children = append(n.Children, build(r.ContentDoc, n, false))
		}
		return n
	}
	d.Root = build(&reply.Root, nil, false)
	return d, nil
}

// Contains says whether node b lies in a's subtree (a node contains
// itself).
func (d *DOM) Contains(a, b int) bool {
	n := d.ByBackend[b]
	for ; n != nil; n = n.Parent {
		if n.Backend == a {
			return true
		}
	}
	return false
}

// Element is the element itself, or a text node's parent element.
func (d *DOM) Element(backend int) int {
	n := d.ByBackend[backend]
	for n != nil && n.Type != 1 {
		n = n.Parent
	}
	if n == nil {
		return backend
	}
	return n.Backend
}
