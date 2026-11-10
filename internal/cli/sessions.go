package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/launcher"
	"fennec/internal/launchprofile"
	"fennec/internal/localssh"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fennec/internal/runta"
	runtime "fennec/internal/runtime"
)

type sessionRequest struct {
	Token          string   `json:"token"`
	RuntimeID      string   `json:"runtime_id"`
	SessionID      string   `json:"session_id"`
	Agent          string   `json:"agent"`
	Directory      string   `json:"directory"`
	Address        string   `json:"address"`
	Text           string   `json:"text"`
	Arguments      []string `json:"arguments"`
	Version        string   `json:"version,omitempty"`
	LocalDirectory string   `json:"local_directory,omitempty"`
	Device         string   `json:"device,omitempty"`
	CloudWorkspace bool     `json:"cloud_workspace,omitempty"`
}

func launchSession(input io.Reader, output io.Writer) error {
	return sessionOperation(input, output, "launch")
}
func listSessions(input io.Reader, output io.Writer) error {
	return sessionOperation(input, output, "list")
}
func sessionOperation(input io.Reader, output io.Writer, operation string) error {
	return sessionOperationContext(context.Background(), input, output, operation)
}

func sessionOperationContext(parent context.Context, input io.Reader, output io.Writer, operation string) error {
	defer launchprofile.Begin("session." + operation)()
	var request sessionRequest
	if err := json.NewDecoder(io.LimitReader(input, 65536)).Decode(&request); err != nil {
		return err
	}
	script := runtime.ListScript
	var err error
	if operation == "launch" {
		if request.CloudWorkspace {
			if err := validateLocalSessionContext(request.LocalDirectory, request.Device); err != nil {
				return err
			}
		}
		script, err = runtime.LaunchScriptWithVersion(request.SessionID, request.Agent, request.Directory, request.Arguments, request.Version)
		if err != nil {
			return err
		}
	}
	if operation == "read" {
		script, err = runtime.ReadScript(request.Address, request.SessionID)
		if err != nil {
			return err
		}
	}
	if operation == "send" {
		script, err = runtime.SendScript(request.Address, request.SessionID, request.Text)
		if err != nil {
			return err
		}
	}
	if operation == "stop" {
		script, err = runtime.StopScript(request.Address, request.SessionID)
		if err != nil {
			return err
		}
	}
	if request.Token == "" || request.RuntimeID == "" {
		return fmt.Errorf("authenticated runtime required")
	}
	timeout := 90 * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	api := &runta.Client{Endpoint: "https://api.runta.com", Token: request.Token}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if operation == "launch" && request.CloudWorkspace {
		return launchMultiplex(ctx, api, filepath.Join(home, ".config", "fennec", "cloud"), request, output)
	}
	if _, err := api.OwnedRunningRuntime(ctx, request.RuntimeID); err != nil {
		return err
	}
	client, err := api.Connect(ctx, request.RuntimeID, filepath.Join(home, ".config", "fennec", "cloud"))
	if err != nil {
		return err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(script)
	remoteDone := launchprofile.Begin("remote." + operation)
	raw, err := session.Output("sh -s")
	remoteDone()
	if err != nil {
		return fmt.Errorf("session %s failed or outcome unknown; inspect session %s before retry", operation, request.SessionID)
	}
	var state map[string]any
	if len(raw) > 2<<20 || json.Unmarshal(raw, &state) != nil {
		return fmt.Errorf("invalid session state")
	}
	if operation == "list" {
		if terminals, ok := state["terminals"].([]any); ok {
			for _, value := range terminals {
				terminal, ok := value.(map[string]any)
				if !ok {
					continue
				}
				labels, ok := terminal["labels"].(map[string]any)
				if !ok {
					continue
				}
				id, _ := labels["fennec.session"].(string)
				if title, ok := terminal["title"].(string); ok {
					terminal["title"] = launcher.CleanSessionTitle(title, id)
				}
			}
		}
	}
	if operation == "launch" {
		state["id"] = request.SessionID
		state["agent"] = request.Agent
		if timings, ok := state["fennec_profile_ms"].(map[string]any); ok {
			for _, stage := range []string{"session_lookup", "launch_validation", "model_config", "gateway_catalog", "herdr_spawn"} {
				if value, ok := timings[stage].(float64); ok {
					elapsed := time.Duration(value * float64(time.Millisecond))
					launchprofile.Record("guest."+stage, time.Now().Add(-elapsed), elapsed)
				}
			}
		}
		delete(state, "fennec_profile_ms")
	}
	return json.NewEncoder(output).Encode(state)
}

func validateLocalSessionContext(directory, device string) error {
	if !filepath.IsAbs(directory) || len(directory) > 4096 || strings.ContainsAny(directory, "\r\n\x00") {
		return fmt.Errorf("absolute local working directory required")
	}
	_, err := localssh.DeviceAlias("machine", device)
	return err
}
