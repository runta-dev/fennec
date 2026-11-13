package browser

import (
	"fmt"
	"strings"

	"fennec/cua/internal/actions"
)

// pwKeys maps fennec-cua's key names (actions.KeyNames) to Playwright's,
// which the CDP key table takes.
var pwKeys = map[string]string{
	"return": "Enter", "tab": "Tab", "escape": "Escape", "space": "Space", "backspace": "Backspace",
	"forward_delete": "Delete", "home": "Home", "end": "End", "pageup": "PageUp", "pagedown": "PageDown",
	"up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight",
	"shift": "Shift", "ctrl": "Control", "alt": "Alt", "cmd": "Meta", "capslock": "CapsLock",
}

// keypad keys keep their numpad identity in Playwright.
var pwKeypad = map[string]string{
	"kp0": "Numpad0", "kp1": "Numpad1", "kp2": "Numpad2", "kp3": "Numpad3", "kp4": "Numpad4",
	"kp5": "Numpad5", "kp6": "Numpad6", "kp7": "Numpad7", "kp8": "Numpad8", "kp9": "Numpad9",
	"kpadd": "NumpadAdd", "kpsubtract": "NumpadSubtract", "kpmultiply": "NumpadMultiply",
	"kpdivide": "NumpadDivide", "kpdecimal": "NumpadDecimal", "kpenter": "NumpadEnter", "kpequal": "NumpadEqual",
}

func foldKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r == '_' || r == '-' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PlaywrightKeys reads an xdotool-style key or chord ("Return",
// "super+c", "shift+Tab", "KP_0") into Playwright key names, in press
// order. An unknown key is an error before anything is sent.
func PlaywrightKeys(chord string) ([]string, error) {
	if strings.TrimSpace(chord) == "" {
		return nil, fmt.Errorf("pressKey takes a key such as \"Return\" or \"super+c\"")
	}
	parts := strings.Split(chord, "+")
	if n := len(parts); n >= 2 && parts[n-1] == "" && parts[n-2] == "" {
		parts = append(parts[:n-2], "+")
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		raw := strings.TrimSpace(p)
		if raw == "" {
			return nil, fmt.Errorf("bad key combination %q: an empty key", chord)
		}
		if len([]rune(raw)) == 1 {
			out = append(out, raw)
			continue
		}
		if k, ok := pwKeypad[foldKey(raw)]; ok {
			out = append(out, k)
			continue
		}
		names, err := actions.KeyNames(raw)
		if err != nil {
			return nil, err
		}
		k := names[0]
		switch {
		case pwKeys[k] != "":
			out = append(out, pwKeys[k])
		case len(k) >= 2 && k[0] == 'f' && k[1] >= '0' && k[1] <= '9':
			out = append(out, "F"+k[1:])
		case len([]rune(k)) == 1:
			out = append(out, k)
		default:
			return nil, fmt.Errorf("%q in %q has no browser key", raw, chord)
		}
	}
	return out, nil
}
