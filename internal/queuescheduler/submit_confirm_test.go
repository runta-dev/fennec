package queuescheduler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"fennec/internal/board"
)

// fakeAgent models a herdr-core terminal whose agent swallows the first
// `drops` Enters after a paste, as Claude Code does while starting up.
type fakeAgent struct {
	status        string // "" means the agent has not reported a status yet
	reportAfter   int    // terminal.get calls before a status appears
	turn          uint64
	drops         int
	pending, sent bool
	presses, gets int
}

func (a *fakeAgent) query(_ context.Context, method string, params any) (json.RawMessage, error) {
	switch method {
	case "terminal.send":
		a.pending = true
		turn := a.turn
		if a.drops == 0 {
			a.submit()
		} else {
			a.drops--
		}
		out := map[string]any{"turn": turn}
		if a.status != "" {
			out["started"] = a.turn > turn
		}
		return json.Marshal(out)
	case "terminal.keys":
		if keys := params.(map[string]any)["keys"].([]string); len(keys) != 1 || keys[0] != "enter" {
			return nil, errors.New("unexpected keys")
		}
		a.presses++
		if a.drops > 0 {
			a.drops--
		} else if a.pending {
			a.submit()
		}
		return json.RawMessage(`{"turn":0}`), nil
	case "terminal.get":
		a.gets++
		if a.status == "" && a.gets > a.reportAfter {
			a.status = "idle"
		}
		out := map[string]any{"state": "live", "turn": a.turn}
		if a.status != "" {
			out["status"] = map[string]any{"state": a.status}
		}
		return json.Marshal(out)
	}
	return nil, errors.New("unexpected core operation " + method)
}

func (a *fakeAgent) submit() {
	a.pending, a.sent = false, true
	a.turn++
	a.status = "working"
}

func fastConfirm(t *testing.T) {
	window, poll := confirmWindow, confirmPoll
	confirmWindow, confirmPoll = 300*time.Millisecond, time.Millisecond
	t.Cleanup(func() { confirmWindow, confirmPoll = window, poll })
}

func textRequest(session string) board.Request {
	return board.Request{Session: session, Messages: []board.Message{{ID: 1, Body: "fix it"}}}
}

func TestSwallowedEnterIsPressedAgainUntilTheTurnStarts(t *testing.T) {
	fastConfirm(t)
	store := board.Store{Home: t.TempDir()}
	agent := &fakeAgent{status: "done", turn: 3, drops: 1}
	if err := SendRequest(context.Background(), store, agent.query, "t1", textRequest("11111111111111111111111111111111"), "prompt"); err != nil {
		t.Fatal(err)
	}
	if !agent.sent || agent.presses != 1 {
		t.Fatalf("prompt not submitted once more: sent=%v presses=%d", agent.sent, agent.presses)
	}
}

func TestFreshAgentWithoutStatusIsNotTakenAsRunning(t *testing.T) {
	fastConfirm(t)
	store := board.Store{Home: t.TempDir()}
	// started is null at send time; the agent reports idle a moment later with
	// the prompt still unsent in its input box.
	agent := &fakeAgent{reportAfter: 2, drops: 1}
	if err := SendRequest(context.Background(), store, agent.query, "t1", textRequest("22222222222222222222222222222222"), "prompt"); err != nil {
		t.Fatal(err)
	}
	if !agent.sent {
		t.Fatal("an unsent prompt was reported as started")
	}
}

func TestUnsubmittedPromptStaysUnconfirmed(t *testing.T) {
	fastConfirm(t)
	store := board.Store{Home: t.TempDir()}
	agent := &fakeAgent{status: "idle", drops: 100}
	err := SendRequest(context.Background(), store, agent.query, "t1", textRequest("33333333333333333333333333333333"), "prompt")
	if !errors.Is(err, ErrInputUnconfirmed) {
		t.Fatal("unsubmitted prompt accepted:", err)
	}
	if agent.presses != maxResubmits {
		t.Fatalf("pressed Enter %d times, want %d", agent.presses, maxResubmits)
	}
}

func TestEnterIsNeverPressedIntoADialog(t *testing.T) {
	fastConfirm(t)
	store := board.Store{Home: t.TempDir()}
	agent := &fakeAgent{status: "blocked", drops: 1}
	err := SendRequest(context.Background(), store, agent.query, "t1", textRequest("44444444444444444444444444444444"), "prompt")
	if !errors.Is(err, ErrInputUnconfirmed) || agent.presses != 0 {
		t.Fatalf("pressed Enter into a dialog: err=%v presses=%d", err, agent.presses)
	}
}

func TestAgentWithoutStatusKeepsPreviousBehaviour(t *testing.T) {
	fastConfirm(t)
	store := board.Store{Home: t.TempDir()}
	// It never reports a status, so its turn never moves either.
	agent := &fakeAgent{reportAfter: 1 << 30, drops: 1 << 30}
	if err := SendRequest(context.Background(), store, agent.query, "t1", textRequest("55555555555555555555555555555555"), "prompt"); err != nil {
		t.Fatal("agent without status reporting now fails:", err)
	}
	if agent.presses != 0 {
		t.Fatal("pressed Enter without knowing the agent's state")
	}
}
