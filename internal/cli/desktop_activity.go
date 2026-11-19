package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/desktopactivity"
)

func installDesktopActivity(home string, c *controlplane.Controller, settings *desktopWindowSettings) {
	c.Additional["desktop.activity.update"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in desktopactivity.Input
		if json.Unmarshal(raw, &in) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid terminal snapshot"}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out := desktopactivity.Update(in)
		for _, a := range out.Alerts {
			if settings.get().NotificationSounds {
				desktopactivity.Play(home, a.Kind)
			}
		}
		return out, nil
	}
}
