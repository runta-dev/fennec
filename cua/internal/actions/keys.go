package actions

import (
	"fmt"
	"strconv"
	"strings"

	"fennec/cua/internal/protocol"
)

// The one key-name vocabulary. A key is one character, f1 to f12, or a
// named key; names match ignoring case and any _, - or space inside them.
var namedKeys = []string{
	"return", "tab", "escape", "space", "backspace", "forward_delete", "home", "end",
	"pageup", "pagedown", "up", "down", "left", "right",
	"shift", "ctrl", "alt", "cmd", "capslock", "fn",
}

// keyAliases maps folded names (lower case, no _, - or space) to keys.
var keyAliases = map[string]string{
	// Modifiers.
	"control": "ctrl", "ctl": "ctrl", "controll": "ctrl", "controlr": "ctrl", "ctrlleft": "ctrl", "ctrlright": "ctrl", "primary": "ctrl",
	"shiftl": "shift", "shiftr": "shift", "shiftleft": "shift", "shiftright": "shift",
	"option": "alt", "opt": "alt", "altl": "alt", "altr": "alt", "altleft": "alt", "altright": "alt", "mod1": "alt", "menu": "alt",
	"command": "cmd", "super": "cmd", "superl": "cmd", "superr": "cmd", "meta": "cmd", "win": "cmd", "windows": "cmd", "winleft": "cmd", "winright": "cmd", "mod4": "cmd",
	// Editing and navigation.
	"enter": "return", "kpenter": "return", "numpadenter": "return",
	"esc":    "escape",
	"bksp":   "backspace",
	"delete": "forward_delete", "del": "forward_delete", "forwarddelete": "forward_delete", "fwddelete": "forward_delete", "deleteforward": "forward_delete",
	"spacebar": "space",
	"prior":    "pageup", "pgup": "pageup",
	"next": "pagedown", "pgdn": "pagedown", "pgdown": "pagedown", "pagedn": "pagedown",
	"arrowup": "up", "arrowdown": "down", "arrowleft": "left", "arrowright": "right",
	"uparrow": "up", "downarrow": "down", "leftarrow": "left", "rightarrow": "right",
	"capslock": "capslock",
	// Symbols.
	"plus": "+", "minus": "-", "dash": "-", "equal": "=", "equals": "=", "asterisk": "*", "star": "*",
	"slash": "/", "backslash": "\\", "period": ".", "dot": ".", "comma": ",", "semicolon": ";", "colon": ":",
	"underscore": "_", "parenleft": "(", "parenright": ")",
	// Keypad names of the xdotool vocabulary (KP_0, KP_Add, ...).
	"kp0": "0", "kp1": "1", "kp2": "2", "kp3": "3", "kp4": "4", "kp5": "5", "kp6": "6", "kp7": "7", "kp8": "8", "kp9": "9",
	"kpadd": "+", "kpsubtract": "-", "kpmultiply": "*", "kpdivide": "/", "kpdecimal": ".", "kpequal": "=",
	"kpup": "up", "kpdown": "down", "kpleft": "left", "kpright": "right", "kphome": "home", "kpend": "end",
	"kpprior": "pageup", "kpnext": "pagedown", "kppageup": "pageup", "kppagedown": "pagedown", "kpdelete": "forward_delete", "kpspace": "space", "kptab": "tab",
}

// notMac are names of keys a Mac keyboard does not have.
var notMac = map[string]bool{"insert": true, "ins": true, "numlock": true}

func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r == '_' || r == '-' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// KeyNames reads a combination such as "ctrl+shift+s" into the driver's
// macOS key names. ctrl++ and ctrl+plus both name the plus key. An unknown
// name, a key a Mac does not have and an empty part are refused, before
// anything is sent.
func KeyNames(combo string) ([]string, error) {
	parts := strings.Split(combo, "+")
	// A trailing "++" is the plus key.
	if n := len(parts); n >= 2 && parts[n-1] == "" && parts[n-2] == "" {
		parts = append(parts[:n-2], "+")
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		k, err := keyName(p, combo)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func keyName(part, combo string) (string, error) {
	raw := strings.TrimSpace(part)
	if raw == "" {
		return "", fmt.Errorf("bad key combination %q: an empty key", combo)
	}
	if raw == "+" {
		return "+", nil
	}
	if len([]rune(raw)) == 1 {
		return strings.ToLower(raw), nil
	}
	f := fold(raw)
	if notMac[f] {
		return "", fmt.Errorf("%q in %q is not a key on macOS", raw, combo)
	}
	if k, ok := keyAliases[f]; ok {
		return k, nil
	}
	if strings.HasPrefix(f, "f") {
		if n, err := strconv.Atoi(f[1:]); err == nil {
			if n >= 1 && n <= 12 {
				return "f" + strconv.Itoa(n), nil
			}
			return "", fmt.Errorf("unknown key %q in %q: the function keys are f1 to f12", raw, combo)
		}
	}
	for _, k := range namedKeys {
		if fold(k) == f {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown key %q in %q: a key is one character, f1 to f12, or one of %s", raw, combo, strings.Join(namedKeys, ", "))
}

var modifierKeys = map[string]bool{"shift": true, "ctrl": true, "alt": true, "cmd": true, "fn": true}

// keyCall is the driver call for a combination on a route: one key goes as
// press_key, a combination as hotkey. The plus key goes as shift+= on the
// foreground (HID) route and as a literal + in the background.
func keyCall(keys []string, foreground bool) (tool string, args map[string]any) {
	keys = append([]string(nil), keys...)
	if foreground {
		for i, k := range keys {
			if k != "+" {
				continue
			}
			keys[i] = "="
			has := false
			for _, m := range keys {
				has = has || m == "shift"
			}
			if !has {
				keys = append(keys[:i], append([]string{"shift"}, keys[i:]...)...)
			}
			break
		}
	}
	if len(keys) == 1 {
		return "press_key", map[string]any{"key": keys[0]}
	}
	// A combination whose only non-modifier is its last key may go as a
	// key with modifiers; hotkey takes them in order either way.
	return "hotkey", map[string]any{"keys": keys}
}

func keyError(err error) *protocol.Error {
	return &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error() + "; nothing was sent", Hint: "keys are joined by +, such as cmd+s, ctrl+shift+tab or return"}
}
