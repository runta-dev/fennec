package ax

// Kind holds the AX facts that decide an element's ARIA role.
type Kind struct {
	Role    string
	Subrole string
	// Named is set when the element has a title or description.
	Named bool
	// Submenu is set for a menu item that opens a menu.
	Submenu bool
	// Marked is set for a menu item that shows a check mark.
	Marked bool
}

// ARIARole maps an AX role and subrole to an ARIA role after W3C Core-AAM.
// ok is false when ARIA has no match: such an element gets no role. A
// nameless container is "generic" so the outline can fold it away.
func ARIARole(k Kind) (role string, ok bool) {
	switch k.Role {
	case "AXButton", "AXDisclosureTriangle", "AXColorWell", "AXMenuButton":
		return "button", true
	case "AXCheckBox":
		switch k.Subrole {
		case "AXSwitch":
			return "switch", true
		case "AXToggle":
			return "button", true
		}
		return "checkbox", true
	case "AXRadioButton":
		if k.Subrole == "AXTabButton" {
			return "tab", true
		}
		return "radio", true
	case "AXTextField":
		if k.Subrole == "AXSearchField" {
			return "searchbox", true
		}
		return "textbox", true
	case "AXTextArea":
		return "textbox", true
	case "AXStaticText":
		return "text", true
	case "AXPopUpButton", "AXComboBox":
		return "combobox", true
	case "AXMenuBar":
		return "menubar", true
	case "AXMenuBarItem":
		return "menu", true
	case "AXMenuItem":
		switch {
		case k.Submenu:
			return "menu", true
		case k.Marked:
			return "menuitemcheckbox", true
		}
		return "menuitem", true
	case "AXToolbar":
		return "toolbar", true
	case "AXTabGroup":
		return "tablist", true
	case "AXTable":
		return "table", true
	case "AXOutline":
		return "treegrid", true
	case "AXRow":
		if k.Subrole == "AXOutlineRow" {
			return "treeitem", true
		}
		return "row", true
	case "AXCell":
		return "cell", true
	case "AXList":
		return "list", true
	case "AXImage":
		return "img", true
	case "AXLink":
		return "link", true
	case "AXSlider":
		return "slider", true
	case "AXIncrementor":
		return "spinbutton", true
	case "AXProgressIndicator", "AXBusyIndicator", "AXLevelIndicator":
		return "progressbar", true
	case "AXSheet":
		return "dialog", true
	case "AXWindow":
		if k.Subrole == "AXDialog" || k.Subrole == "AXSystemDialog" {
			return "dialog", true
		}
		return "window", true
	case "AXHeading":
		return "heading", true
	case "AXWebArea":
		return "document", true
	case "AXHelpTag":
		return "tooltip", true
	case "AXSplitter":
		return "separator", true
	case "AXScrollBar":
		return "scrollbar", true
	case "AXRadioGroup", "AXGroup", "AXScrollArea", "AXSplitGroup", "AXLayoutArea", "AXMenu":
		if k.Named {
			return "group", true
		}
		return "generic", true
	}
	return "", false
}

// Facts are the AX facts that decide an element's states.
type Facts struct {
	Enabled  bool
	Focused  bool
	Selected bool
	Expanded bool
	Secure   bool
	// Value is the element's numeric AXValue, when it has one.
	Value    int
	HasValue bool
}

// ToggleStates derives the states of an element with ARIA role role.
// Toggle roles (checkbox, switch, radio, menuitemcheckbox) are checkable
// and read value 1 as checked and 2 as mixed; a button reads value 1 as
// pressed (a toggle button). Secure fields are protected and disabled
// elements disabled.
func ToggleStates(role string, f Facts) []string {
	var out []string
	toggle := role == "checkbox" || role == "switch" || role == "radio" || role == "menuitemcheckbox"
	if f.Focused {
		out = append(out, "focused")
	}
	if f.Selected {
		out = append(out, "selected")
	}
	if toggle {
		out = append(out, "checkable")
		if f.HasValue && f.Value == 1 {
			out = append(out, "checked")
		}
		if f.HasValue && f.Value == 2 {
			out = append(out, "mixed")
		}
	}
	if role == "button" && f.HasValue && f.Value == 1 {
		out = append(out, "pressed")
	}
	if f.Expanded {
		out = append(out, "expanded")
	}
	if !f.Enabled {
		out = append(out, "disabled")
	}
	if f.Secure {
		out = append(out, "protected")
	}
	return out
}
