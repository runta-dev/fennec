package launcher

import (
	"errors"
	"strings"
	"testing"
)

func TestExecLocalExplainsMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := ExecLocal("codex", nil, "/nonexistent-shims")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("got %v", err)
	}
	for _, want := range []string{"Codex CLI is not installed on this Mac", "https://chatgpt.com/codex/install.sh", "Cloud computer"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("%q missing %q", err, want)
		}
	}
}
