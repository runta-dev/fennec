package runta

import (
	"context"
	"fmt"
)

func (client *Client) OwnedRunningRuntime(ctx context.Context, id string) (Runtime, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type identityResult struct {
		user string
		err  error
	}
	type runtimeResult struct {
		runtime Runtime
		err     error
	}
	identity := make(chan identityResult, 1)
	runtime := make(chan runtimeResult, 1)
	go func() { user, err := client.CurrentUser(ctx); identity <- identityResult{user, err} }()
	go func() { value, err := client.Get(ctx, id); runtime <- runtimeResult{value, err} }()
	var user string
	var computer Runtime
	for identity != nil || runtime != nil {
		select {
		case result := <-identity:
			if result.err != nil {
				return Runtime{}, result.err
			}
			user = result.user
			identity = nil
		case result := <-runtime:
			if result.err != nil {
				return Runtime{}, result.err
			}
			computer = result.runtime
			runtime = nil
		case <-ctx.Done():
			return Runtime{}, ctx.Err()
		}
	}
	if computer.Owner != user || computer.Environment["FENNEC_PURPOSE"] != "shared-v1" {
		return Runtime{}, fmt.Errorf("cloud computer ownership mismatch")
	}
	if computer.Status != "running" || computer.Degraded {
		return Runtime{}, fmt.Errorf("cloud computer is not ready")
	}
	return computer, nil
}
