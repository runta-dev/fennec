package cli

import (
	"bytes"
	"context"
	"fennec/internal/launcher"
	"fennec/internal/runta"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// resumeLocalSession copies the conversation a takeover launch resumes from
// the Mac to the cloud computer, so "claude --resume <id>" or "codex resume
// --last" continues it there instead of failing with "No conversation found".
// "Continue latest" becomes the explicit ID found on the Mac. A launch that
// resumes nothing, or a conversation the Mac does not have, is unchanged.
func resumeLocalSession(ctx context.Context, channel *terminalChannel, intent launcher.LaunchIntent) (launcher.LaunchIntent, error) {
	if intent.Agent != "claude" && intent.Agent != "codex" {
		return intent, nil
	}
	home := setup.LocalAgentHome(ctx, intent.Agent)
	bundle, args, err := cloudruntime.CollectLocalSession(home, intent.Agent, intent.Directory, intent.RequestID, intent.Arguments)
	if err != nil {
		return intent, err
	}
	if bundle == nil {
		return intent, nil
	}
	payload, err := cloudruntime.EncodeSessionImport(bundle)
	if err != nil {
		return intent, err
	}
	operation, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if _, err := channel.remote(operation, cloudruntime.SessionImportCommand, payload); err != nil {
		return intent, fmt.Errorf("could not move conversation %s to the cloud computer: %v", bundle.Conversation, err)
	}
	intent.Arguments = args
	return intent, nil
}

// remote runs one command on the cloud computer over the shared SSH
// connection, with stdin from input. Credentials never appear in argv.
func (channel *terminalChannel) remote(ctx context.Context, command string, input []byte) ([]byte, error) {
	control, err := channel.shared.Get(ctx)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(channel.home, ".config/fennec/cloud")
	ssh := exec.CommandContext(ctx, "/usr/bin/ssh", "-T", "-S", control, "-o", "ControlMaster=no", "-o", "ProxyCommand=false", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "LogLevel=ERROR", "-o", "UserKnownHostsFile="+filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), channel.runtimeID+"@"+runta.ActiveSSHHost(channel.home), command)
	ssh.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	ssh.Stderr = &stderr
	output, err := ssh.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, err
	}
	return output, nil
}
