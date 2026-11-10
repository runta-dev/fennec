package cli

import (
	"context"
	"fennec/internal/setup"
	"fmt"
	"golang.org/x/crypto/ssh"
	"testing"
	"time"
)

func TestSlowFailedAgentDoesNotDelayReadyAgent(t *testing.T) {
	release := make(chan struct{})
	ready := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- prepareAgentsIndependently(context.Background(), []setup.Agent{{ID: "opencode"}, {ID: "claude"}}, []setup.AgentRoute{{Agent: "opencode"}, {Agent: "claude"}}, func(ctx context.Context, a setup.Agent) error {
			if a.ID == "opencode" {
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				return &ssh.ExitError{}
			}
			return nil
		}, func(ctx context.Context, r setup.AgentRoute) ([]setup.RouteResult, error) {
			return []setup.RouteResult{{Agent: r.Agent, Status: "configured"}}, nil
		}, func(r []setup.RouteResult) { ready <- r[0].Agent + ":" + r[0].Status })
	}()
	select {
	case value := <-ready:
		if value != "claude:configured" {
			t.Error(value)
		}
	case <-time.After(time.Second):
		t.Error("ready agent blocked by other install")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if value := <-ready; value != "opencode:installation_failed" {
		t.Fatal(value)
	}
}

func TestSlowModelDoesNotDelayOtherAgent(t *testing.T) {
	release := make(chan struct{})
	ready := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- prepareAgentsIndependently(context.Background(), []setup.Agent{{ID: "codex"}, {ID: "claude"}}, []setup.AgentRoute{{Agent: "codex"}, {Agent: "claude"}}, func(context.Context, setup.Agent) error { return nil }, func(ctx context.Context, r setup.AgentRoute) ([]setup.RouteResult, error) {
			if r.Agent == "codex" {
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return []setup.RouteResult{{Agent: r.Agent, Status: "configured"}}, nil
		}, func(r []setup.RouteResult) { ready <- r[0].Agent })
	}()
	select {
	case agent := <-ready:
		if agent != "claude" {
			t.Error(agent)
		}
	case <-time.After(time.Second):
		t.Error("slow model blocked ready agent")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationErrorPreservesOtherAgents(t *testing.T) {
	results := map[string]string{}
	err := prepareAgentsIndependently(context.Background(), []setup.Agent{{ID: "codex"}, {ID: "claude"}}, []setup.AgentRoute{{Agent: "codex"}, {Agent: "claude"}}, func(context.Context, setup.Agent) error { return nil }, func(ctx context.Context, r setup.AgentRoute) ([]setup.RouteResult, error) {
		if r.Agent == "codex" {
			return nil, fmt.Errorf("model query failed")
		}
		return []setup.RouteResult{{Agent: r.Agent, Status: "configured"}}, nil
	}, func(rows []setup.RouteResult) {
		for _, r := range rows {
			results[r.Agent] = r.Status
		}
	})
	if err != nil || results["claude"] != "configured" || results["codex"] != "configuration_failed" {
		t.Fatal(results, err)
	}
}

func TestUnsupportedDistributionDoesNotBlockConfiguredPeer(t *testing.T) {
	results := map[string]string{}
	agents := []setup.Agent{{ID: "opencode", Version: "3.0.0"}, {ID: "claude", Version: "2.1.318"}}
	err := prepareAgentsIndependently(context.Background(), agents, []setup.AgentRoute{{Agent: "opencode"}, {Agent: "claude"}}, func(ctx context.Context, a setup.Agent) error {
		_, err := setup.AgentInstallScript(a)
		return err
	}, func(ctx context.Context, r setup.AgentRoute) ([]setup.RouteResult, error) {
		if r.Agent == "opencode" {
			t.Error("unsupported distribution reached model configuration")
		}
		return []setup.RouteResult{{Agent: r.Agent, Status: "configured"}}, nil
	}, func(rows []setup.RouteResult) {
		for _, r := range rows {
			results[r.Agent] = r.Status
		}
	})
	if err != nil || results["claude"] != "configured" || results["opencode"] != "distribution_unsupported" {
		t.Fatal(results, err)
	}
}

func TestCanceledPreparationDoesNotPublishSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	configured, published := false, false
	err := prepareAgentsIndependently(ctx, []setup.Agent{{ID: "claude"}}, []setup.AgentRoute{{Agent: "claude"}}, func(context.Context, setup.Agent) error { cancel(); return nil }, func(context.Context, setup.AgentRoute) ([]setup.RouteResult, error) {
		configured = true
		return nil, nil
	}, func([]setup.RouteResult) { published = true })
	if err != context.Canceled || configured || published {
		t.Fatal("cancellation continued", err, configured, published)
	}
}
