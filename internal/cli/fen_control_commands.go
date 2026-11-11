package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/sessionid"
	"fmt"
	"io"
	"os"
	"time"
)

// User-facing commands map to the exact same methods used by the native UI.
func fenControlCommand(arguments []string, output io.Writer) (bool, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	args := append([]string{}, arguments...)
	if len(args) > 1 && args[len(args)-1] == "--json" && args[0] != "launch" && args[0] != "send" {
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		return false, nil
	}
	method := ""
	params := map[string]any{}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: fen status [--json]")
		}
		method = "state.get"
	case "settings":
		if len(args) == 1 {
			method = "settings.get"
			break
		}
		if len(args) != 3 || (args[1] != "theme" && args[1] != "language" && args[1] != "magpie") {
			return true, fmt.Errorf("usage: fen settings [theme|language <value> | magpie on|off]")
		}
		method = "settings.set"
		params[args[1]] = args[2]
		if args[1] == "magpie" {
			if args[2] != "on" && args[2] != "off" {
				return true, fmt.Errorf("usage: fen settings magpie on|off")
			}
			params[args[1]] = args[2] == "on"
		}
	case "login-item":
		if len(args) != 2 || (args[1] != "status" && args[1] != "on" && args[1] != "off" && args[1] != "settings") {
			return true, fmt.Errorf("usage: fen login-item status|on|off|settings")
		}
		method = "login-item." + args[1]
	case "login":
		if len(args) != 1 && !(len(args) == 2 && args[1] == "--reauth") {
			return true, fmt.Errorf("usage: fen login [--reauth] [--json]")
		}
		method = "account.login"
		if len(args) == 2 {
			params["reauthenticate"] = true
		}
	case "logout":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: fen logout")
		}
		method = "account.logout"
	case "agents":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: fen agents")
		}
		method = "agents.list"
	case "account":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: fen account")
		}
		method = "account.profile"
	case "billing":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: fen billing")
		}
		method = "account.billing"
	case "runtime":
		if len(args) != 2 || (args[1] != "status" && args[1] != "ensure" && args[1] != "retry") {
			return true, fmt.Errorf("usage: fen runtime status|ensure|retry")
		}
		method = "runtime." + args[1]
	case "takeover":
		if len(args) == 1 {
			method = "takeover.get"
			break
		}
		if len(args) != 3 || (args[2] != "on" && args[2] != "off") {
			return true, fmt.Errorf("usage: fen takeover [agent on|off]")
		}
		method = "takeover.set"
		params = map[string]any{"agent": args[1], "enabled": args[2] == "on"}
	case "model":
		if len(args) < 2 || len(args) > 4 {
			return true, fmt.Errorf("usage: fen model <agent> [model|default] [effort]")
		}
		method = "model.get"
		params = map[string]any{"agent": args[1]}
		if len(args) >= 3 {
			method = "model.set"
			model := args[2]
			if model == "default" {
				model = ""
			}
			params["model"] = model
		}
		if len(args) == 4 {
			params["effort"] = args[3]
		}
	case "approvals":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: fen approvals")
		}
		method = "approvals.list"
	case "approve", "deny":
		if len(args) < 2 || len(args) > 3 || len(args) == 3 && (args[0] != "approve" || args[2] != "--always") {
			return true, fmt.Errorf("usage: fen approve <id> [--always] | fen deny <id>")
		}
		method = "approvals.decide"
		params = map[string]any{"id": args[1], "allow": args[0] == "approve", "never_ask": len(args) == 3}
	case "trust":
		if len(args) != 2 || args[1] != "clear" {
			return true, fmt.Errorf("usage: fen trust clear")
		}
		method = "trust.clear"
	case "sessions":
		if len(args) != 1 {
			return true, fmt.Errorf("usage: fen sessions")
		}
		method = "sessions.list"
	case "stop", "read":
		if len(args) != 2 {
			return true, fmt.Errorf("usage: fen %s <session-id>", args[0])
		}
		method = "sessions." + args[0]
		params["session_id"] = args[1]
	case "send":
		if len(args) != 3 {
			return true, fmt.Errorf("usage: fen send <session-id> <text>")
		}
		method = "sessions.send"
		params = map[string]any{"session_id": args[1], "text": args[2]}
	case "launch":
		if len(args) < 2 {
			return true, fmt.Errorf("usage: fen launch <agent> [-- <agent arguments>]")
		}
		directory, err := os.Getwd()
		if err != nil {
			return true, err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return true, err
		}
		id, err := sessionid.Allocate(home)
		if err != nil {
			return true, err
		}
		argv := args[2:]
		if len(argv) > 0 && argv[0] == "--" {
			argv = argv[1:]
		}
		method = "sessions.launch"
		params = map[string]any{"session_id": id, "agent": args[1], "directory": directory, "arguments": argv}
	default:
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return true, err
	}
	if method == "account.login" {
		socket, err := controlSocket()
		if err != nil {
			return true, err
		}
		client := control.Client{Socket: socket}
		var state map[string]any
		if err = client.Call(ctx, method, params, &state); err != nil {
			return true, err
		}
		// Wait only for the device URL, never require a GUI or keep a credential in CLI.
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for state["status"] == "authorizing" && state["verification_url"] == nil {
			select {
			case <-ctx.Done():
				return true, ctx.Err()
			case <-ticker.C:
			}
			if err = client.Call(ctx, "account.status", nil, &state); err != nil {
				return true, err
			}
		}
		return true, json.NewEncoder(output).Encode(state)
	}
	data, err := json.Marshal(params)
	if err != nil {
		return true, err
	}
	err = callControl([]string{method}, bytes.NewReader(data), output)
	if err != nil && method == "sessions.launch" {
		return true, fmt.Errorf("launch %s not confirmed; inspect this request before retrying: %w", params["session_id"], err)
	}
	return true, err
}

const fenControlHelp = `Fennec — one daemon, UI and CLI clients

Usage: fen [attach [session-id]]
	fen start
	fen install [--check] [--replace] [--destination <app-path>]
	fen uninstall --dry-run
	fen uninstall [--yes] [--delete-cloud --confirm-runtime <id>]
  fen daemon start|stop|status
  fen login [--reauth]
  fen status | logout | agents | account | billing
  fen runtime status|ensure|retry
  fen takeover [agent on|off]
  fen model <agent> [model|default] [effort]
  fen sessions | launch <agent> [-- <agent arguments>]
  fen stop <session-id> | read <session-id> | send <session-id> <text>
  fen approvals | approve <id> [--always] | deny <id> | trust clear
  fen control <method>  (JSON parameters on stdin)
  fen settings [theme|language <value> | magpie on|off]
  fen login-item status|on|off|settings

Picker: ↑/↓ j/k select · enter attach · x kill · r refresh · esc quit
Control commands return JSON; --json is accepted explicitly.
Login returns a device authorization URL usable without opening the UI.
Closing the UI does not stop the daemon or remote sessions.
Picker: ↑/↓ j/k select · enter attach · x kill · r refresh · esc quit
`
