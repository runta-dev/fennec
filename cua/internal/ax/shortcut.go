package ax

import "strings"

// Bits of AXMenuItemCmdModifiers. Command is implied unless NoCommand is set.
const (
	ModShift     = 1
	ModOption    = 2
	ModControl   = 4
	ModNoCommand = 8
)

// MenuShortcut renders a menu item's key equivalent in act key's vocabulary
// from its AXMenuItemCmdChar and AXMenuItemCmdModifiers: ctrl, alt and
// shift as set, cmd unless the no-command bit is set, then the key
// lower-cased. Empty characters and function-key private-use characters
// (U+F700 to U+F8FF) give no shortcut.
func MenuShortcut(char string, mods any) string {
	if char == "" {
		return ""
	}
	for _, r := range char {
		if r >= 0xF700 && r <= 0xF8FF {
			return ""
		}
	}
	m := 0
	switch v := mods.(type) {
	case float64:
		m = int(v)
	case int64:
		m = int(v)
	case int:
		m = v
	}
	var parts []string
	if m&ModControl != 0 {
		parts = append(parts, "ctrl")
	}
	if m&ModOption != 0 {
		parts = append(parts, "alt")
	}
	if m&ModShift != 0 {
		parts = append(parts, "shift")
	}
	if m&ModNoCommand == 0 {
		parts = append(parts, "cmd")
	}
	key := strings.ToLower(char)
	if key == " " {
		key = "space"
	}
	return strings.Join(append(parts, key), "+")
}
