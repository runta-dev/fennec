package cli

import (
	"context"
	"fennec/internal/setup"
	"testing"
	"time"
)

func TestRouteRecoveryPreservesPeersAndPublishesRecoveredAgent(t *testing.T) {
	routes := []setup.AgentRoute{{Agent: "codex", Model: "test/model"}, {Agent: "claude", Model: "other/model"}}
	results := []setup.RouteResult{{Agent: "codex", Status: "model_rate_limited"}, {Agent: "claude", Status: "configured"}}
	calls, published := 0, 0
	got, err := recoverRoutes(context.Background(), routes, results, time.Millisecond, func(_ context.Context, route setup.AgentRoute) ([]setup.RouteResult, error) {
		if route.Agent != "codex" {
			t.Fatal("healthy peer reprobed")
		}
		calls++
		status := "model_rate_limited"
		if calls == 2 {
			status = "configured"
		}
		return []setup.RouteResult{{Agent: route.Agent, Model: route.Model, Status: status}}, nil
	}, func(current []setup.RouteResult) {
		published++
		if len(current) != 2 || current[1].Status != "configured" {
			t.Fatal("lost peer state")
		}
	})
	if err != nil || calls != 2 || published != 2 || got[0].Status != "configured" {
		t.Fatal(got, err, calls, published)
	}
	if results[0].Status != "model_rate_limited" {
		t.Fatal("mutated caller snapshot")
	}
}

func TestRouteRecoveryHonorsCancellationAndAuthenticationFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	configure := func(context.Context, setup.AgentRoute) ([]setup.RouteResult, error) { called = true; return nil, nil }
	_, err := recoverRoutes(ctx, nil, []setup.RouteResult{{Agent: "codex", Status: "model_rate_limited"}}, time.Hour, configure, func([]setup.RouteResult) { t.Fatal("published after cancellation") })
	if err != context.Canceled || called {
		t.Fatal(err, called)
	}
	_, err = recoverRoutes(context.Background(), nil, []setup.RouteResult{{Agent: "codex", Status: "model_authentication_required"}}, time.Hour, configure, func([]setup.RouteResult) { t.Fatal("retried authentication failure") })
	if err != nil || called {
		t.Fatal(err, called)
	}
}
