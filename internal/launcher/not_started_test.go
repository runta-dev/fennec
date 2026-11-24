package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The broker tells the client whether a failed launch started anything, so
// the client can report a refusal instead of an unknown outcome.
func TestBrokerDistinguishesRefusalFromUnknownOutcome(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fn-not-started-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "ipc")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ServeBroker(ctx, path, func(_ context.Context, intent LaunchIntent) (LaunchReceipt, error) {
		if intent.Agent == "codex" {
			return LaunchReceipt{}, NotStarted(errors.New("this agent runs on this Mac; set its run location to Cloud computer in Fennec first"))
		}
		return LaunchReceipt{}, errors.New("resident channel lost after the launch was sent")
	})
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_, err = RequestLaunch(ctx, path, LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "codex", Directory: "/project"})
	if !IsNotStarted(err) || !strings.Contains(err.Error(), "set its run location") {
		t.Fatalf("refusal not reported as not started: %v", err)
	}
	_, err = RequestLaunch(ctx, path, LaunchIntent{RequestID: strings.Repeat("b", 32), Agent: "claude", Directory: "/project"})
	if err == nil || IsNotStarted(err) || !strings.Contains(err.Error(), "resident channel lost") {
		t.Fatalf("an unknown outcome was reported as not started: %v", err)
	}
	// Nothing is sent when the broker cannot be reached.
	_, err = RequestLaunch(ctx, filepath.Join(dir, "missing"), LaunchIntent{RequestID: strings.Repeat("c", 32), Agent: "codex", Directory: "/project"})
	if !IsNotStarted(err) {
		t.Fatalf("an unreachable broker was not reported as not started: %v", err)
	}
	if NotStarted(nil) != nil {
		t.Fatal("NotStarted(nil) must stay nil")
	}
}
