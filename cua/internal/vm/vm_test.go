package vm

import (
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       "'plain'",
		"it's":        `'it'\''s'`,
		"a b; rm -rf": "'a b; rm -rf'",
	} {
		if got := Quote(in); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

func TestInGUIRunsAsTheConsoleUser(t *testing.T) {
	got := InGUI("echo 'hi'")
	want := `sudo launchctl asuser "$(id -u)" sudo -u "$(id -un)" /bin/sh -c 'echo '\''hi'\'''`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestExecCommandAttachesStdinWhenInteractive(t *testing.T) {
	v := &VM{Name: "trial-1", Tart: "/opt/tart"}
	bin, args := v.ExecCommand(true, "echo hi")
	got := bin + " " + strings.Join(args, " ")
	if want := "/opt/tart exec -i trial-1 /bin/sh -c echo hi"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	_, args = v.ExecCommand(false, "true")
	if args[1] != "trial-1" {
		t.Errorf("non-interactive exec has extra flags: %v", args)
	}
}
