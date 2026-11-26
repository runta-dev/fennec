package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrResumeUnavailable means the earlier session left no conversation to
// resume, e.g. its agent never started a turn (FEN-11).
var ErrResumeUnavailable = errors.New("the previous conversation is not available to resume")

// ResumeArguments resolves a conversation within its original workspace.
// Never use an agent's global latest conversation as a fallback.
func ResumeArguments(agentHome, agent, directory, launchID, executable string) (*SessionImport, []string, error) {
	args := []string{"--continue"}
	if agent == "codex" {
		args = []string{"resume", "--last"}
	}
	bundle, args, err := CollectLocalSession(agentHome, agent, directory, launchID, args, executable)
	if err != nil {
		return nil, nil, err
	}
	if bundle == nil {
		return nil, nil, ErrResumeUnavailable
	}
	return bundle, args, nil
}

func (runtime *LocalRuntime) resumeArguments(request LocalLaunch, executable string) ([]string, string, error) {
	id := request.ResumeFrom
	seen := map[string]bool{}
	for {
		if seen[id] || !sessionIDPattern.MatchString(id) {
			return nil, "", fmt.Errorf("invalid resume history")
		}
		seen[id] = true
		data, err := os.ReadFile(filepath.Join(runtime.Home, ".local/fennec/launches", id+".json"))
		if err != nil {
			return nil, "", err
		}
		var previous LocalLaunch
		if json.Unmarshal(data, &previous) != nil || previous.Agent != request.Agent || previous.Device != request.Device {
			return nil, "", fmt.Errorf("previous session does not match this agent and device")
		}
		if previous.ResumeFrom == "" {
			break
		}
		id = previous.ResumeFrom
	}
	cwd := filepath.Join(runtime.Home, ".fen", id)
	_, args, err := ResumeArguments(cloudAgentHome(runtime.Home, request.Agent), request.Agent, cwd, request.ID, executable)
	if err != nil {
		return nil, "", err
	}
	return append(args, request.Arguments...), cwd, nil
}
