// Package hotkey reads the kill hotkey setting ("ctrl+alt+cmd+escape") into
// a macOS virtual key code and Carbon modifier mask, and registers it as a
// global hotkey through a Registrar. "off" means no hotkey.
package hotkey

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Default is the kill hotkey of a configuration that names none.
const Default = "ctrl+alt+cmd+escape"

// Off disables the kill hotkey.
const Off = "off"

// Unregistered tells the user how to stop fennec-cua when the hotkey could
// not be registered.
const Unregistered = "The kill hotkey did not register; another app may hold it. Stop fennec-cua from Fennec's menu or with `fennec-cua stop`, or set another kill_hotkey under [safety] in config.toml."

// Carbon modifier masks (Events.h).
const (
	Cmd     uint32 = 1 << 8
	Shift   uint32 = 1 << 9
	Option  uint32 = 1 << 11
	Control uint32 = 1 << 12
)

// Hotkey is a key combination: a macOS virtual key code and a Carbon
// modifier mask.
type Hotkey struct {
	Code      uint32
	Modifiers uint32
}

var modifiers = map[string]uint32{
	"ctrl": Control, "control": Control,
	"alt": Option, "option": Option, "opt": Option,
	"shift": Shift,
	"cmd":   Cmd, "command": Cmd,
}

// Keys maps every key name the hotkey accepts (folded: lower case, no
// spaces, '_' or '-') to its macOS virtual key code (kVK_* in Events.h).
var Keys = map[string]uint32{
	"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9,
	"b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17,
	"1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "=": 24, "9": 25, "7": 26,
	"-": 27, "8": 28, "0": 29, "]": 30, "o": 31, "u": 32, "[": 33, "i": 34, "p": 35,
	"l": 37, "j": 38, "'": 39, "k": 40, ";": 41, "\\": 42, ",": 43, "/": 44,
	"n": 45, "m": 46, ".": 47, "`": 50,
	"return": 36, "enter": 36, "tab": 48, "space": 49, "backspace": 51,
	"escape": 53, "esc": 53,
	"forwarddelete": 117, "delete": 117, "del": 117,
	"home": 115, "end": 119, "pageup": 116, "pagedown": 121, "pgup": 116, "pgdn": 121,
	"left": 123, "right": 124, "down": 125, "up": 126,
	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97, "f7": 98, "f8": 100,
	"f9": 101, "f10": 109, "f11": 103, "f12": 111, "f13": 105, "f14": 107, "f15": 113,
	"f16": 106, "f17": 64, "f18": 79, "f19": 80, "f20": 90,
}

func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '\t', '_':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Parse reads spec: modifiers (ctrl/control, alt/option, shift, cmd/command)
// joined by '+' and then one key, ignoring case and spaces. "off" (in any
// case or spacing) is no hotkey: Parse answers nil and no error.
func Parse(spec string) (*Hotkey, error) {
	if fold(spec) == Off {
		return nil, nil
	}
	parts := strings.Split(spec, "+")
	key := fold(parts[len(parts)-1])
	var mods uint32
	for _, p := range parts[:len(parts)-1] {
		m, ok := modifiers[fold(p)]
		if !ok {
			return nil, fmt.Errorf("kill_hotkey %q: unknown modifier %q", spec, strings.TrimSpace(p))
		}
		mods |= m
	}
	if key == "" {
		return nil, fmt.Errorf("kill_hotkey %q: no key after the modifiers", spec)
	}
	if mods == 0 {
		if _, isMod := modifiers[key]; !isMod {
			return nil, fmt.Errorf("kill_hotkey %q: needs a modifier (ctrl, alt, shift or cmd) besides the key", spec)
		}
	}
	code, ok := Keys[key]
	if !ok {
		return nil, fmt.Errorf("kill_hotkey %q: unknown key %q", spec, strings.TrimSpace(parts[len(parts)-1]))
	}
	return &Hotkey{Code: code, Modifiers: mods}, nil
}

// KeyNames lists the accepted key names, sorted.
func KeyNames() []string {
	out := make([]string, 0, len(Keys))
	for k := range Keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ErrInUse is a registration another app's hotkey blocks.
var ErrInUse = errors.New("the hotkey is held by another app")

// Registrar registers a global hotkey that calls fire when pressed; the
// returned function unregisters it.
type Registrar interface {
	Register(h Hotkey, fire func()) (unregister func(), err error)
}

// Registration is the outcome of Start.
type Registration struct {
	// Hotkey is the registered combination; nil when none is registered
	// (off, or the registration failed).
	Hotkey *Hotkey
	// Spec is the setting as configured.
	Spec string
	// Message, when not empty, is for the user: the hotkey did not register
	// and how to stop fennec-cua instead.
	Message string
	stop    func()
}

// Registered reports whether the hotkey is live.
func (r *Registration) Registered() bool { return r != nil && r.Hotkey != nil }

// StopLabel is the title of the Stop control: it names the hotkey only when
// the hotkey is registered, so a hotkey another app holds is never claimed.
func (r *Registration) StopLabel() string {
	if r.Registered() {
		return "Stop (" + r.Spec + ")"
	}
	return "Stop"
}

// Close unregisters the hotkey; it is safe to call more than once.
func (r *Registration) Close() {
	if r != nil && r.stop != nil {
		r.stop()
		r.stop = nil
	}
}

// Start parses spec and registers it with reg, calling fire on each press.
// A parse error is returned (the configuration is wrong). "off" registers
// nothing. A failed registration is no error: the Registration carries no
// hotkey and the Unregistered message instead.
func Start(spec string, reg Registrar, fire func()) (*Registration, error) {
	h, err := Parse(spec)
	if err != nil {
		return nil, err
	}
	r := &Registration{Spec: spec}
	if h == nil {
		return r, nil
	}
	if reg == nil {
		r.Message = Unregistered
		return r, nil
	}
	stop, err := reg.Register(*h, fire)
	if err != nil {
		r.Message = Unregistered
		return r, nil
	}
	r.Hotkey, r.stop = h, stop
	return r, nil
}
