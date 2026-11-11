package launcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fennec/internal/terminalstate"
	"fennec/internal/terminalstream"
	"fmt"
	"golang.org/x/term"
	"os"
	"os/exec"
	"path/filepath"
)

// ExecuteColdStream opens one non-PTY SSH session after the broker's ownership
// check. Its first remote command carries the authorized launch frame, removing
// the separate PTY request and ready-then-launch network round trips.
func ExecuteColdStream(ctx context.Context, plan LaunchPlan, intent LaunchIntent, home string, ready func(), connection *ColdConnection) error {
	if connection == nil || !connection.Ready(ctx, plan) || plan.Launch == nil || plan.Launch.ID != intent.RequestID || plan.Launch.Agent != intent.Agent {
		return fmt.Errorf("cold launch identity was not verified")
	}
	if err := plan.Launch.Validate(); err != nil {
		return err
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		saved, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return err
		}
		defer terminalstate.Restore(os.Stdin, os.Stdout, saved)
	}
	facts, early, err := probeDisplay(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		cols, rows = 120, 40
	}
	opening := terminalstream.Frame{Type: "launch", ID: intent.RequestID, Launch: plan.Launch, Rows: rows, Columns: cols, Term: os.Getenv("TERM"), Display: facts}
	payload, err := json.Marshal(opening)
	if err != nil {
		return err
	}
	// The payload contains validated launch arguments, never the daemon API token.
	remote := `"$HOME/.local/fennec/bin/fennec-terminal" serve-initial ` + base64.StdEncoding.EncodeToString(payload)
	directory := filepath.Join(home, ".config/fennec/cloud")
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(lifecycle, "/usr/bin/ssh", "-T", "-S", connection.control, "-o", "ControlMaster=no", "-o", "ProxyCommand=false", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "UserKnownHostsFile="+filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), plan.RuntimeID+"@ssh.runta.com", remote)
	transport, session, err := terminalstream.StartInitial(command, opening)
	if err != nil {
		return err
	}
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		return fmt.Errorf("cold launch outcome unknown; inspect session %s: %w", intent.RequestID, err)
	}
	path, err := terminalstream.OfferLocal(lifecycle, transport, filepath.Join(home, ".config/fennec/ipc"), opening, session)
	if err != nil {
		return err
	}
	plan.StreamPath = path
	return executeStream(plan, intent, home, ready, &preparedDisplay{facts: facts, input: early})
}
