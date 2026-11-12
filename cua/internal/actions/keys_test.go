package actions_test

import (
	"reflect"
	"strings"
	"testing"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/protocol"
)

func keys(t *testing.T, combo string) []string {
	t.Helper()
	k, err := actions.KeyNames(combo)
	if err != nil {
		t.Fatalf("%s: %v", combo, err)
	}
	return k
}

func TestKeyAliases(t *testing.T) {
	for combo, want := range map[string][]string{
		"Ctrl+Shift+S":    {"ctrl", "shift", "s"},
		"control+a":       {"ctrl", "a"},
		"ctl+a":           {"ctrl", "a"},
		"option+esc":      {"alt", "escape"},
		"opt+x":           {"alt", "x"},
		"command+q":       {"cmd", "q"},
		"super+l":         {"cmd", "l"},
		"win+l":           {"cmd", "l"},
		"meta+c":          {"cmd", "c"},
		"ArrowUp":         {"up"},
		"left_arrow":      {"left"},
		"Up-Arrow":        {"up"},
		"PgDown":          {"pagedown"},
		"Page Down":       {"pagedown"},
		"enter":           {"return"},
		"kp_enter":        {"return"},
		"esc":             {"escape"},
		"bksp":            {"backspace"},
		"spacebar":        {"space"},
		"caps_lock":       {"capslock"},
		"ctrl+minus":      {"ctrl", "-"},
		"shift+parenleft": {"shift", "("},
		"alt+F12":         {"alt", "f12"},
		"F01":             {"f1"},
	} {
		if got := keys(t, combo); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", combo, got, want)
		}
	}
}

func TestKeyUnknownRefused(t *testing.T) {
	for _, combo := range []string{"f0", "f13", "ctrl+printscreen", "pause", "ctrl+", "hyper+x", ""} {
		if _, err := actions.KeyNames(combo); err == nil {
			t.Errorf("%q was accepted", combo)
		}
	}
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Key: "ctrl+printscreen"})
	wantStatus(t, r, protocol.StatusRefused)
	wantCode(t, r, protocol.ErrInvalid)
	wantNoInput(t, d)
}

func TestKeyMacOnlyNames(t *testing.T) {
	if got := keys(t, "fn+f1"); !reflect.DeepEqual(got, []string{"fn", "f1"}) {
		t.Fatalf("fn+f1: %v", got)
	}
	if got := keys(t, "menu"); !reflect.DeepEqual(got, []string{"alt"}) {
		t.Fatalf("menu: %v", got)
	}
	for _, combo := range []string{"shift+insert", "numlock", "num_lock", "ins"} {
		_, err := actions.KeyNames(combo)
		if err == nil || !strings.Contains(err.Error(), "not a key on macOS") {
			t.Errorf("%s: %v", combo, err)
		}
	}
}

func TestPlusKeyAsShiftEquals(t *testing.T) {
	for _, combo := range []string{"ctrl++", "ctrl+plus"} {
		if got := keys(t, combo); !reflect.DeepEqual(got, []string{"ctrl", "+"}) {
			t.Fatalf("%s: %v", combo, got)
		}
	}
	for combo, want := range map[string]string{"cmd++": `["cmd","shift","="]`, "cmd+shift++": `["cmd","shift","="]`} {
		d := scene()
		r := newRig(t, d, "").step(protocol.Step{Key: combo})
		wantStatus(t, r, protocol.StatusConfirmed)
		hk := calls(d, "hotkey")
		if len(hk) != 1 || hk[0].Arg("keys") != want {
			t.Fatalf("%s: %s", combo, d)
		}
	}
}

func TestDeleteIsForwardDelete(t *testing.T) {
	for _, combo := range []string{"delete", "del", "Forward_Delete", "fwddelete"} {
		if got := keys(t, combo); !reflect.DeepEqual(got, []string{"forward_delete"}) {
			t.Errorf("%s: %v", combo, got)
		}
	}
	if got := keys(t, "shift+Delete"); !reflect.DeepEqual(got, []string{"shift", "forward_delete"}) {
		t.Errorf("shift+Delete: %v", got)
	}
	if got := keys(t, "backspace"); !reflect.DeepEqual(got, []string{"backspace"}) {
		t.Errorf("backspace: %v", got)
	}
}

func TestKeyNamesToDriver(t *testing.T) {
	d := scene()
	x := newRig(t, d, "")
	for _, combo := range []string{"Enter", "Meta+S", "SUPER+shift+z"} {
		wantStatus(t, x.step(protocol.Step{Key: combo}), protocol.StatusConfirmed)
	}
	if len(d.Calls) != 3 {
		t.Fatal(d)
	}
	if c := d.Calls[0]; c.Tool != "press_key" || c.Arg("key") != "return" {
		t.Errorf("Enter: %s %v", c.Tool, c.Args)
	}
	if c := d.Calls[1]; c.Tool != "hotkey" || c.Arg("keys") != `["cmd","s"]` {
		t.Errorf("Meta+S: %s %v", c.Tool, c.Args)
	}
	if c := d.Calls[2]; c.Arg("keys") != `["cmd","shift","z"]` {
		t.Errorf("SUPER+shift+z: %v", c.Args)
	}
}

func TestPlusKeyByRoute(t *testing.T) {
	fg := scene()
	newRig(t, fg, protocol.InputForeground).step(protocol.Step{Key: "cmd++"})
	if c := calls(fg, "hotkey"); len(c) != 1 || c[0].Arg("keys") != `["cmd","shift","="]` || c[0].Arg("delivery_mode") != "foreground" {
		t.Fatalf("foreground: %s", fg)
	}
	bg := scene()
	newRig(t, bg, protocol.InputBackground).step(protocol.Step{Key: "cmd++", Window: "Notes"})
	if c := calls(bg, "hotkey"); len(c) != 1 || c[0].Arg("keys") != `["cmd","+"]` || c[0].Arg("delivery_mode") != "background" {
		t.Fatalf("background: %s", bg)
	}
}

func TestKeyNameTable(t *testing.T) {
	if got := keys(t, "+"); !reflect.DeepEqual(got, []string{"+"}) {
		t.Fatalf("+: %v", got)
	}
	if got := keys(t, "delete"); !reflect.DeepEqual(got, []string{"forward_delete"}) {
		t.Fatalf("delete: %v", got)
	}
	// An unknown name is never sent as its first letter.
	d := scene()
	r := newRig(t, d, "").step(protocol.Step{Key: "cmd+Qwerty"})
	wantCode(t, r, protocol.ErrInvalid)
	wantNoInput(t, d)
}

func TestVisionKeyAliasesAndCombos(t *testing.T) {
	for combo, want := range map[string][]string{
		"Prior": {"pageup"}, "Next": {"pagedown"}, "Return": {"return"},
		"Control_L+c": {"ctrl", "c"}, "BackSpace": {"backspace"},
	} {
		if got := keys(t, combo); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", combo, got, want)
		}
	}
	d := scene()
	x := newRig(t, d, "")
	x.step(protocol.Step{Key: "ctrl+l"})
	x.step(protocol.Step{Key: "Return"})
	if joined(d.Tools()) != "hotkey,press_key" {
		t.Fatalf("a combination goes as a hotkey and one key as a key: %s", d)
	}
	for _, bad := range []string{"ctrl+", "+ctrl+"} {
		if r := x.step(protocol.Step{Key: bad}); r.Status != protocol.StatusRefused {
			t.Errorf("%q: %+v", bad, r)
		}
	}
}

func TestMacPlusKeyIsShiftEquals(t *testing.T) {
	d := scene()
	newRig(t, d, protocol.InputForeground).step(protocol.Step{Key: "+"})
	if c := d.Calls; len(c) != 1 || c[0].Tool != "hotkey" || c[0].Arg("keys") != `["shift","="]` {
		t.Fatalf("the plus key: %s", d)
	}
}
