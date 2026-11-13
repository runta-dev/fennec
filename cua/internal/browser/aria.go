package browser

import (
	"regexp"
	"strconv"
	"strings"
)

// The playwright.domSnapshot() text: the page's accessibility tree as an
// indented YAML list in the shape of Playwright's aria snapshot, which the
// reference's DOM snapshots follow: `- role "name" [attrs]: text`, nested
// children, `- text:` runs, `/url` and `/placeholder` entries.

// ariaRoles renames Chromium roles to ARIA role names.
var ariaRoles = map[string]string{
	"StaticText": "text", "image": "img", "LabelText": "generic", "Section": "generic",
	"Iframe": "iframe", "Canvas": "img", "Pre": "generic", "Abbr": "generic", "Ruby": "generic",
	"DescriptionList": "list", "DescriptionListTerm": "term", "DescriptionListDetail": "definition",
	"Video": "generic", "Audio": "generic", "Figcaption": "generic", "Legend": "generic",
}

var yamlPlain = regexp.MustCompile(`^[A-Za-z_\p{L}][^:#\n"'\[\]{}]*$`)

// yamlText quotes text when YAML would read it as something else.
func yamlText(s string) string {
	t := strings.TrimSpace(s)
	if t != "" && t == s && yamlPlain.MatchString(t) && !strings.HasSuffix(t, ":") {
		switch strings.ToLower(t) {
		case "true", "false", "null", "yes", "no", "on", "off", "~":
		default:
			return t
		}
	}
	return strconv.Quote(s)
}

func ariaName(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(s) + `"`
}

// AriaSnapshot renders the tree.
func AriaSnapshot(t *AXTree) string {
	var b strings.Builder
	var emit func(nodes []*AXNode, depth int)
	// visible lists what shows in place of n: n itself, or its children
	// when n does not show.
	var visible func(n *AXNode) []*AXNode
	visible = func(n *AXNode) []*AXNode {
		if n.Role == "InlineTextBox" || n.Role == "LineBreak" || n.Role == "ListMarker" || n.Role == "MenuListPopup" {
			return nil
		}
		if n.Ignored || n.Role == "none" || n.Role == "presentation" || n.Role == "RootWebArea" || n.Role == "WebArea" {
			var out []*AXNode
			for _, c := range n.Children {
				out = append(out, visible(c)...)
			}
			return out
		}
		if n.Role == "StaticText" && strings.TrimSpace(n.Name) == "" {
			return nil
		}
		if (n.Role == "generic" || n.Role == "LabelText") && n.Name == "" && !n.True("focusable") {
			// An unnamed wrapper of one element gives way to it.
			var kids []*AXNode
			for _, c := range n.Children {
				kids = append(kids, visible(c)...)
			}
			if len(kids) == 1 && kids[0].Role != "StaticText" {
				return kids
			}
		}
		return []*AXNode{n}
	}
	kidsOf := func(n *AXNode) []*AXNode {
		if fieldRoles[n.Role] || n.Role == "combobox" {
			return nil
		}
		var out []*AXNode
		for _, c := range n.Children {
			out = append(out, visible(c)...)
		}
		return out
	}
	pad := func(d int) string { return strings.Repeat("  ", d) }
	emit = func(nodes []*AXNode, depth int) {
		for i := 0; i < len(nodes); i++ {
			n := nodes[i]
			if n.Role == "StaticText" {
				text := n.Name
				for i+1 < len(nodes) && nodes[i+1].Role == "StaticText" {
					i++
					text += nodes[i].Name
				}
				b.WriteString(pad(depth) + "- text: " + yamlText(strings.TrimSpace(text)) + "\n")
				continue
			}
			role := n.Role
			if r, ok := ariaRoles[role]; ok {
				role = r
			}
			line := pad(depth) + "- " + role
			if n.Name != "" {
				line += " " + ariaName(n.Name)
			}
			var attrs []string
			switch c, _ := n.Prop("checked"); c {
			case "true":
				attrs = append(attrs, "[checked]")
			case "mixed":
				attrs = append(attrs, "[checked=mixed]")
			}
			if n.True("disabled") {
				attrs = append(attrs, "[disabled]")
			}
			if n.True("expanded") {
				attrs = append(attrs, "[expanded]")
			}
			if lv, ok := n.Prop("level"); ok && n.Role == "heading" {
				attrs = append(attrs, "[level="+lv+"]")
			}
			if p, _ := n.Prop("pressed"); p == "true" {
				attrs = append(attrs, "[pressed]")
			}
			if n.True("selected") {
				attrs = append(attrs, "[selected]")
			}
			if len(attrs) > 0 {
				line += " " + strings.Join(attrs, " ")
			}
			kids := kidsOf(n)
			var props []string
			if u, _ := n.Prop("url"); u != "" && n.Role == "link" {
				props = append(props, "/url: "+yamlText(u))
			}
			value := ""
			if valueRoles[n.Role] {
				value = n.Value
			}
			if n.NameFrom == "placeholder" || (fieldRoles[n.Role] && n.Name == "") {
				if ph, ok := n.Prop("placeholder"); ok && ph != "" {
					props = append(props, "/placeholder: "+yamlText(ph))
				}
			}
			allText := len(kids) > 0
			text := ""
			for _, k := range kids {
				if k.Role != "StaticText" {
					allText = false
					break
				}
				text += k.Name
			}
			text = strings.TrimSpace(text)
			switch {
			case value != "" && len(props) == 0:
				b.WriteString(line + ": " + yamlText(value) + "\n")
			case allText && len(props) == 0:
				if text == n.Name || text == "" {
					b.WriteString(line + "\n")
				} else {
					b.WriteString(line + ": " + yamlText(text) + "\n")
				}
			case len(kids) == 0 && len(props) == 0 && value == "":
				b.WriteString(line + "\n")
			default:
				b.WriteString(line + ":\n")
				if value != "" {
					b.WriteString(pad(depth+1) + "- text: " + yamlText(value) + "\n")
				}
				if !(allText && text == n.Name) {
					emit(kids, depth+1)
				}
				for _, p := range props {
					b.WriteString(pad(depth+1) + "- " + p + "\n")
				}
			}
		}
	}
	emit(visible(t.Root), 0)
	return strings.TrimRight(b.String(), "\n")
}
