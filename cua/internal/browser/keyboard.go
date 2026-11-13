package browser

import "strings"

// The US keyboard layout for CDP key events, keyed by Playwright key
// names ("Enter", "ArrowLeft", "Meta", "a", "A", "!", "Numpad0").

type keyInfo struct {
	key      string
	code     string
	keyCode  int
	text     string
	location int // 1 left modifier, 3 numpad
}

// CDP modifier bits.
const (
	modAlt   = 1
	modCtrl  = 2
	modMeta  = 4
	modShift = 8
)

var modifierBits = map[string]int{"Alt": modAlt, "Control": modCtrl, "Meta": modMeta, "Shift": modShift}

var namedKeys = map[string]keyInfo{
	"Enter":          {"Enter", "Enter", 13, "\r", 0},
	"Tab":            {"Tab", "Tab", 9, "", 0},
	"Backspace":      {"Backspace", "Backspace", 8, "", 0},
	"Delete":         {"Delete", "Delete", 46, "", 0},
	"Escape":         {"Escape", "Escape", 27, "", 0},
	"Space":          {" ", "Space", 32, " ", 0},
	"Insert":         {"Insert", "Insert", 45, "", 0},
	"Home":           {"Home", "Home", 36, "", 0},
	"End":            {"End", "End", 35, "", 0},
	"PageUp":         {"PageUp", "PageUp", 33, "", 0},
	"PageDown":       {"PageDown", "PageDown", 34, "", 0},
	"ArrowLeft":      {"ArrowLeft", "ArrowLeft", 37, "", 0},
	"ArrowUp":        {"ArrowUp", "ArrowUp", 38, "", 0},
	"ArrowRight":     {"ArrowRight", "ArrowRight", 39, "", 0},
	"ArrowDown":      {"ArrowDown", "ArrowDown", 40, "", 0},
	"Shift":          {"Shift", "ShiftLeft", 16, "", 1},
	"Control":        {"Control", "ControlLeft", 17, "", 1},
	"Alt":            {"Alt", "AltLeft", 18, "", 1},
	"Meta":           {"Meta", "MetaLeft", 91, "", 1},
	"CapsLock":       {"CapsLock", "CapsLock", 20, "", 0},
	"NumpadAdd":      {"+", "NumpadAdd", 107, "+", 3},
	"NumpadSubtract": {"-", "NumpadSubtract", 109, "-", 3},
	"NumpadMultiply": {"*", "NumpadMultiply", 106, "*", 3},
	"NumpadDivide":   {"/", "NumpadDivide", 111, "/", 3},
	"NumpadDecimal":  {".", "NumpadDecimal", 110, ".", 3},
	"NumpadEnter":    {"Enter", "NumpadEnter", 13, "\r", 3},
	"NumpadEqual":    {"=", "NumpadEqual", 187, "=", 3},
}

// punctuation: the unshifted and shifted character of each key.
var punctuation = []struct {
	code    string
	keyCode int
	plain   string
	shifted string
}{
	{"Backquote", 192, "`", "~"}, {"Minus", 189, "-", "_"}, {"Equal", 187, "=", "+"},
	{"BracketLeft", 219, "[", "{"}, {"BracketRight", 221, "]", "}"}, {"Backslash", 220, "\\", "|"},
	{"Semicolon", 186, ";", ":"}, {"Quote", 222, "'", "\""}, {"Comma", 188, ",", "<"},
	{"Period", 190, ".", ">"}, {"Slash", 191, "/", "?"},
}

const digitShifted = ")!@#$%^&*("

var charKeys = func() map[string]keyInfo {
	m := map[string]keyInfo{}
	for c := 'a'; c <= 'z'; c++ {
		code := "Key" + strings.ToUpper(string(c))
		m[string(c)] = keyInfo{string(c), code, int(c - 'a' + 65), string(c), 0}
		m[strings.ToUpper(string(c))] = keyInfo{strings.ToUpper(string(c)), code, int(c - 'a' + 65), strings.ToUpper(string(c)), 0}
	}
	for d := 0; d <= 9; d++ {
		ds := string(rune('0' + d))
		m[ds] = keyInfo{ds, "Digit" + ds, 48 + d, ds, 0}
		sh := string(digitShifted[d])
		m[sh] = keyInfo{sh, "Digit" + ds, 48 + d, sh, 0}
		m["Numpad"+ds] = keyInfo{ds, "Numpad" + ds, 96 + d, ds, 3}
	}
	for _, p := range punctuation {
		m[p.plain] = keyInfo{p.plain, p.code, p.keyCode, p.plain, 0}
		m[p.shifted] = keyInfo{p.shifted, p.code, p.keyCode, p.shifted, 0}
	}
	m[" "] = namedKeys["Space"]
	for i := 1; i <= 24; i++ {
		n := "F" + itoa(i)
		m[n] = keyInfo{n, n, 111 + i, "", 0}
	}
	return m
}()

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// keyDef finds a key by its Playwright name.
func keyDef(name string) (keyInfo, bool) {
	if k, ok := namedKeys[name]; ok {
		return k, true
	}
	k, ok := charKeys[name]
	return k, ok
}

// macCommands are the macOS editing commands Chromium runs for key
// presses (it maps shortcuts to commands through the text input system,
// which synthetic events bypass), keyed by modifiers and key code.
var macCommands = map[string][]string{
	"Backspace":             {"deleteBackward"},
	"Shift+Backspace":       {"deleteBackward"},
	"Alt+Backspace":         {"deleteWordBackward"},
	"Meta+Backspace":        {"deleteToBeginningOfLine"},
	"Delete":                {"deleteForward"},
	"Alt+Delete":            {"deleteWordForward"},
	"Meta+Delete":           {"deleteToEndOfLine"},
	"Enter":                 {"insertNewline"},
	"NumpadEnter":           {"insertNewline"},
	"Tab":                   {"insertTab"},
	"Shift+Tab":             {"insertBacktab"},
	"Escape":                {"cancelOperation"},
	"ArrowLeft":             {"moveLeft"},
	"ArrowRight":            {"moveRight"},
	"ArrowUp":               {"moveUp"},
	"ArrowDown":             {"moveDown"},
	"Shift+ArrowLeft":       {"moveLeftAndModifySelection"},
	"Shift+ArrowRight":      {"moveRightAndModifySelection"},
	"Shift+ArrowUp":         {"moveUpAndModifySelection"},
	"Shift+ArrowDown":       {"moveDownAndModifySelection"},
	"Alt+ArrowLeft":         {"moveWordLeft"},
	"Alt+ArrowRight":        {"moveWordRight"},
	"Alt+Shift+ArrowLeft":   {"moveWordLeftAndModifySelection"},
	"Alt+Shift+ArrowRight":  {"moveWordRightAndModifySelection"},
	"Meta+ArrowLeft":        {"moveToLeftEndOfLine"},
	"Meta+ArrowRight":       {"moveToRightEndOfLine"},
	"Meta+ArrowUp":          {"moveToBeginningOfDocument"},
	"Meta+ArrowDown":        {"moveToEndOfDocument"},
	"Shift+Meta+ArrowLeft":  {"moveToLeftEndOfLineAndModifySelection"},
	"Shift+Meta+ArrowRight": {"moveToRightEndOfLineAndModifySelection"},
	"Shift+Meta+ArrowUp":    {"moveToBeginningOfDocumentAndModifySelection"},
	"Shift+Meta+ArrowDown":  {"moveToEndOfDocumentAndModifySelection"},
	"Home":                  {"scrollToBeginningOfDocument"},
	"End":                   {"scrollToEndOfDocument"},
	"PageUp":                {"scrollPageUp"},
	"PageDown":              {"scrollPageDown"},
	"Meta+KeyA":             {"selectAll"},
	"Meta+KeyC":             {"copy"},
	"Meta+KeyX":             {"cut"},
	"Meta+KeyV":             {"paste"},
	"Meta+KeyZ":             {"undo"},
	"Shift+Meta+KeyZ":       {"redo"},
}

// editingCommands answers the commands for a key press with mods held.
func editingCommands(mods int, code string) []string {
	var b strings.Builder
	if mods&modAlt != 0 {
		b.WriteString("Alt+")
	}
	if mods&modCtrl != 0 {
		b.WriteString("Control+")
	}
	if mods&modShift != 0 {
		b.WriteString("Shift+")
	}
	if mods&modMeta != 0 {
		b.WriteString("Meta+")
	}
	return macCommands[b.String()+code]
}
