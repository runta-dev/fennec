package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/audit"
	"fennec/internal/control"
	"fennec/internal/sandbox"
	"fmt"
	"io"
	"strconv"
	"time"
)

func fenSandboxCommand(args []string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}

	readSettings := func() (map[string]any, error) {
		var s map[string]any
		if err := client.Call(ctx, "settings.get", nil, &s); err != nil {
			return nil, err
		}
		return s, nil
	}
	project := func(s map[string]any) map[string]any {
		return map[string]any{
			"permission_mode":         s["permission_mode"],
			"sandbox_mode":            s["sandbox_mode"],
			"sandbox_network":         s["sandbox_network"],
			"effective_sandbox_roots": s["effective_sandbox_roots"],
			"seatbelt_available":      sandbox.Available(),
		}
	}

	if len(args) == 0 || (len(args) == 1 && (args[0] == "status" || args[0] == "--json")) {
		s, err := readSettings()
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(project(s))
	}

	switch args[0] {
	case "ask", "auto", "full", "ask-for-approval", "approve-for-me", "full-access":
		if len(args) != 1 {
			return fmt.Errorf("usage: fen sandbox <ask | auto | full>")
		}
		var updated map[string]any
		if err := client.Call(ctx, "settings.set", map[string]any{"permission_mode": sandbox.NormalizePreset(args[0])}, &updated); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(project(updated))
	case "mode":
		if len(args) != 2 || !sandbox.ValidPreset(args[1]) {
			return fmt.Errorf("usage: fen sandbox mode <ask | auto | full>")
		}
		var updated map[string]any
		if err := client.Call(ctx, "settings.set", map[string]any{"permission_mode": sandbox.NormalizePreset(args[1])}, &updated); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(project(updated))
	default:
		return fmt.Errorf("usage: fen sandbox [status | ask | auto | full]")
	}
}

func fenAuditCommand(args []string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}

	filter := "all"
	limit := 50
	clearAll := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--blocked", "blocked":
			filter = "blocked"
		case "--approved", "approved":
			filter = "approved"
		case "--clear", "clear":
			clearAll = true
		case "--json":
		case "--limit":
			if i+1 >= len(args) {
				return fmt.Errorf("usage: fen audit [--blocked | --approved] [--limit <n>] [--clear]")
			}
			i++
			n, convErr := strconv.Atoi(args[i])
			if convErr != nil || n <= 0 {
				return fmt.Errorf("invalid --limit value %q", args[i])
			}
			limit = n
		default:
			return fmt.Errorf("usage: fen audit [--blocked | --approved] [--limit <n>] [--clear]")
		}
	}
	var result audit.QueryResult
	if clearAll {
		if err := client.Call(ctx, "audit.clear", map[string]any{}, &result); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(result)
	}
	if err := client.Call(ctx, "audit.list", map[string]any{"filter": filter, "limit": limit}, &result); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
