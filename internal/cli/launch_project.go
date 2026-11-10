package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"fennec/internal/runtime"
	"fennec/internal/syncengine"
)

type launchProjectRequest struct {
	Token          string   `json:"token"`
	RuntimeID      string   `json:"runtime_id"`
	SessionID      string   `json:"session_id"`
	Agent          string   `json:"agent"`
	Directory      string   `json:"directory"`
	Device         string   `json:"device"`
	Arguments      []string `json:"arguments"`
	Version        string   `json:"version,omitempty"`
	CloudWorkspace bool     `json:"cloud_workspace,omitempty"`
}
type syncReceipt struct {
	Mapping syncengine.ProjectMapping `json:"mapping"`
	Sync    syncengine.Session        `json:"sync"`
	Ready   bool                      `json:"ready"`
}

func launchProject(input io.Reader, output io.Writer) error {
	return launchProjectWith(input, output, syncProject, launchSession)
}
func launchProjectWith(input io.Reader, output io.Writer, synchronize, launch func(io.Reader, io.Writer) error) error {
	var request launchProjectRequest
	if err := json.NewDecoder(io.LimitReader(input, 65536)).Decode(&request); err != nil {
		return err
	}
	if request.Token == "" || request.RuntimeID == "" || request.Device == "" {
		return fmt.Errorf("authenticated cloud computer and device required")
	}
	// Validate agent/arguments/session before any project upload.
	if _, err := runtime.LaunchScriptWithVersion(request.SessionID, request.Agent, "/validation", request.Arguments, request.Version); err != nil {
		return err
	}
	if request.CloudWorkspace {
		if err := validateLocalSessionContext(request.Directory, request.Device); err != nil {
			return err
		}
		payload, err := json.Marshal(sessionRequest{Token: request.Token, RuntimeID: request.RuntimeID, SessionID: request.SessionID, Agent: request.Agent, Directory: "/validation", Arguments: request.Arguments, Version: request.Version, CloudWorkspace: true, LocalDirectory: request.Directory, Device: request.Device})
		if err != nil {
			return err
		}
		return launch(bytes.NewReader(payload), output)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	var synced bytes.Buffer
	if err = synchronize(bytes.NewReader(encoded), &synced); err != nil {
		return err
	}
	var receipt syncReceipt
	if json.Unmarshal(synced.Bytes(), &receipt) != nil {
		return fmt.Errorf("invalid project synchronization receipt")
	}
	if !receipt.Ready || !receipt.Sync.Ready() {
		return fmt.Errorf("project synchronization is not ready; agent was not started")
	}
	root, cwd := receipt.Mapping.CloudRoot, receipt.Mapping.CloudDirectory
	if !path.IsAbs(root) || !path.IsAbs(cwd) || path.Clean(root) != root || path.Clean(cwd) != cwd || !(cwd == root || strings.HasPrefix(cwd, root+"/")) {
		return fmt.Errorf("invalid synchronized working directory")
	}
	launchRequest := sessionRequest{Token: request.Token, RuntimeID: request.RuntimeID, SessionID: request.SessionID, Agent: request.Agent, Directory: cwd, Arguments: request.Arguments, Version: request.Version}
	data, err := json.Marshal(launchRequest)
	if err != nil {
		return err
	}
	return launch(bytes.NewReader(data), output)
}
