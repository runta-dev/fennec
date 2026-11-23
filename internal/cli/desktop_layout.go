package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/controlplane"
	"fennec/internal/desktoplayout"
)

func installDesktopLayout(home string, c *controlplane.Controller) {
	store := desktoplayout.New(home)
	c.Additional["desktop.layout"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var in desktoplayout.Input
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return store.Apply(in)
	}
}
