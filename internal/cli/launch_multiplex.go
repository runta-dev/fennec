package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/launcher"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	runtime "fennec/internal/runtime"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

func launchMultiplex(ctx context.Context, api *runta.Client, directory string, request sessionRequest, output io.Writer) error {
	script, err := runtime.CloudLaunchScript(request.SessionID, request.Agent, request.LocalDirectory, request.Device, request.Arguments)
	if err != nil {
		return err
	}
	if _, err := api.PrepareOwnedSSH(ctx, request.RuntimeID, directory); err != nil {
		return err
	}
	args, err := launcher.LaunchSSHArgs(directory, request.RuntimeID, request.SessionID)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	command.Stdin = strings.NewReader(script)
	command.WaitDelay = time.Second
	finished := launchprofile.Begin("ssh.launch_and_prepare")
	raw, err := command.Output()
	finished()
	if err != nil {
		return fmt.Errorf("session launch failed or outcome unknown; inspect session %s before retry", request.SessionID)
	}
	var state map[string]any
	if len(raw) > 2<<20 || json.Unmarshal(raw, &state) != nil {
		return fmt.Errorf("invalid session state")
	}
	if timings, ok := state["fennec_profile_ms"].(map[string]any); ok {
		for _, stage := range []string{"session_lookup", "launch_validation", "model_config", "gateway_catalog", "herdr_spawn"} {
			if value, ok := timings[stage].(float64); ok {
				elapsed := time.Duration(value * float64(time.Millisecond))
				launchprofile.Record("guest."+stage, time.Now().Add(-elapsed), elapsed)
			}
		}
	}
	delete(state, "fennec_profile_ms")
	state["id"], state["agent"] = request.SessionID, request.Agent
	return json.NewEncoder(output).Encode(state)
}
