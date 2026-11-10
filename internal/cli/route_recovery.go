package cli

import (
	"context"
	"fennec/internal/setup"
	"time"
)

func transientRoute(status string) bool {
	return status == "model_rate_limited" || status == "model_temporarily_unavailable"
}

func recoverRoutes(ctx context.Context, routes []setup.AgentRoute, results []setup.RouteResult, delay time.Duration, configure func(context.Context, setup.AgentRoute) ([]setup.RouteResult, error), publish func([]setup.RouteResult)) ([]setup.RouteResult, error) {
	results = append([]setup.RouteResult(nil), results...)
	for attempt := 0; attempt < 3; attempt++ {
		pending := false
		for _, result := range results {
			pending = pending || transientRoute(result.Status)
		}
		if !pending {
			return results, nil
		}
		timer := time.NewTimer(delay * time.Duration(1<<attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return results, ctx.Err()
		case <-timer.C:
		}
		for index, result := range results {
			if !transientRoute(result.Status) {
				continue
			}
			for _, route := range routes {
				if route.Agent != result.Agent {
					continue
				}
				updated, err := configure(ctx, route)
				if ctx.Err() != nil {
					return results, ctx.Err()
				}
				if err != nil {
					break
				}
				for _, next := range updated {
					if next.Agent == result.Agent {
						results[index] = next
					}
				}
				publish(append([]setup.RouteResult(nil), results...))
				break
			}
		}
	}
	return results, nil
}
