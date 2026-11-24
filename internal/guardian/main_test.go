package guardian

import (
	"os"
	"testing"
)

var inheritedCodexHome = os.Getenv("CODEX_HOME")

func TestMain(m *testing.M) {
	// Unit reviewers must use their fixture Home, never the invoking agent's
	// OAuth grant. The explicitly opted-in live test restores the override.
	os.Unsetenv("CODEX_HOME")
	os.Exit(m.Run())
}
