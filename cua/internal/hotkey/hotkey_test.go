package hotkey

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	h, err := Parse(Default)
	if err != nil || h == nil || h.Code != 53 || h.Modifiers != Control|Option|Cmd {
		t.Fatalf("default: %+v %v", h, err)
	}
	ok := map[string]Hotkey{
		"Control + Option + K":     {40, Control | Option},
		"CTRL+ALT+CMD+ESCAPE":      {53, Control | Option | Cmd},
		"shift+command+f12":        {111, Shift | Cmd},
		" opt + shift + page_up ":  {116, Option | Shift},
		"ctrl+esc":                 {53, Control},
		"cmd+Forward Delete":       {117, Cmd},
		"ctrl+alt+shift+cmd+space": {49, Control | Option | Shift | Cmd},
	}
	for spec, want := range ok {
		h, err := Parse(spec)
		if err != nil || h == nil || *h != want {
			t.Errorf("%q: %+v %v, want %+v", spec, h, err, want)
		}
	}
	// Every named key parses to its own code behind a modifier.
	for _, k := range KeyNames() {
		h, err := Parse("ctrl+" + k)
		if err != nil || h == nil || h.Code != Keys[k] || h.Modifiers != Control {
			t.Errorf("ctrl+%s: %+v %v", k, h, err)
		}
	}
	for _, k := range []string{"escape", "return", "tab", "space", "backspace", "up", "down", "left", "right", "home", "end", "pageup", "pagedown", "f1", "f20", "a", "z", "0", "9"} {
		if _, ok := Keys[k]; !ok {
			t.Errorf("key %q missing", k)
		}
	}
	bad := map[string]string{
		"escape":         "needs a modifier",
		"K":              "needs a modifier",
		"ctrl+hyper+k":   `unknown modifier "hyper"`,
		"ctrl+pause":     `unknown key "pause"`,
		"ctrl+":          "no key",
		"ctrl+off":       `unknown key "off"`,
		"off+ctrl+k":     `unknown modifier "off"`,
		"ctrl+alt+k+cmd": `unknown modifier "k"`,
	}
	for spec, want := range bad {
		if h, err := Parse(spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %+v %v, want an error with %q", spec, h, err, want)
		}
	}
	for _, off := range []string{"off", "OFF", " Off ", "o f f"} {
		if h, err := Parse(off); h != nil || err != nil {
			t.Errorf("%q: %+v %v", off, h, err)
		}
	}
}

type fakeRegistrar struct {
	calls  []Hotkey
	err    error
	closed int
}

func (f *fakeRegistrar) Register(h Hotkey, fire func()) (func(), error) {
	f.calls = append(f.calls, h)
	if f.err != nil {
		return nil, f.err
	}
	return func() { f.closed++ }, nil
}

func TestOffRegistersNone(t *testing.T) {
	for _, spec := range []string{"off", " OFF "} {
		reg := &fakeRegistrar{}
		r, err := Start(spec, reg, func() {})
		if err != nil {
			t.Fatal(err)
		}
		if len(reg.calls) != 0 || r.Registered() || r.Message != "" || r.StopLabel() != "Stop" {
			t.Errorf("%q: calls %v, registration %+v", spec, reg.calls, r)
		}
		r.Close()
	}
	// Another chord registers that chord.
	reg := &fakeRegistrar{}
	r, err := Start("ctrl+shift+k", reg, func() {})
	if err != nil || len(reg.calls) != 1 || reg.calls[0] != (Hotkey{40, Control | Shift}) || !r.Registered() {
		t.Fatalf("chord: %v %v %+v", err, reg.calls, r)
	}
	if r.StopLabel() != "Stop (ctrl+shift+k)" {
		t.Errorf("label %q", r.StopLabel())
	}
	r.Close()
	r.Close()
	if reg.closed != 1 {
		t.Errorf("unregistered %d times", reg.closed)
	}
	// A bad setting is a configuration error, and nothing registers.
	reg = &fakeRegistrar{}
	if _, err := Start("hyper+k", reg, func() {}); err == nil || len(reg.calls) != 0 {
		t.Errorf("bad spec: %v %v", err, reg.calls)
	}
}

func TestRegisterFailureReported(t *testing.T) {
	reg := &fakeRegistrar{err: ErrInUse}
	r, err := Start(Default, reg, func() {})
	if err != nil {
		t.Fatalf("a held hotkey failed the start: %v", err)
	}
	if len(reg.calls) != 1 {
		t.Fatalf("calls %v", reg.calls)
	}
	if r.Registered() || r.Hotkey != nil {
		t.Error("the stop control claims a hotkey that did not register")
	}
	if r.StopLabel() != "Stop" {
		t.Errorf("label %q names the hotkey", r.StopLabel())
	}
	if r.Message != Unregistered || !strings.Contains(r.Message, "fennec-cua stop") || !strings.Contains(r.Message, "kill_hotkey") {
		t.Errorf("message %q", r.Message)
	}
	r.Close() // nothing to unregister
}
