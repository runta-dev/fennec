package channel

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// TestMain keeps tests away from the live fennec-channels.service: as root,
// Service used to rewrite /etc/systemd/system with a test's temp HOME and
// restart it, which took the real Discord bot down (FEN-242). Here systemctl
// always fails ("not running"), and units go to a temp dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fennec-units-")
	if err != nil {
		panic(err)
	}
	systemctl = func(ctx context.Context, _ ...string) *exec.Cmd { return exec.CommandContext(ctx, "false") }
	unitDir = func(string) string { return dir }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
