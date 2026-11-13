package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fennec/cua/internal/protocol"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/script"
)

// StderrTail is how much of a crashed plugin's stderr its error quotes.
const StderrTail = 2 << 10

// maxReport bounds a plugin's report.
const maxReport = 64 << 20

// RunCall is who runs a plugin.
type RunCall struct {
	Agent string // the caller that owns the run and its desk turn
	Input string // the configured input mode
	// Owner, when set, is whom the run's desk calls pass as instead of
	// Agent: the js call that started the plugin and holds the turn.
	Owner string
}

// report is what a plugin writes on its report descriptor.
type report struct {
	Result any                `json:"result"`
	Error  *protocol.RunError `json:"error"`
	Usage  protocol.Usage     `json:"usage"`
}

// RunPlugin runs the plugin name for one run request and reports as the run
// tool does: logs survive timeouts and stops. The caller holds the desk turn.
func (x *Extensions) RunPlugin(ctx context.Context, name string, call RunCall, req protocol.RunRequest) protocol.RunResult {
	start := time.Now()
	logs := &capped{max: script.LogCap}
	out := func(kind, msg string) protocol.RunResult {
		r := protocol.RunResult{Logs: logs.String(), DurationMs: int(time.Since(start).Milliseconds())}
		if kind != "" {
			r.Error = &protocol.RunError{Kind: kind, Message: msg}
		}
		return r
	}
	p := x.plugins[name]
	if p == nil {
		return out(protocol.RunImport, "no plugin named "+name)
	}
	timeout := script.DefaultTimeout
	if req.TimeoutS > 0 {
		timeout = min(time.Duration(req.TimeoutS)*time.Second, script.MaxTimeout)
	}
	line, err := json.Marshal(req)
	if err != nil {
		return out(protocol.RunImport, err.Error())
	}
	owner := call.Owner
	if owner == "" {
		owner = call.Agent
	}
	token, done := x.addRun(owner)
	defer done()

	cmd := exec.Command(p.Entry)
	cmd.Dir = home()
	cmd.Env = x.environ(map[string]string{
		EnvPlugin:   p.Name,
		EnvAgent:    call.Agent,
		EnvInput:    call.Input,
		EnvRun:      token,
		EnvReportFD: "3",
	})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	tail := &tailBuf{max: StderrTail}
	cmd.Stdout = logs
	cmd.Stderr = io.MultiWriter(logs, tail)
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return out(protocol.RunImport, err.Error())
	}
	rr, rw, err := os.Pipe()
	if err != nil {
		return out(protocol.RunImport, err.Error())
	}
	cmd.ExtraFiles = []*os.File{rw}
	if err := cmd.Start(); err != nil {
		_ = rr.Close()
		_ = rw.Close()
		return out(protocol.RunImport, "the plugin does not start: "+err.Error())
	}
	_ = rw.Close()
	pgid := cmd.Process.Pid
	reported := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(rr, maxReport+1))
		_ = rr.Close()
		reported <- data
	}()
	go func() {
		// The request, then stdin stays open: its end tells the plugin
		// the run is over or the daemon is gone.
		_, _ = stdin.Write(append(line, '\n'))
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	var waitErr error
	kind, msg := "", ""
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case waitErr = <-exited:
	case <-timer.C:
		kind, msg = protocol.RunTimeout, "the run timed out"
		waitErr = x.terminate(ctx, pgid, stdin, exited, true)
	case <-ctx.Done():
		kind, msg = protocol.RunStopped, "the run was stopped"
		if errors.Is(sched.Cause(ctx), context.DeadlineExceeded) {
			kind, msg = protocol.RunTimeout, "the run timed out"
		}
		waitErr = x.terminate(ctx, pgid, stdin, exited, false)
	}
	_ = stdin.Close()
	// Whatever the plugin started ends with its run.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)

	var rep report
	var data []byte
	select {
	case data = <-reported:
	case <-time.After(time.Second):
		// A process outside the group holds the descriptor open.
		_ = rr.Close()
	}
	haveReport := len(bytes.TrimSpace(data)) > 0
	if haveReport {
		if len(data) > maxReport {
			return out(protocol.RunException, "the plugin's report is larger than 64 MiB")
		}
		if err := json.Unmarshal(data, &rep); err != nil {
			if kind == "" {
				return out(protocol.RunException, "the plugin's report is not valid JSON: "+err.Error())
			}
			haveReport = false
		}
	}
	res := out(kind, msg)
	if haveReport {
		res.Usage = rep.Usage
	}
	if kind != "" {
		return res
	}
	if haveReport {
		res.Result = rep.Result
		if rep.Error != nil {
			res.Error = rep.Error
			return res
		}
	}
	if waitErr != nil {
		m := "the plugin " + exitText(waitErr)
		if t := strings.TrimSpace(tail.String()); t != "" {
			m += "; its stderr ends with:\n" + t
		}
		res.Result = nil
		res.Error = &protocol.RunError{Kind: protocol.RunException, Message: m}
	}
	return res
}

func exitText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "was killed by " + ws.Signal().String()
		}
		return "exited with status " + strconv.Itoa(ee.ExitCode())
	}
	return "failed: " + err.Error()
}

// terminate sends the plugin's group SIGTERM and SIGKILL after the grace.
// When a timeout began it, a caller that leaves during the grace ends the
// plugin at once.
func (x *Extensions) terminate(ctx context.Context, pgid int, stdin io.Closer, exited <-chan error, byTimeout bool) error {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	_ = stdin.Close()
	grace := time.NewTimer(x.Grace)
	defer grace.Stop()
	var leave <-chan struct{}
	if byTimeout {
		leave = ctx.Done()
	}
	select {
	case err := <-exited:
		return err
	case <-grace.C:
	case <-leave:
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	return <-exited
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

// capped keeps the first max bytes written.
type capped struct {
	max int
	mu  sync.Mutex
	b   bytes.Buffer
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.max - c.b.Len()
	if len(p) > room {
		c.cut = true
		if room > 0 {
			c.b.Write(p[:room])
		}
		return len(p), nil
	}
	c.b.Write(p)
	return len(p), nil
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cut {
		return c.b.String() + fmt.Sprintf("\n[logs cut at %d KiB]", c.max>>10)
	}
	return c.b.String()
}

// tailBuf keeps the last max bytes written.
type tailBuf struct {
	max int
	mu  sync.Mutex
	b   []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
