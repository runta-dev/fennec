package launcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fennec/internal/runta"
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
	opening.HookBridge = len(plan.Launch.Hooks) > 0
	payload, err := json.Marshal(opening)
	if err != nil {
		return err
	}
	// The payload contains validated launch arguments, never the daemon API token.
	remote := `"$HOME/.local/fennec/bin/fennec-terminal" serve-initial ` + base64.StdEncoding.EncodeToString(payload)
	if opening.HookBridge {
		remote = fmt.Sprintf(`"$HOME/.local/fennec/bin/fennec-terminal" serve --protocol=%d`, terminalstream.ProtocolVersion)
	}
	directory := filepath.Join(home, ".config/fennec/cloud")
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(lifecycle, "/usr/bin/ssh", "-T", "-S", connection.control, "-o", "ControlMaster=no", "-o", "ProxyCommand=false", "-o", "IPQoS=ef", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "UserKnownHostsFile="+filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), plan.RuntimeID+"@"+runta.ActiveSSHHost(home), remote)
	var transport *terminalstream.Transport
	var session *terminalstream.Session
	if opening.HookBridge {
		transport, err = terminalstream.Start(command)
		if err == nil {
			err = transport.Ready(ctx)
		}
		if err == nil && !transport.Supports("hook_bridge_v1") {
			err = fmt.Errorf("update Fennec to use local cloud hooks")
		}
		if err == nil {
			session, err = transport.Launch(opening)
		}
	} else {
		transport, session, err = terminalstream.StartInitial(command, opening)
	}
	if err != nil {
		if transport != nil {
			transport.Close()
		}
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
