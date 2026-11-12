package vm

import "testing"

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
