package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"fennec/internal/cloudstate"
	"fennec/internal/control"
)

type cloudCaller interface {
	Call(context.Context, string, any, any) error
}

func fenCloudCommand(args []string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	return cloudCommand(ctx, control.Client{Socket: socket}, args, os.Stdin, output, isTerminal(os.Stdin))
}
func cloudCommand(ctx context.Context, client cloudCaller, args []string, input io.Reader, output io.Writer, interactive bool) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	if args[0] == "status" && len(args) == 1 {
		var state any
		if err := client.Call(ctx, "cloud.status", nil, &state); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(state)
	}
	if args[0] == "create" && len(args) == 1 {
		var state struct {
			Runtime   string `json:"runtime_id"`
			Request   string `json:"request_id"`
			CanCreate bool   `json:"can_create"`
		}
		if err := client.Call(ctx, "cloud.status", nil, &state); err != nil {
			return err
		}
		if !state.CanCreate || state.Runtime == "" || state.Request == "" {
			return fmt.Errorf("cloud deletion must be confirmed before creating another computer")
		}
		var result any
		if err := client.Call(ctx, "cloud.create", map[string]string{"confirm_runtime": state.Runtime, "request_id": state.Request}, &result); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(result)
	}
	if args[0] != "delete" || (len(args) != 2 && len(args) != 4) {
		return fmt.Errorf("usage: fen cloud status | delete <runtime-id> [--confirm-runtime <runtime-id>] | create")
	}
	id := args[1]
	confirmed := ""
	if len(args) == 4 {
		if args[2] != "--confirm-runtime" || args[3] != id {
			return fmt.Errorf("confirmation must match the cloud computer ID")
		}
		confirmed = args[3]
	}
	if id == "" {
		return fmt.Errorf("cloud computer ID required")
	}
	if confirmed == "" && !interactive {
		return fmt.Errorf("noninteractive deletion requires --confirm-runtime <runtime-id>")
	}
	var plan cloudstate.Plan
	if err := client.Call(ctx, "cloud.delete.preview", map[string]string{"runtime_id": id}, &plan); err != nil {
		return err
	}
	if plan.ID == "" || plan.Target.Runtime != id {
		return fmt.Errorf("cloud deletion preview did not match the requested computer")
	}
	if confirmed == "" {
		if _, err := fmt.Fprintf(output, "Permanently delete cloud computer %s\nAll cloud sessions, files, and data will be deleted\nYour local app and login will remain\nType %s to confirm: ", id, id); err != nil {
			return err
		}
		line, err := bufio.NewReader(input).ReadString('\n')
		if err != nil {
			return fmt.Errorf("deletion canceled")
		}
		if strings.TrimSpace(line) != id {
			return fmt.Errorf("deletion canceled")
		}
	}
	var receipt struct {
		Accepted bool   `json:"accepted"`
		Runtime  string `json:"runtime_id"`
		Request  string `json:"request_id"`
	}
	if err := client.Call(ctx, "cloud.delete", map[string]string{"plan_id": plan.ID, "confirm_runtime": id}, &receipt); err != nil {
		return err
	}
	if !receipt.Accepted || receipt.Runtime != id || receipt.Request == "" {
		return fmt.Errorf("cloud deletion receipt is unconfirmed")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state struct {
			Stage   string `json:"stage"`
			Runtime string `json:"runtime_id"`
			Request string `json:"request_id"`
		}
		if err := client.Call(ctx, "cloud.status", nil, &state); err != nil {
			return err
		}
		if state.Runtime != id || state.Request != receipt.Request {
			return fmt.Errorf("cloud deletion identity changed")
		}
		switch state.Stage {
		case "deleted":
			return json.NewEncoder(output).Encode(state)
		case "deleting":
		default:
			return fmt.Errorf("cloud deletion is not confirmed; inspect fen cloud status")
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("cloud deletion is not confirmed: %w", ctx.Err())
		}
	}
}
