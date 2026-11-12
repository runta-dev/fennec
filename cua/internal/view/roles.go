package view

import (
	"strconv"
	"strings"
)

// axRoles maps macOS AX roles to normalized role names. Subroles and role
// descriptions refine some of them in NormalizeRole.
var axRoles = map[string]string{
	"AXApplication":         "application",
	"AXBrowser":             "browser",
	"AXBusyIndicator":       "progressbar",
	"AXButton":              "button",
	"AXCell":                "cell",
	"AXCheckBox":            "checkbox",
	"AXColorWell":           "button",
	"AXColumn":              "column",
	"AXComboBox":            "combobox",
	"AXDateField":           "textbox",
	"AXDisclosureTriangle":  "button",
	"AXDockItem":            "button",
	"AXDrawer":              "group",
	"AXGrid":                "grid",
	"AXGroup":               "group",
	"AXGrowArea":            "generic",
	"AXHeading":             "heading",
	"AXHelpTag":             "tooltip",
	"AXImage":               "image",
	"AXIncrementor":         "spinbutton",
	"AXLayoutArea":          "group",
	"AXLayoutItem":          "listitem",
	"AXLevelIndicator":      "progressbar",
	"AXLink":                "link",
	"AXList":                "list",
	"AXMenu":                "menu",
	"AXMenuBar":             "menubar",
	"AXMenuBarItem":         "menubaritem",
	"AXMenuButton":          "button",
	"AXMenuItem":            "menuitem",
	"AXOutline":             "tree",
	"AXPopUpButton":         "combobox",
	"AXProgressIndicator":   "progressbar",
	"AXRadioButton":         "radio",
	"AXRadioGroup":          "radiogroup",
	"AXRelevanceIndicator":  "progressbar",
	"AXRow":                 "row",
	"AXScrollArea":          "scrollarea",
	"AXScrollBar":           "scrollbar",
	"AXSearchField":         "textbox",
	"AXSheet":               "sheet",
	"AXSlider":              "slider",
	"AXSplitGroup":          "group",
	"AXSplitter":            "separator",
	"AXStaticText":          "text",
	"AXTabGroup":            "tablist",
	"AXTable":               "table",
	"AXTextArea":            "textbox",
	"AXTextField":           "textbox",
	"AXTimeField":           "textbox",
	"AXToolbar":             "toolbar",
	"AXValueIndicator":      "valueindicator",
	"AXWebArea":             "document",
	"AXWindow":              "window",
	"AXDocument":            "document",
	"AXPage":                "document",
	"AXTextMarker":          "text",
	"AXMatte":               "generic",
	"AXRuler":               "ruler",
	"AXRulerMarker":         "ruler",
	"AXSystemWide":          "application",
	"AXUnknown":             "generic",
	"AXLevelIndicatorRole":  "progressbar",
	"AXHandle":              "separator",
	"AXPopover":             "dialog",
	"AXDescriptionList":     "list",
	"AXDescriptionTerm":     "listitem",
	"AXDescriptionListItem": "listitem",
}

// axSubroles refine a role whatever the AX role is (mostly web content,
// where the ARIA role shows up as a subrole).
var axSubroles = map[string]string{
	"AXSwitch":                 "switch",
	"AXTabButton":              "tab",
	"AXOutlineRow":             "treeitem",
	"AXSecureTextField":        "textbox",
	"AXSearchField":            "textbox",
	"AXApplicationDialog":      "dialog",
	"AXApplicationAlert":       "alert",
	"AXApplicationAlertDialog": "dialog",
	"AXApplicationStatus":      "status",
	"AXApplicationLog":         "log",
	"AXApplicationTimer":       "timer",
	"AXApplicationMarquee":     "marquee",
	"AXTabPanel":               "tabpanel",
	"AXDocumentArticle":        "article",
	"AXDocumentMath":           "math",
	"AXDocumentNote":           "note",
	"AXDocumentRegion":         "region",
	"AXLandmarkBanner":         "banner",
	"AXLandmarkComplementary":  "complementary",
	"AXLandmarkContentInfo":    "contentinfo",
	"AXLandmarkMain":           "main",
	"AXLandmarkNavigation":     "navigation",
	"AXLandmarkRegion":         "region",
	"AXLandmarkSearch":         "search",
	"AXUserInterfaceTooltip":   "tooltip",
	"AXContentList":            "list",
	"AXDefinitionList":         "list",
	"AXDialog":                 "dialog",
	"AXSystemDialog":           "dialog",
	"AXFloatingWindow":         "panel",
	"AXSystemFloatingWindow":   "panel",
}

// roleDescriptions are role descriptions (as web engines expose ARIA roles
// on generic AX groups) that name a role outright.
var roleDescriptions = map[string]string{
	"list item":     "listitem",
	"listitem":      "listitem",
	"tab panel":     "tabpanel",
	"heading":       "heading",
	"dialog":        "dialog",
	"alert":         "alert",
	"menu":          "menu",
	"menu item":     "menuitem",
	"tab":           "tab",
	"tab list":      "tablist",
	"tree item":     "treeitem",
	"tree":          "tree",
	"grid":          "grid",
	"row":           "row",
	"cell":          "cell",
	"grid cell":     "cell",
	"toolbar":       "toolbar",
	"navigation":    "navigation",
	"search":        "search",
	"article":       "article",
	"form":          "form",
	"figure":        "figure",
	"banner":        "banner",
	"status":        "status",
	"progress bar":  "progressbar",
	"switch":        "switch",
	"toggle button": "button",
}

// NormalizeRole maps an AX role, subrole and role description to a
// normalized role name. Unknown roles fall back to the lowercase role without
// its AX prefix.
func NormalizeRole(axRole, subrole, roleDescription string) string {
	// Subrole-specific refinements of one role.
	switch {
	case axRole == "AXCheckBox" && subrole == "AXToggle":
		return "button"
	case axRole == "AXRow" && subrole == "AXOutlineRow":
		return "treeitem"
	case axRole == "AXWindow":
		return WindowRole(subrole, false)
	case axRole == "AXSheet":
		return "sheet"
	}
	if r, ok := axSubroles[subrole]; ok {
		// A web subrole on a container names its ARIA role; on controls only
		// the control-shaped subroles apply.
		switch subrole {
		case "AXSwitch", "AXTabButton", "AXSecureTextField", "AXSearchField":
			if axRole == "AXCheckBox" || axRole == "AXRadioButton" || axRole == "AXTextField" {
				return r
			}
		default:
			return r
		}
	}
	base, known := axRoles[axRole]
	if (!known || base == "group" || base == "generic") && roleDescription != "" {
		if r, ok := roleDescriptions[strings.ToLower(strings.TrimSpace(roleDescription))]; ok {
			return r
		}
	}
	if known {
		return base
	}
	if axRole == "" {
		return "generic"
	}
	return strings.ToLower(strings.TrimPrefix(axRole, "AX"))
}

// WindowRole names a top-level window by its subrole: window, dialog, sheet
// or panel.
func WindowRole(subrole string, isSheet bool) string {
	if isSheet {
		return "sheet"
	}
	switch subrole {
	case "AXDialog", "AXSystemDialog":
		return "dialog"
	case "AXFloatingWindow", "AXSystemFloatingWindow":
		return "panel"
	}
	return "window"
}

// States derives an element's states from its AX facts: focused, selected,
// checked or mixed (check boxes, switches, radios and toggles read their
// value as 1 or 2), pressed (toggle buttons), expanded, disabled and
// protected (secure text fields).
func States(axRole, subrole string, value any, focused, selected, expanded, enabled bool) []string {
	var out []string
	if focused {
		out = append(out, "focused")
	}
	if selected {
		out = append(out, "selected")
	}
	v, numeric := number(value)
	switch {
	case axRole == "AXCheckBox" && subrole == "AXToggle":
		if numeric && v == 1 {
			out = append(out, "pressed")
		}
	case axRole == "AXCheckBox" || axRole == "AXRadioButton" || (axRole == "AXMenuItem" && numeric):
		if numeric && v == 1 {
			out = append(out, "checked")
		} else if numeric && v == 2 {
			out = append(out, "mixed")
		}
	}
	if expanded {
		out = append(out, "expanded")
	}
	if !enabled {
		out = append(out, "disabled")
	}
	if subrole == "AXSecureTextField" {
		out = append(out, "protected")
	}
	return out
}

// number reads a check value: numbers, booleans and numeric strings.
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// HasState says whether a node carries a state.
func (n RawNode) HasState(s string) bool {
	for _, have := range n.States {
		if have == s {
			return true
		}
	}
	return false
}

// menuRoles are the roles of menu entries, which macOS keeps in the tree
// while their menu is closed.
var menuRoles = map[string]bool{"menuitem": true, "menu": true}

// itemRoles are the roles of items of lists, tables and trees.
var itemRoles = map[string]bool{"listitem": true, "row": true, "cell": true, "treeitem": true, "option": true}

// containerRoles hold items.
var containerRoles = map[string]bool{"list": true, "table": true, "tree": true, "grid": true, "listbox": true, "outline": true}

// windowRoles are the roles a window or sheet can have.
var windowRoles = map[string]bool{"window": true, "dialog": true, "sheet": true, "panel": true}

// Showing says whether a node is drawn: a "hidden" or "offscreen" state or an
// empty frame hides it, and a menu entry counts as hidden unless the backend
// marks it "showing" (its menu is open). A node without a frame otherwise
// counts as showing.
func Showing(n RawNode) bool {
	if n.HasState("hidden") || n.HasState("offscreen") {
		return false
	}
	if n.HasState("showing") {
		return true
	}
	if n.Bounds != nil && (n.Bounds[2] <= 0 || n.Bounds[3] <= 0) {
		return false
	}
	return !menuRoles[n.Role]
}
