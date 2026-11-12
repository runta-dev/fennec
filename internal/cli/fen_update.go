package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/updater"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func fenUpdateCommand(args []string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}

	if len(args) > 0 {
		switch args[0] {
		case "status":
			var snap updater.Snapshot
			if err := client.Call(ctx, "update.status", map[string]any{"fresh": true}, &snap); err != nil {
				return err
			}
			return json.NewEncoder(output).Encode(snap)
		case "check":
			var snap updater.Snapshot
			if err := client.Call(ctx, "update.check", map[string]any{"download": true}, &snap); err != nil {
				return err
			}
			return json.NewEncoder(output).Encode(snap)
		case "auto":
			if len(args) == 1 {
				var prefs desktopPreferences
				if err := client.Call(ctx, "settings.get", nil, &prefs); err != nil {
					return err
				}
				return json.NewEncoder(output).Encode(map[string]any{"auto_update": !prefs.NoAutoUpdate})
			}
			if len(args) == 2 && (args[1] == "on" || args[1] == "off") {
				enabled := args[1] == "on"
				var prefs desktopPreferences
				if err := client.Call(ctx, "settings.set", map[string]any{"auto_update": enabled}, &prefs); err != nil {
					return err
				}
				return json.NewEncoder(output).Encode(map[string]any{"auto_update": !prefs.NoAutoUpdate})
			}
			return fmt.Errorf("usage: fen update [status | check | auto [on|off]]")
		case "--json":
			args = args[1:]
			if len(args) > 0 {
				return fmt.Errorf("usage: fen update [status | check | auto [on|off]]")
			}
		default:
			return fmt.Errorf("usage: fen update [status | check | auto [on|off]]")
		}
	}

	var snap updater.Snapshot
	if err := client.Call(ctx, "update.check", map[string]any{"download": true}, &snap); err != nil {
		return err
	}
	switch strings.TrimSpace(snap.State) {
	case "latest", "source":
		return json.NewEncoder(output).Encode(snap)
	case "available":
		return fmt.Errorf("update %s is available at %s (%s)", snap.Latest, snap.URL, snap.Stuck)
	case "error":
		if snap.Error != "" {
			return fmt.Errorf("%s", snap.Error)
		}
		return fmt.Errorf("update check failed")
	}
	if snap.State == "ready" || snap.State == "found" {
		if err := client.Call(ctx, "update.install", map[string]any{}, &snap); err != nil {
			return err
		}
		if !isTerminal(os.Stdin) {
			time.Sleep(50 * time.Millisecond)
		}
		return json.NewEncoder(output).Encode(snap)
	}
	return json.NewEncoder(output).Encode(snap)
}
