package localssh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/internal/guardian"
	"fennec/internal/sandbox"
)

// lockedBuffer captures broker events written from several goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buf.String() }

func reviewer(t *testing.T, outcome string, unavailable bool) *guardian.Reviewer {
	t.Helper()
	r := &guardian.Reviewer{Home: t.TempDir(), Locate: func(string, string) (string, error) { return "/usr/bin/true", nil }}
	r.Run = func(_ context.Context, _ string, args []string, _ []byte) ([]byte, error) {
		if unavailable {
			return []byte("ERROR: Not logged in. Run codex login"), errors.New("exit status 1")
		}
		for i, arg := range args {
			if arg == "-o" {
				os.WriteFile(args[i+1], []byte(outcome), 0o600)
			}
		}
		return nil, nil
	}
	return r
}

func brokerFor(preset string, r *guardian.Reviewer, out io.Writer) *Broker {
	b := &Broker{pending: map[string]chan Decision{}, output: out, Reviewer: r}
	b.Policy = func() sandbox.Policy { return sandbox.PolicyForPreset(preset, nil) }
	return b
}

func request(command string) approvalRequest {
	return approvalRequest{Display: command, Fingerprint: "SHA256:device", Action: guardian.Action{Kind: "exec", Command: command, Justification: "needed"}}
}

func TestFullAccessNeverAsks(t *testing.T) {
	b := brokerFor(sandbox.PresetFull, nil, io.Discard)
	if got := b.authorize(context.Background(), request("git push")); !got.Allowed {
		t.Fatalf("full access must not ask: %+v", got)
	}
}

func TestApproveForMeUsesAutoReview(t *testing.T) {
	events := &lockedBuffer{}
	b := brokerFor(sandbox.PresetAuto, reviewer(t, `{"outcome":"allow"}`, false), events)
	got := b.authorize(context.Background(), request("git push"))
	if !got.Allowed || got.Decision != decisionAutoApproved || strings.Contains(events.String(), `"approval"`) {
		t.Fatalf("auto-review approval must not prompt the user: %+v %s", got, events.String())
	}
	b = brokerFor(sandbox.PresetAuto, reviewer(t, `{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"Pushes to a protected branch."}`, false), events)
	got = b.authorize(context.Background(), request("git push --force origin main"))
	if got.Allowed || got.Decision != decisionAutoDenied || !strings.Contains(got.Message, "Pushes to a protected branch.") || strings.Contains(events.String(), `"approval"`) {
		t.Fatalf("auto-review denial goes back to the agent without asking the user: %+v", got)
	}
}

func waitForPrompt(t *testing.T, events *lockedBuffer) Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(events.String(), "\n") {
			var event Event
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "approval" {
				return event
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected a user approval prompt")
	return Event{}
}

func TestUserIsAskedForAskModeUnavailableReviewAndTrippedBreaker(t *testing.T) {
	cases := map[string]func(*lockedBuffer) *Broker{
		"ask mode": func(e *lockedBuffer) *Broker {
			return brokerFor(sandbox.PresetAsk, reviewer(t, `{"outcome":"allow"}`, false), e)
		},
		"local Codex unavailable": func(e *lockedBuffer) *Broker {
			return brokerFor(sandbox.PresetAuto, reviewer(t, "", true), e)
		},
		"circuit breaker": func(e *lockedBuffer) *Broker {
			b := brokerFor(sandbox.PresetAuto, reviewer(t, `{"outcome":"deny","rationale":"no"}`, false), e)
			for i := 0; i < 3; i++ {
				if b.authorize(context.Background(), request("x")).Allowed {
					t.Fatal("denied review allowed")
				}
			}
			return b
		},
	}
	for name, build := range cases {
		events := &lockedBuffer{}
		b := build(events)
		done := make(chan approvalOutcome, 1)
		go func() { done <- b.authorize(context.Background(), request("git push")) }()
		prompt := waitForPrompt(t, events)
		if prompt.Reason != "needed" || prompt.Command != "git push" {
			t.Fatalf("%s: prompt must carry the command and justification: %+v", name, prompt)
		}
		b.decide(Decision{ID: prompt.ID, Allow: true})
		if got := <-done; !got.Allowed || got.Decision != "user_approved" {
			t.Fatalf("%s: user approval: %+v", name, got)
		}
		if name == "circuit breaker" && b.breakerTripped("SHA256:device") {
			t.Fatal("a user decision resets the breaker")
		}
	}
}
