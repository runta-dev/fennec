package cli

import (
	"context"
	"errors"
	"fennec/internal/setup"
	"fmt"
	"golang.org/x/crypto/ssh"
)

// Each agent installs and verifies independently; only publication is serialized.
func prepareAgentsIndependently(ctx context.Context, agents []setup.Agent, routes []setup.AgentRoute, install func(context.Context, setup.Agent) error, configure func(context.Context, setup.AgentRoute) ([]setup.RouteResult, error), publish func([]setup.RouteResult)) error {
	if len(agents) != len(routes) {
		return fmt.Errorf("agent route count mismatch")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type completion struct {
		results []setup.RouteResult
		err     error
	}
	completed := make(chan completion, len(agents))
	slots := make(chan struct{}, 4)
	for i, a := range agents {
		go func(a setup.Agent, route setup.AgentRoute) {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				completed <- completion{err: ctx.Err()}
				return
			}
			defer func() { <-slots }()
			if err := ctx.Err(); err != nil {
				completed <- completion{err: err}
				return
			}
			if err := install(ctx, a); err != nil {
				if errors.Is(err, setup.ErrAgentDistributionUnsupported) {
					completed <- completion{results: []setup.RouteResult{{Agent: route.Agent, Status: "distribution_unsupported"}}}
					return
				}
				if ctx.Err() != nil {
					completed <- completion{err: ctx.Err()}
					return
				}
				var exit *ssh.ExitError
				if !errors.As(err, &exit) {
					completed <- completion{err: err}
					return
				}
				completed <- completion{results: []setup.RouteResult{{Agent: route.Agent, Model: route.Model, Status: "installation_failed"}}}
				return
			}
			if err := ctx.Err(); err != nil {
				completed <- completion{err: err}
				return
			}
			result, err := configure(ctx, route)
			if err != nil && ctx.Err() == nil {
				// A failed route is not evidence that another agent is unusable.
				// Shared cancellation remains fatal, but preserve peer results.
				completed <- completion{results: []setup.RouteResult{{Agent: route.Agent, Model: route.Model, Status: "configuration_failed"}}}
				return
			}
			completed <- completion{results: result, err: err}
		}(a, routes[i])
	}
	for range agents {
		select {
		case done := <-completed:
			if err := ctx.Err(); err != nil {
				return err
			}
			if done.err != nil {
				return done.err
			}
			publish(done.results)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
