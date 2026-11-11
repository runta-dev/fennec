package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

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
	brokerReadiness = func(context.Context, string) error { return nil }
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

func TestCmuxHookPathsAreRewrittenBeforeBrokerDispatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FENNEC_COLD_START", "")
	dir := filepath.Join(home, ".cmux/hooks")
	os.MkdirAll(dir, 0700)
	body := []byte("#!/bin/sh\n# fixture CMUX_SURFACE_ID\nprintf '{}'\n")
	sum := sha256.Sum256(body)
	path := filepath.Join(dir, "cmux-codex-hook-"+hex.EncodeToString(sum[:8])+"-session-start.sh")
	os.WriteFile(path, body, 0700)
	original := filepath.Join(home, "codex-original")
	os.WriteFile(original, []byte("#!/bin/sh\nexit 99\n"), 0700)
	oldRequest, oldReadiness := brokerPlanRequest, brokerReadiness
	t.Cleanup(func() { brokerPlanRequest = oldRequest; brokerReadiness = oldReadiness })
	brokerReadiness = func(context.Context, string) error { return nil }
	called := false
	brokerPlanRequest = func(_ context.Context, _ string, intent launcher.LaunchIntent) (launcher.LaunchPlan, error) {
		called = true
		joined := strings.Join(intent.Arguments, " ")
		if strings.Contains(joined, path) || !strings.Contains(joined, "cmux-hook "+intent.RequestID+" session-start") || !strings.Contains(joined, "model=fixture") {
			t.Fatal("incorrect broker arguments", joined)
		}
		return launcher.LaunchPlan{}, errors.New("fixture: stop before remote launch")
	}
	configuration := `hooks.SessionStart=[{hooks=[{type="command",command='''` + path + `''',timeout=5000}]}]`
	if err := runAgentOriginal([]string{"codex", "--enable", "hooks", "-c", configuration, "-c", "model=fixture"}, original); err == nil || !called {
		t.Fatal("broker not exercised", err)
	}
}
