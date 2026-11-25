package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"fennec/internal/setup"
)

var desktopSessionName = regexp.MustCompile(`^desktop-[0-9]{1,20}$`)

// desktopLocalFolder makes the working directory for an agent session the
// desktop window starts on this Mac, and returns it. Claude Code never
// remembers trust for the home folder ("Yes lasts for this session only"),
// so a session started there asks "Quick safety check" every time; Fennec
// made this folder, so Claude Code trusts it up front, as in queue sessions.
// Other agents keep working from the home folder ("" means use it).
func desktopLocalFolder(ctx context.Context, home, agent, name string) (string, error) {
	if agent != "claude" {
		return "", nil
	}
	if !desktopSessionName.MatchString(name) {
		return "", fmt.Errorf("invalid session name")
	}
	root := filepath.Join(home, ".fen", "desktop-sessions")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("session folder must be a directory")
	}
	if err := trustClaudeFolders(claudeProfile(home, setup.LocalAgentHome(ctx, agent)), dir); err != nil {
		return "", fmt.Errorf("trust Claude working directory: %w", err)
	}
	return dir, nil
}
