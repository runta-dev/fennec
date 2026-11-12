package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/cua/internal/engine"
	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
)

func tempPaths(t *testing.T) Paths {
	t.Helper()
	// Short base: unix socket paths are limited.
	dir, err := os.MkdirTemp("/tmp", "cua")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return PathsIn(filepath.Join(dir, "d"))
}

func startDaemon(t *testing.T, eng *engine.Engine) (Paths, context.CancelFunc, chan error) {
	t.Helper()
	p := tempPaths(t)
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, Info: mcpserver.Info{Name: "t"}, Ready: make(chan struct{})}
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()
	select {
	case <-d.Ready:
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not start")
	}
	t.Cleanup(func() { cancel(); <-errc })
	return p, cancel, errc
}

func newEngine(impl map[string]engine.Tool) *engine.Engine {
	return &engine.Engine{Turn: &sched.Turn{Busy: 200 * time.Millisecond}, Stopper: &sched.Stopper{}, Impl: impl}
}

func dialMCP(t *testing.T, p Paths) *mcpclient.Client {
	t.Helper()
	c, err := mcpclient.Dial(context.Background(), p.MCP)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestSocketsAreOwnerOnlyAndSingleInstance(t *testing.T) {
	eng := newEngine(nil)
	p, _, _ := startDaemon(t, eng)
	for _, s := range []string{p.MCP, p.Control} {
		fi, err := os.Lstat(s)
		if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", s, fi.Mode(), err)
		}
	}
	if fi, _ := os.Stat(p.Dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
	second := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper}
	if err := second.Run(context.Background()); !errors.Is(err, ErrRunning) {
		t.Errorf("second daemon: %v", err)
	}
	if _, err := os.Lstat(p.MCP); err != nil {
		t.Errorf("the loser removed the winner's socket: %v", err)
	}
}

func TestLongSocketPathFails(t *testing.T) {
	p := PathsIn("/tmp/" + strings.Repeat("x", 100))
	d := &Daemon{Paths: p}
	if err := d.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "at most 101") {
		t.Errorf("err %v", err)
	}
}

func TestStatusOverMCPAndControl(t *testing.T) {
	eng := newEngine(nil)
	p, _, _ := startDaemon(t, eng)
	c := dialMCP(t, p)
	ctx := context.Background()
	names, err := c.Tools(ctx)
	if err != nil || strings.Join(names, ",") != "observe,act,wait,run,flow,status" {
		t.Fatalf("tools %v %v", names, err)
	}
	r, err := c.Tool(ctx, "status", nil)
	if err != nil || r.IsError {
		t.Fatalf("status %+v %v", r, err)
	}
	var st protocol.StatusResult
	if err := json.Unmarshal([]byte(r.Text()), &st); err != nil || st.Version == "" || st.Stopped {
		t.Fatalf("status %s %v", r.Text(), err)
	}
	if _, err := Control(ctx, p, "stop"); err != nil {
		t.Fatal(err)
	}
	r, _ = c.Tool(ctx, "observe", nil)
	if !r.IsError || !strings.Contains(r.Text(), `"stopped"`) {
		t.Errorf("observe while stopped: %s", r.Text())
	}
	rep, err := Control(ctx, p, "status")
	if err != nil || !strings.Contains(mustJSON(rep.Status), `"stopped":true`) {
		t.Errorf("control status %v %v", rep, err)
	}
	if _, err := Control(ctx, p, "resume"); err != nil {
		t.Fatal(err)
	}
	r, _ = c.Tool(ctx, "observe", nil)
	if !strings.Contains(r.Text(), `"unavailable"`) {
		t.Errorf("observe after resume: %s", r.Text())
	}
	if _, err := Control(ctx, p, "../stop"); err == nil {
		t.Error("crafted op accepted")
	}
}

func TestStopEndsACallInFlightAndBusyBetweenSessions(t *testing.T) {
	started := make(chan struct{}, 1)
	eng := newEngine(map[string]engine.Tool{
		protocol.ToolAct: func(ctx context.Context, call *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
			release, err := call.Turn(ctx)
			if err != nil {
				return nil, engine.TurnError(err)
			}
			defer release()
			started <- struct{}{}
			<-ctx.Done()
			return nil, nil
		},
	})
	p, _, _ := startDaemon(t, eng)
	a, b := dialMCP(t, p), dialMCP(t, p)
	ctx := context.Background()
	got := make(chan mcpclient.ToolResult, 1)
	go func() {
		r, _ := a.Tool(ctx, "act", map[string]any{"steps": []any{}})
		got <- r
	}()
	<-started
	r, err := b.Tool(ctx, "act", map[string]any{"steps": []any{}})
	if err != nil || !strings.Contains(r.Text(), `"busy"`) {
		t.Fatalf("second session: %s %v", r.Text(), err)
	}
	start := time.Now()
	if _, err := Control(ctx, p, "stop"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if !strings.Contains(r.Text(), `"stopped"`) {
			t.Errorf("stopped call answered %s", r.Text())
		}
		if time.Since(start) > time.Second {
			t.Error("stop took over a second")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not end the call")
	}
}

func TestDroppedConnectionCancelsItsCall(t *testing.T) {
	ended := make(chan struct{})
	started := make(chan struct{})
	eng := newEngine(map[string]engine.Tool{
		protocol.ToolWait: func(ctx context.Context, _ *engine.Call, _ json.RawMessage) (any, *protocol.Error) {
			close(started)
			<-ctx.Done()
			close(ended)
			return nil, nil
		},
	})
	p, _, _ := startDaemon(t, eng)
	c, err := mcpclient.Dial(context.Background(), p.MCP)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = c.Tool(context.Background(), "wait", map[string]any{}) }()
	<-started
	_ = c.Close()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("call kept running after its connection closed")
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestStartingRunsUnderTheLockAndCanFailTheStart(t *testing.T) {
	p := tempPaths(t)
	eng := newEngine(nil)
	ran := false
	d := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, Starting: func(context.Context) error {
		ran = true
		if _, err := os.Stat(p.Lock); err != nil {
			t.Errorf("Starting ran before the lock: %v", err)
		}
		return errors.New("service x: path too long")
	}}
	if err := d.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "service x") || !ran {
		t.Fatalf("Run = %v (ran %v)", err, ran)
	}
	if _, err := os.Stat(p.MCP); !os.IsNotExist(err) {
		t.Error("the daemon listened after a failed start")
	}
}
