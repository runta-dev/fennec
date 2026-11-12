// Package fixture talks to CUAFixture, the scenario app in cua/testapp, over
// its control socket: reset to a named scene, read the true state, list the
// scenes. It implements parity.Fixture and can launch and quit the app.
//
// The wire is line-delimited JSON on a unix socket: one request object per
// line, one answer object per line, {"ok":true,...} or {"ok":false,"error":...}.
package fixture

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fennec/cua/internal/parity"
)

// VolatileKey is the top-level state key that holds values which change on
// their own (clocks, live-list order, pids, reset counters). State drops it
// so two runs of one scenario compare equal; RawState keeps it.
const VolatileKey = "volatile"

// Client is a connection-per-call client of the control socket.
type Client struct {
	Path string
}

var _ parity.Fixture = (*Client)(nil)

// New returns a client for the socket at path.
func New(path string) *Client { return &Client{Path: path} }

// Error is a request the fixture answered with ok=false.
type Error struct {
	Op      string
	Message string
	Busy    bool // the app's main thread did not answer in time
}

func (e *Error) Error() string { return fmt.Sprintf("fixture %s: %s", e.Op, e.Message) }

// Call sends one request and returns the decoded answer. A non-ok answer is
// returned as *Error.
func (c *Client) Call(ctx context.Context, req map[string]any) (map[string]any, error) {
	op, _ := req["op"].(string)
	if dl, ok := ctx.Deadline(); ok {
		// Let the app give up on a frozen main thread before we do.
		if _, set := req["timeout_ms"]; !set {
			if ms := time.Until(dl).Milliseconds() - 250; ms > 0 {
				req["timeout_ms"] = ms
			}
		}
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return nil, ctxErr(ctx, err)
	}
	r := bufio.NewReaderSize(conn, 1<<16)
	answer, err := r.ReadBytes('\n')
	if err != nil {
		return nil, ctxErr(ctx, fmt.Errorf("fixture %s: read answer: %w", op, err))
	}
	var out map[string]any
	if err := json.Unmarshal(answer, &out); err != nil {
		return nil, fmt.Errorf("fixture %s: bad answer: %w", op, err)
	}
	if ok, _ := out["ok"].(bool); !ok {
		msg, _ := out["error"].(string)
		busy, _ := out["busy"].(bool)
		if msg == "" {
			msg = "request failed"
		}
		return out, &Error{Op: op, Message: msg, Busy: busy}
	}
	return out, nil
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Reset rebuilds the fixture's windows to a scene's initial state. It
// returns once the scene is ready.
func (c *Client) Reset(ctx context.Context, scene string) error {
	_, err := c.Call(ctx, map[string]any{"op": "reset", "scene": scene})
	return err
}

// RawState returns the whole state the fixture reports.
func (c *Client) RawState(ctx context.Context) (map[string]any, error) {
	out, err := c.Call(ctx, map[string]any{"op": "state"})
	if err != nil {
		return nil, err
	}
	st, ok := out["state"].(map[string]any)
	if !ok {
		return nil, errors.New("fixture state: answer has no state object")
	}
	return st, nil
}

// State returns the fixture's state without the volatile part, for
// comparing two runs.
func (c *Client) State(ctx context.Context) (map[string]any, error) {
	st, err := c.RawState(ctx)
	if err != nil {
		return nil, err
	}
	delete(st, VolatileKey)
	return st, nil
}

// Scenes lists the scene names.
func (c *Client) Scenes(ctx context.Context) ([]string, error) {
	out, err := c.Call(ctx, map[string]any{"op": "scenes"})
	if err != nil {
		return nil, err
	}
	raw, ok := out["scenes"].([]any)
	if !ok {
		return nil, errors.New("fixture scenes: answer has no scene list")
	}
	names := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("fixture scenes: non-string entry %v", v)
		}
		names = append(names, s)
	}
	return names, nil
}

// Freeze blocks the app's main thread for d, simulating a hung app. It
// returns as soon as the freeze is scheduled.
func (c *Client) Freeze(ctx context.Context, d time.Duration) error {
	_, err := c.Call(ctx, map[string]any{"op": "freeze", "seconds": d.Seconds()})
	return err
}

// Quit asks the app to exit.
func (c *Client) Quit(ctx context.Context) error {
	_, err := c.Call(ctx, map[string]any{"op": "quit"})
	return err
}

// App is a running CUAFixture started by Launch.
type App struct {
	*Client
	Pid int

	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	closer sync.Once
}

// DefaultSocketPath is where the app serves when it was started without
// CUA_FIXTURE_SOCKET (for example by a backend under test launching it) and
// no other instance already serves there. It matches the app's
// NSTemporaryDirectory when TMPDIR is the per-user temporary directory.
func DefaultSocketPath() string { return filepath.Join(os.TempDir(), "cua-fixture.sock") }

// Executable returns the binary inside an app bundle.
func Executable(appPath string) string {
	return filepath.Join(appPath, "Contents", "MacOS", "CUAFixture")
}

// Launch starts the bundle's binary with CUA_FIXTURE_SOCKET set to
// socketPath and waits until the socket answers. The socket path must be
// shorter than 104 bytes (a macOS limit).
func Launch(ctx context.Context, appPath, socketPath string) (*App, error) {
	if len(socketPath) >= 104 {
		return nil, fmt.Errorf("fixture: socket path is %d bytes; macOS allows at most 103", len(socketPath))
	}
	bin := Executable(appPath)
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("fixture: %w", err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "CUA_FIXTURE_SOCKET="+socketPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("fixture: start: %w", err)
	}
	a := &App{Client: New(socketPath), Pid: cmd.Process.Pid, cmd: cmd, done: make(chan struct{})}
	go func() {
		a.err = cmd.Wait()
		close(a.done)
	}()
	if err := a.waitReady(ctx); err != nil {
		a.kill()
		return nil, err
	}
	return a, nil
}

func (a *App) waitReady(ctx context.Context) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := a.Scenes(cctx)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fixture: socket never answered: %w (last error: %v)", ctx.Err(), err)
		case <-a.done:
			return fmt.Errorf("fixture: app exited before its socket answered: %v", a.err)
		case <-tick.C:
		}
	}
}

// Close asks the app to quit, waits up to five seconds, then kills it.
func (a *App) Close() error {
	var err error
	a.closer.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		qerr := a.Quit(ctx)
		cancel()
		select {
		case <-a.done:
		case <-time.After(5 * time.Second):
			a.kill()
			if qerr != nil {
				err = qerr
			} else {
				err = errors.New("fixture: app did not quit; killed")
			}
		}
		os.Remove(a.Path)
	})
	return err
}

func (a *App) kill() {
	_ = a.cmd.Process.Kill()
	<-a.done
}
