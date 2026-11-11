package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"fennec/internal/sessionid"
	"fennec/internal/setup"
	"fmt"
	"io"
	"os"
	"time"
)

func fenAgentLogin(args []string, output io.Writer) error {
	jsonOutput := len(args) > 0 && args[len(args)-1] == "--json"
	if jsonOutput {
		args = args[:len(args)-1]
	}
	if len(args) >= 2 && args[0] == "account" {
		if len(args) > 3 {
			return fmt.Errorf("usage: fen agent account <agent> [local|cloud]")
		}
		location := "local"
		if len(args) == 3 {
			location = args[2]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ensureControl(ctx); err != nil {
			return err
		}
		socket, err := controlSocket()
		if err != nil {
			return err
		}
		var view setup.NativeAccountView
		if err = (control.Client{Socket: socket}).Call(ctx, "agent.account.get", map[string]any{"agent": args[1], "location": location, "refresh": true}, &view); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(view)
	}
	if len(args) < 2 || len(args) > 3 || !map[string]bool{"login": true, "finish": true, "cancel": true}[args[0]] {
		return fmt.Errorf("usage: fen agent login <agent> [session-id] [--json] | fen agent finish|cancel <agent> <session-id>")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	id := ""
	if len(args) == 3 {
		id = args[2]
	} else if args[0] == "login" {
		id, err = sessionid.Allocate(home)
		if err != nil {
			return err
		}
	}
	if err = (setup.NativeLogin{Agent: args[1], ID: id}).Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = ensureControl(ctx); err != nil {
		return err
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}
	params := map[string]string{"agent": args[1], "session_id": id}
	operation := args[0]
	if operation == "login" {
		operation = "start"
	}
	if operation != "start" {
		var state any
		if err = client.Call(ctx, "agent.login."+operation, params, &state); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(state)
	}
	var receipt launcher.LaunchReceipt
	if err = client.Call(ctx, "agent.login.start", params, &receipt); err != nil {
		return fmt.Errorf("login %s was not confirmed: %w", id, err)
	}
	if jsonOutput || !isTerminal(os.Stdin) {
		return json.NewEncoder(output).Encode(receipt)
	}
	attachErr := fenCommand([]string{"attach", receipt.SessionID}, output)
	finish, done := context.WithTimeout(context.Background(), 30*time.Second)
	defer done()
	var state struct {
		SignedIn bool `json:"signed_in"`
	}
	if err = client.Call(finish, "agent.login.finish", params, &state); err != nil {
		return err
	}
	if state.SignedIn {
		return nil
	}
	// Ctrl+C detaches a regular session; leaving this explicit login workflow
	// cancels its pending authorization so a late callback cannot change identity.
	var result any
	if err = client.Call(finish, "agent.login.cancel", params, &result); err != nil {
		return err
	}
	if attachErr != nil {
		return attachErr
	}
	return fmt.Errorf("sign-in was not completed")
}
