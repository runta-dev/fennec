package cli

import (
	"context"
	"errors"
	"fennec/internal/launcher"
	"os"
	"strings"
	"testing"
)

func TestRunAgentUsesCloudLatest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := home + "/codex-original"
	if err := os.WriteFile(original, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	oldRequest := brokerPlanRequest
	oldReadiness := brokerReadiness
	brokerReadiness = func(context.Context) error { return nil }
	t.Cleanup(func() { brokerReadiness = oldReadiness })
	t.Cleanup(func() { brokerPlanRequest = oldRequest })
	called := false
	brokerPlanRequest = func(_ context.Context, _ string, intent launcher.LaunchIntent) (launcher.LaunchPlan, error) {
		called = true
		if intent.Version != "" {
			t.Fatal("local version pinned cloud launch", intent.Version)
		}
		return launcher.LaunchPlan{}, errors.New("stop after request inspection")
	}
	// The original executable is deliberately unusable; cloud launch does not
	// execute it to obtain a version.
	if err := runAgentOriginal([]string{"codex"}, original); err == nil || !called {
		t.Fatal("cloud latest launch did not reach broker", err)
	}
}

func TestBrokerLaunchDoesNotRetryUnknownOutcome(t *testing.T) {
	intent := launcher.LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "codex", Directory: "/project"}
	calls := 0
	attached := false
	err := runAgentWith(context.Background(), "/private/socket", intent, func(context.Context, string, launcher.LaunchIntent) (launcher.LaunchReceipt, error) {
		calls++
		return launcher.LaunchReceipt{}, errors.New("timeout")
	}, func(launcher.LaunchReceipt) error { attached = true; return nil })
	if err == nil || calls != 1 || attached || !strings.Contains(err.Error(), intent.RequestID) {
		t.Fatal("unknown launch was retried or lost identity")
	}
}
func TestAttachFailureRetainsSessionIdentity(t *testing.T) {
	intent := launcher.LaunchIntent{RequestID: strings.Repeat("b", 32), Agent: "pi", Directory: "/project"}
	err := runAgentWith(context.Background(), "/private/socket", intent, func(context.Context, string, launcher.LaunchIntent) (launcher.LaunchReceipt, error) {
		return launcher.LaunchReceipt{SessionID: intent.RequestID, Agent: "pi", Address: "t1"}, nil
	}, func(launcher.LaunchReceipt) error { return errors.New("terminal unavailable") })
	if err == nil || !strings.Contains(err.Error(), "reconnect instead of relaunching") {
		t.Fatal(err)
	}
}
