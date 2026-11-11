package controlplane

import (
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/cloudstate"
	"fennec/internal/control"
	"fennec/internal/runta"

	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreparationSingleFlightAndLogoutCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "private"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	entered, stopped := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := &Controller{Account: manager, Prepare: func(ctx context.Context, token string, w io.Writer) error {
		if token != "private" {
			t.Error("incorrect credential")
		}
		calls.Add(1)
		close(entered)
		io.WriteString(w, "{\"type\":\"runtime_progress\",\"runtime_id\":\"shared\",\"stage\":\"connecting\"}\n")
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := c.ensureRuntime(ctx, json.RawMessage(`{}`)); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("preparation did not start")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate preparation", calls.Load())
	}
	if err := manager.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("old account preparation survived logout")
	}
	if got := c.runtimeSnapshot(); got.Stage != "idle" || got.RuntimeID != "" {
		t.Fatalf("old account runtime leaked: %+v", got)
	}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("signed-out preparation allowed")
	}
}

func TestUninstallQuiesceDrainsPreparationAndPreventsRecreation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := &Controller{Account: manager, Prepare: func(ctx context.Context, _ string, _ io.Writer) error {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ctx.Err()
	}}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.QuiescePreparation(ctx) }()
	<-cancelled
	select {
	case <-done:
		t.Fatal("quiesce returned before preparation cleanup")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("runtime recreated while uninstalling")
	}
	if calls.Load() != 1 {
		t.Fatal("queued reconciliation escaped uninstall barrier")
	}
	_, token, _, ok := manager.Session()
	if !ok || token != "fixture" {
		t.Fatal("recovery credentials deleted before cloud confirmation")
	}
}
func TestPersistentUninstallBlocksPreparationAfterDaemonRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	_ = manager.Restore(ctx)
	c := &Controller{Account: manager, PreparationBlocked: func() bool { return true }, Prepare: func(context.Context, string, io.Writer) error { t.Error("recreated runtime after restart"); return nil }}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("persisted uninstall ignored")
	}
	c.Additional = map[string]control.Handler{"settings.set": func(context.Context, json.RawMessage) (any, error) {
		t.Error("settings changed during uninstall")
		return nil, nil
	}, "uninstall.plan": func(context.Context, json.RawMessage) (any, error) { return "available", nil }}
	handlers := c.Handlers()
	if _, err := handlers["settings.set"](ctx, nil); err == nil {
		t.Fatal("mutation allowed")
	}
	if _, err := handlers["uninstall.plan"](ctx, nil); err != nil {
		t.Fatal("recovery disabled", err)
	}
}

func TestUnauthorizedPreparationOffersAccountLogin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &testCredentials{token: "fixture"}
	manager := account.New(ctx, store, "")
	_ = manager.Restore(ctx)
	c := &Controller{Account: manager, Prepare: func(context.Context, string, io.Writer) error { return &runta.APIError{Status: 401} }}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for {
		changed := manager.Changes()
		state := manager.Snapshot()
		if state.Status == "signed_out" {
			if state.Error != "session_expired" {
				t.Fatal(state)
			}
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatal("preparation failure did not restore sign-in entry")
		}
	}
	if token, _ := store.Read(ctx); token != "fixture" {
		t.Fatal("stored credential removed before reauthorization")
	}
}

func TestLoginCannotRaceActiveUninstall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &testCredentials{token: "fixture"}
	manager := account.New(ctx, store, "")
	_ = manager.Restore(ctx)
	c := &Controller{Account: manager, LoginAllowed: func() bool { return false }}
	if _, err := c.Handlers()["account.login"](ctx, json.RawMessage(`{"reauthenticate":true}`)); err == nil {
		t.Fatal("login started while uninstall was active")
	}
	if manager.Snapshot().Status != "authorized" {
		t.Fatal("rejected login modified account")
	}
}

// A saved preference is not proof that its asynchronous configuration completed.
func TestReconciliationGenerationsFenceQueuedConfiguration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan int64, 3)
	release := make(chan struct{}, 3)
	var calls atomic.Int64
	c := &Controller{Account: manager, Prepare: func(ctx context.Context, _ string, w io.Writer) error {
		n := calls.Add(1)
		entered <- n
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := io.WriteString(w, "{\"type\":\"runtime_progress\",\"runtime_id\":\"shared\",\"stage\":\"agents_installed\",\"status\":\"ready\"}\n")
		return err
	}}
	waitEntered := func() {
		t.Helper()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("configuration did not start")
		}
	}
	waitReady := func() RuntimeState {
		t.Helper()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			s := c.runtimeSnapshot()
			if s.Stage == "ready" && !s.Running && !s.Pending {
				return s
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("configuration did not complete")
			}
		}
	}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	waitEntered()
	initial := c.runtimeSnapshot()
	if initial.Generation == 0 || initial.Pending {
		t.Fatal(initial)
	}
	// Multiple choices while one operation is executing coalesce into one run.
	for i := 0; i < 3; i++ {
		if _, err := c.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	queued := c.runtimeSnapshot()
	if !queued.Pending || queued.Generation != initial.Generation || !queued.Running {
		t.Fatal(queued)
	}
	release <- struct{}{}
	waitEntered()
	next := c.runtimeSnapshot()
	if next.Generation <= initial.Generation || next.Pending || !next.Running || len(next.Events) != 0 {
		t.Fatalf("stale completion leaked into next run: %+v", next)
	}
	release <- struct{}{}
	ready := waitReady()
	if ready.Generation != next.Generation || ready.RuntimeID != "shared" || calls.Load() != 2 {
		t.Fatal(ready, calls.Load())
	}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if c.runtimeSnapshot().Generation != ready.Generation {
		t.Fatal("readiness check created new configuration")
	}
}

func TestFailedPreparationKeepsGenerationAndRetryAdvances(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	c := &Controller{Account: manager, Prepare: func(context.Context, string, io.Writer) error { return io.ErrUnexpectedEOF }}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	waitFailure := func() RuntimeState {
		t.Helper()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			state := c.runtimeSnapshot()
			if !state.Running && state.Stage == "failed" {
				return state
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("missing failed completion")
			}
		}
	}
	first := waitFailure()
	if first.Generation == 0 || first.Error == "" {
		t.Fatal(first)
	}
	if _, err := c.retryRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	second := waitFailure()
	if second.Generation <= first.Generation || second.Pending {
		t.Fatal(first, second)
	}
	if err := manager.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	signedOut := c.runtimeSnapshot()
	if signedOut.Generation != 0 || signedOut.Pending || len(signedOut.Events) != 0 {
		t.Fatal("previous account execution leaked", signedOut)
	}
}

func TestCloudQuiesceDrainsAndKeepsLocalControlsAvailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, epoch, _ := manager.Session()
	entered, released := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	c := &Controller{Account: manager, Prepare: func(ctx context.Context, _ string, _ io.Writer) error {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		<-released
		return ctx.Err()
	}}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	<-entered
	c.mu.Lock()
	c.runtime.RuntimeID = "shared"
	c.mu.Unlock()
	if err := c.QuiesceCloud(ctx, epoch, "other"); err == nil {
		t.Fatal("wrong runtime paused preparation")
	}
	c.mu.Lock()
	wronglyPaused := c.cloudPaused
	c.mu.Unlock()
	if wronglyPaused {
		t.Fatal("rejected runtime changed preparation admission")
	}
	if _, err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	paused := make(chan error, 1)
	go func() { paused <- c.QuiesceCloud(ctx, epoch, "shared") }()
	for {
		c.mu.Lock()
		stopping := c.cloudPaused
		c.mu.Unlock()
		if stopping {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("pause not started")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("cloud preparation restarted while draining")
	}
	select {
	case <-paused:
		t.Fatal("quiesce returned before preparation drained")
	default:
	}
	close(released)
	if err := <-paused; err != nil {
		t.Fatal(err)
	}
	if c.runtimeSnapshot().Stage != "disabled" || calls.Load() != 1 {
		t.Fatal("queued preparation escaped pause")
	}
	c.Additional = map[string]control.Handler{
		"settings.set": func(context.Context, json.RawMessage) (any, error) { return "available", nil },
		"sessions.browser": func(context.Context, json.RawMessage) (any, error) {
			t.Error("browser session escaped pause")
			return nil, nil
		},
		"agent.login.start": func(context.Context, json.RawMessage) (any, error) { t.Error("login escaped pause"); return nil, nil },
	}
	handlers := c.Handlers()
	if _, err := handlers["settings.set"](ctx, nil); err != nil {
		t.Fatal("local settings blocked", err)
	}
	for _, method := range []string{"sessions.browser", "agent.login.start"} {
		if _, err := handlers[method](ctx, nil); err == nil {
			t.Fatal("cloud operation allowed", method)
		}
	}
	if manager.Snapshot().Status != "authorized" {
		t.Fatal("cloud pause removed local login")
	}
	if err := c.ResumeCloud(epoch + 1); err == nil {
		t.Fatal("foreign account resumed lifecycle")
	}
	if err := c.ResumeCloud(epoch); err != nil {
		t.Fatal(err)
	}
	if state := c.runtimeSnapshot(); state.Stage != "idle" || state.Running {
		t.Fatal(state)
	}
}

func TestIntentionalCloudDeletionRestoresWithoutPreparationError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	manager := account.New(ctx, &testCredentials{token: "fixture"}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	c := &Controller{Account: manager, Prepare: func(context.Context, string, io.Writer) error { calls.Add(1); return cloudstate.ErrDisabled }}
	if _, err := c.ensureRuntime(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for {
		state := c.runtimeSnapshot()
		if !state.Running {
			if state.Stage != "disabled" || state.Error != "" || len(state.Events) != 0 {
				t.Fatal(state)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("restore did not complete")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := c.ensureRuntime(ctx, nil); err == nil {
		t.Fatal("automatic creation allowed")
	}
	if calls.Load() != 1 || manager.Snapshot().Status != "authorized" {
		t.Fatal("restoration changed login or retried preparation")
	}
}
