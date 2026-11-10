package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"fennec/internal/launcher"
)

// cloud-agent receives a single-use launch envelope on inherited fd 3. stdin
// remains the user's terminal. The native broker must supply a stable request ID.
func cloudAgent(arguments []string) error {
	if len(arguments) < 1 || !launcher.Supported[arguments[0]] {
		return fmt.Errorf("cloud-agent requires a supported agent")
	}
	file := os.NewFile(3, "fennec-launch-context")
	if file == nil {
		return fmt.Errorf("private launch context required on fd 3")
	}
	var request launchProjectRequest
	err := json.NewDecoder(io.LimitReader(file, 65536)).Decode(&request)
	file.Close() // do not pass credentials to SSH or the agent
	if err != nil {
		return fmt.Errorf("invalid private launch context")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	directory, agentArgs, err := launcher.ResolveLaunchDirectory(arguments[0], cwd, arguments[1:])
	if err != nil {
		return err
	}
	request.Agent = arguments[0]
	request.Arguments = agentArgs
	request.Directory = directory
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return cloudAgentWith(request, launchProject, func(runtime, address, session string) error {
		receipt := launcher.LaunchReceipt{RuntimeID: runtime, Address: address, SessionID: session, Agent: request.Agent}
		if err := launcher.SaveReceipt(filepath.Join(home, ".config/fennec/launches"), receipt); err != nil {
			return fmt.Errorf("cloud session exists but receipt could not be saved: %w", err)
		}
		return launcher.Attach(runtime, address, session, filepath.Join(home, ".config/fennec/cloud"))
	})
}
func cloudAgentWith(request launchProjectRequest, launch func(io.Reader, io.Writer) error, attach func(string, string, string) error) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var output bytes.Buffer
	if err = launch(bytes.NewReader(payload), &output); err != nil {
		return err
	}
	var result struct {
		Address string `json:"address"`
		State   string `json:"state"`
	}
	if json.Unmarshal(output.Bytes(), &result) != nil || result.Address == "" || result.State != "live" {
		return fmt.Errorf("launch not confirmed live; inspect existing session before retrying")
	}
	return attach(request.RuntimeID, result.Address, request.SessionID)
}
