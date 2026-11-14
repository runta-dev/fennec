package cli

import (
	"context"
	"fennec/internal/agent"
	"fennec/internal/launcher"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"time"
)

// Resolve a native resume request on the Mac, then transfer its active history
// over the same resident channel used to launch the cloud TUI.
func resumeLocalSession(ctx context.Context, channel *terminalChannel, intent launcher.LaunchIntent) (launcher.LaunchIntent, error) {
	home := setup.LocalAgentHome(ctx, intent.Agent)
	executable := ""
	if intent.Agent == "opencode" {
		if entry, err := agent.EntryPath("opencode"); err == nil {
			executable, _ = launcher.OriginalEntry(entry)
		}
	}
	bundle, args, err := cloudruntime.CollectLocalSession(home, intent.Agent, intent.Directory, intent.RequestID, intent.Arguments, executable)
	if err != nil {
		return intent, err
	}
	if bundle == nil {
		return intent, nil
	}
	operation, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	transport, err := channel.get(operation)
	if err != nil {
		return intent, err
	}
	if err := terminalstream.ImportSession(operation, transport, bundle); err != nil {
		return intent, err
	}
	intent.Arguments = args
	return intent, nil
}
