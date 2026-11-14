package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// An agent Magpie does not route (Claude Code on its own "opus") reports
// that model; only an unreadable answer is an error.
func TestLocalMagpieModelReportsNativeModel(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	for _, tc := range []struct {
		answer, want string
		fail         bool
	}{
		{answer: "Claude Code model opus", want: "opus"},
		{answer: "Claude Code model zai/glm-5.3", want: "zai/glm-5.3"},
		{answer: "Claude Code model default", want: ""},
		{answer: "nothing useful", fail: true},
	} {
		script := "#!/bin/sh\necho '" + tc.answer + "'\n"
		if err := os.WriteFile(filepath.Join(bin, "magpie"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		got, err := LocalMagpieModel(context.Background(), home, "claude")
		if tc.fail {
			if err == nil {
				t.Errorf("%q: want an error, got %q", tc.answer, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q, %v; want %q", tc.answer, got, err, tc.want)
		}
	}
}
