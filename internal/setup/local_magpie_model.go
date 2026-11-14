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
	if !map[string]bool{"codex": true, "claude": true, "pi": true, "kimi": true}[agentID] {
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
	if !map[string]bool{"codex": true, "claude": true, "pi": true, "kimi": true}[agentID] {
		return "", fmt.Errorf("unsupported agent")
	}
	if LocalMagpieBinary(home) == "" {
		return "", fmt.Errorf("Magpie is not installed")
	}
	if model, ok := readConfiguredMagpieModel(home, agentID); ok {
		return model, nil
	}
	output, err := localMagpieCommand(ctx, home, agentID, "model")
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.TrimSpace(output), " model default") {
		return "", nil
	}
	model, err := ParseAgentModel(output)
	if err != nil {
		// The agent runs a model of its own (Claude Code's "opus"), not a
		// Magpie route: that is its current model, not a failure to retry.
		if native, ok := nativeAgentModel(output); ok {
			return native, nil
		}
		return "", err
	}
	return model, nil
}

// nativeAgentModel reads a plain model name, such as an alias without a
// provider, from Magpie's "<Agent> model <name>" answer.
func nativeAgentModel(output string) (string, bool) {
	line := strings.TrimSpace(output)
	_, model, found := strings.Cut(line, " model ")
	model = strings.TrimSpace(model)
	if !found || model == "" || model == "default" || len(model) > 128 || strings.ContainsAny(model, " \t\r\n\x00/") {
		return "", false
	}
	return model, true
}
func SetLocalMagpieModel(ctx context.Context, home, agentID, model string) error {
	if _, err := ParseAgentModel("Agent model " + model); err != nil || strings.HasPrefix(model, "-") || strings.ContainsAny(model, " \t\r\n\x00") {
		return fmt.Errorf("choose a valid provider/model")
	}
	if handled, fastErr := writeConfiguredMagpieModel(ctx, home, agentID, model); handled {
		if fastErr != nil {
			return fastErr
		}
		current, err := LocalMagpieModel(ctx, home, agentID)
		if err != nil || current != model {
			return fmt.Errorf("model change was not confirmed")
		}
		return nil
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
