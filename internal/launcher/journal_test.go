package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLaunchJournalDeduplicatesAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	j := &LaunchJournal{Directory: dir}
	intent := LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "codex", Directory: "/project", Arguments: []string{"private prompt"}}
	var count atomic.Int32
	launch := func(_ context.Context, i LaunchIntent) (LaunchReceipt, error) {
		count.Add(1)
		return LaunchReceipt{SessionID: i.RequestID, Agent: i.Agent, Address: "t7"}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := j.Do(context.Background(), intent, launch)
			if e != nil || r.Address != "t7" {
				t.Error(r, e)
			}
		}()
	}
	wg.Wait()
	restarted := &LaunchJournal{Directory: dir}
	if _, err := restarted.Do(context.Background(), intent, launch); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal("duplicate execution", count.Load())
	}
	data, _ := os.ReadFile(filepath.Join(dir, intent.RequestID+".json"))
	if strings.Contains(string(data), "private prompt") {
		t.Fatal("prompt persisted")
	}
	intent.Arguments = []string{"changed"}
	if _, err := j.Do(context.Background(), intent, launch); err == nil {
		t.Fatal("request reused for different intent")
	}
}

func TestLaunchJournalCanceledWaitDoesNotLaunchOrPersist(t *testing.T) {
	j := &LaunchJournal{Directory: t.TempDir()}
	first := LaunchIntent{RequestID: strings.Repeat("c", 32), Agent: "codex", Directory: "/project"}
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := j.Do(context.Background(), first, func(_ context.Context, i LaunchIntent) (LaunchReceipt, error) {
			close(entered)
			<-release
			return LaunchReceipt{SessionID: i.RequestID, Agent: i.Agent}, nil
		})
		finished <- err
	}()
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := first
	// Duplicate waits must be cancellable without modifying the first record.
	result := make(chan error, 1)
	var called atomic.Bool
	go func() {
		_, err := j.Do(ctx, second, func(context.Context, LaunchIntent) (LaunchReceipt, error) {
			called.Store(true)
			return LaunchReceipt{}, nil
		})
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request waited for active launch")
	}
	if called.Load() {
		t.Fatal("canceled request launched")
	}
	data, err := os.ReadFile(filepath.Join(j.Directory, first.RequestID+".json"))
	if err != nil || !strings.Contains(string(data), `"state":"pending"`) {
		t.Fatal("active request record changed", err)
	}
}
func TestLaunchJournalNeverReplaysUnknownOutcome(t *testing.T) {
	j := &LaunchJournal{Directory: t.TempDir()}
	intent := LaunchIntent{RequestID: strings.Repeat("b", 32), Agent: "pi", Directory: "/project"}
	calls := 0
	launch := func(context.Context, LaunchIntent) (LaunchReceipt, error) {
		calls++
		return LaunchReceipt{}, errors.New("transport lost")
	}
	for i := 0; i < 2; i++ {
		if _, err := j.Do(context.Background(), intent, launch); err == nil {
			t.Fatal("failed launch accepted")
		}
	}
	if calls != 1 {
		t.Fatal("unknown outcome replayed")
	}
}

func TestIndependentLaunchesDoNotQueueBehindSlowSession(t *testing.T) {
	j := &LaunchJournal{Directory: t.TempDir()}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	first := LaunchIntent{RequestID: strings.Repeat("e", 32), Agent: "claude", Directory: "/one"}
	go func() {
		_, err := j.Do(context.Background(), first, func(_ context.Context, i LaunchIntent) (LaunchReceipt, error) {
			close(entered)
			<-release
			return LaunchReceipt{SessionID: i.RequestID, Agent: i.Agent}, nil
		})
		done <- err
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second := LaunchIntent{RequestID: strings.Repeat("f", 32), Agent: "codex", Directory: "/two"}
	_, err := j.Do(ctx, second, func(_ context.Context, i LaunchIntent) (LaunchReceipt, error) {
		return LaunchReceipt{SessionID: i.RequestID, Agent: i.Agent}, nil
	})
	if err != nil {
		t.Fatal("independent launch blocked", err)
	}
}
