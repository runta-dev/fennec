package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/desktopactivity"
)

func installDesktopActivity(home string, c *controlplane.Controller, settings *desktopWindowSettings) {
	// Agents no longer chime when they finish or wait (FEN-137): the window
	// tells the person about queue threads instead, through desktop.notice.sound.
	c.Additional["desktop.activity.update"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in desktopactivity.Input
		if json.Unmarshal(raw, &in) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid terminal snapshot"}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return desktopactivity.Update(in), nil
	}
	// A queue thread now needs the person, is in review or failed (FEN-137).
	c.Additional["desktop.notice.sound"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &in) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid notice"}
		}
		if settings.get().NotificationSounds {
			kind := "done"
			if in.Status == "you" || in.Status == "failed" {
				kind = "blocked"
			}
			desktopactivity.Play(home, kind)
		}
		return map[string]bool{"ok": true}, nil
	}
}
