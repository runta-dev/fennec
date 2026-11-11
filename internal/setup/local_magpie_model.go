package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func LocalMagpieBinary(home string) string {
	candidates := []string{}
	search := os.Getenv("PATH") + string(os.PathListSeparator) + strings.Join([]string{filepath.Join(home, ".local/bin"), filepath.Join(home, "bin"), "/opt/homebrew/bin", "/usr/local/bin"}, string(os.PathListSeparator))
	for _, dir := range filepath.SplitList(search) {
		if filepath.IsAbs(dir) {
			candidates = append(candidates, filepath.Join(dir, "magpie"))
		}
	}
	roots := []string{"/Applications"}
	if filepath.IsAbs(home) {
		roots = append(roots, filepath.Join(home, "Applications"))
	}
	for _, root := range roots {
		for _, name := range []string{"Magpie.app", "magpie.app"} {
			candidates = append(candidates, filepath.Join(root, name, "Contents/MacOS/magpie"), filepath.Join(root, name, "Contents/MacOS/Magpie"))
		}
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return path
		}
	}
	return ""
}
func localMagpieCommand(ctx context.Context, home, agentID string, args ...string) (string, error) {
	if !map[string]bool{"codex": true, "claude": true, "pi": true}[agentID] {
		return "", fmt.Errorf("unsupported agent")
	}
	binary := LocalMagpieBinary(home)
	if binary == "" {
		return "", fmt.Errorf("Magpie is not installed")
	}
	step, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(step, binary, append([]string{agentID}, args...)...)
	command.WaitDelay = time.Second
	output := &setupOutput{}
	command.Stdout = output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("Magpie could not update this agent")
	}
	return output.String(), nil
}
func LocalMagpieModel(ctx context.Context, home, agentID string) (string, error) {
	output, err := localMagpieCommand(ctx, home, agentID, "model")
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.TrimSpace(output), " model default") {
		return "", nil
	}
	return ParseAgentModel(output)
}
func SetLocalMagpieModel(ctx context.Context, home, agentID, model string) error {
	if _, err := ParseAgentModel("Agent model " + model); err != nil || strings.HasPrefix(model, "-") || strings.ContainsAny(model, " \t\r\n\x00") {
		return fmt.Errorf("choose a valid provider/model")
	}
	if _, err := localMagpieCommand(ctx, home, agentID, "model", model); err != nil {
		return err
	}
	current, err := LocalMagpieModel(ctx, home, agentID)
	if err != nil || current != model {
		return fmt.Errorf("model change was not confirmed")
	}
	return nil
}
