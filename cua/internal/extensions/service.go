package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"fennec/cua/internal/sched"
)

// Restart backoff of a service that exits.
const (
	FirstBackoff = time.Second
	MaxBackoff   = 30 * time.Second
	// HealthyRun is how long a service must run for its next restart to
	// wait FirstBackoff again.
	HealthyRun = 30 * time.Second
)

// MaxReply bounds a service's reply.
const MaxReply = 64 << 20

// NextBackoff is the wait before restarting a service that exited after
// running for ran, when the previous wait was prev (zero for none): 1 s,
// doubling up to 30 s, and 1 s again after a run of 30 s or more.
func NextBackoff(prev, ran time.Duration) time.Duration {
	if prev == 0 || ran >= HealthyRun {
		return FirstBackoff
	}
	return min(prev*2, MaxBackoff)
}

// Start creates the services directory (0700) and runs every service. It
// fails before creating anything when a socket path is too long.
func (x *Extensions) Start(ctx context.Context) error {
	if err := x.Check(); err != nil {
		return err
	}
	if len(x.set.Services) == 0 {
		return nil
	}
	dir := x.ServicesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	sctx, cancel := context.WithCancel(ctx)
	x.mu.Lock()
	x.cancel = cancel
	for i := range x.set.Services {
		s := &x.set.Services[i]
		sv := &supervisor{x: x, s: s, socket: x.Socket(s.Name)}
		x.sups[s.Name] = sv
		x.wg.Add(1)
		go func() {
			defer x.wg.Done()
			sv.run(sctx)
		}()
	}
	x.mu.Unlock()
	return nil
}

// Close stops every service: SIGTERM, then SIGKILL to its process group
// after StopWait.
func (x *Extensions) Close() {
	if x == nil {
		return
	}
	x.stopOnce.Do(func() {
		x.mu.Lock()
		cancel := x.cancel
		x.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		x.wg.Wait()
	})
}

// ServicePID is the running process of a service, or 0.
func (x *Extensions) ServicePID(name string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	if sv := x.sups[name]; sv != nil {
		return sv.pid
	}
	return 0
}

type supervisor struct {
	x      *Extensions
	s      *Service
	socket string
	pid    int // guarded by x.mu
}

func (sv *supervisor) setPID(pid int) {
	sv.x.mu.Lock()
	sv.pid = pid
	sv.x.mu.Unlock()
}

func (sv *supervisor) logf(format string, args ...any) {
	sv.x.opts.Log("service %s: "+format, append([]any{sv.s.Name}, args...)...)
}

func (sv *supervisor) run(ctx context.Context) {
	sv.x.stopStale(sv.socket, sv.logf)
	var wait time.Duration
	for {
		// A service killed outright leaves its socket file behind.
		_ = os.Remove(sv.socket)
		started := time.Now()
		cmd := exec.Command(sv.s.Program, sv.s.Args...)
		cmd.Dir = home()
		cmd.Env = sv.x.environ(map[string]string{EnvServiceSocket: sv.socket})
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		lw := &lineLog{f: sv.logf}
		cmd.Stdout, cmd.Stderr = lw, lw
		cmd.WaitDelay = time.Second
		if err := cmd.Start(); err != nil {
			sv.logf("does not start: %v", err)
		} else {
			pgid := cmd.Process.Pid
			sv.setPID(pgid)
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case err := <-exited:
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
				sv.setPID(0)
				sv.logf("exited (%v)", exitText2(err))
			case <-ctx.Done():
				sv.x.teardown(pgid, exited)
				sv.setPID(0)
				_ = os.Remove(sv.socket)
				return
			}
		}
		wait = NextBackoff(wait, time.Since(started))
		sv.logf("restarting in %s", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			_ = os.Remove(sv.socket)
			return
		}
	}
}

func exitText2(err error) string {
	if err == nil {
		return "status 0"
	}
	return exitText(err)
}

// teardown sends the group SIGTERM and SIGKILL after StopWait.
func (x *Extensions) teardown(pgid int, exited <-chan error) {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(x.StopWait):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-exited
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// stopStale ends a copy a killed daemon left listening on socket: SIGTERM to
// it (and to the group it leads), SIGKILL to what is left after StopWait.
// Only this user's processes can listen there: the directory is 0700.
func (x *Extensions) stopStale(socket string, logf func(string, ...any)) {
	pid := 0
	// A copy that closes a connection at once can end it before its peer
	// is read; ask again.
	for i := 0; i < 3 && pid == 0; i++ {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			return
		}
		if p, err := peerPID(conn.(*net.UnixConn)); err == nil {
			pid = p
		}
		_ = conn.Close()
	}
	if pid <= 1 || pid == os.Getpid() {
		return
	}
	target := pid
	if pg, err := syscall.Getpgid(pid); err == nil && pg == pid {
		target = -pid
	}
	logf("stopping a copy left by an earlier daemon (pid %d)", pid)
	_ = syscall.Kill(target, syscall.SIGTERM)
	deadline := time.Now().Add(x.StopWait)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(target, syscall.SIGKILL)
	for i := 0; i < 50 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(20 * time.Millisecond)
	}
}

// CallService forwards one call of a service tool: one request line, then
// the service's reply until it closes. It takes no desk turn and no deadline
// besides ctx; when ctx ends the connection closes and the service sees it.
func (x *Extensions) CallService(ctx context.Context, tool, agent string, args json.RawMessage) ([]byte, error) {
	s := x.tools[tool]
	if s == nil {
		return nil, fmt.Errorf("no service answers %s", tool)
	}
	conn, err := x.dial(ctx, x.Socket(s.Name))
	if err != nil {
		if ctx.Err() != nil {
			return nil, sched.Cause(ctx)
		}
		return nil, fmt.Errorf("%w: service %s did not accept the call within %s", ErrNoService, s.Name, x.ListenWait)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	head, _ := json.Marshal(struct {
		Tool  string `json:"tool"`
		Agent string `json:"agent"`
	}{tool, agent})
	req := make([]byte, 0, len(head)+len(args)+16)
	req = append(req, head[:len(head)-1]...)
	req = append(req, `,"arguments":`...)
	req = append(req, args...)
	req = append(req, "}\n"...)

	wrote := make(chan error, 1)
	go func() {
		_, err := conn.Write(req)
		if err == nil {
			err = conn.CloseWrite()
		}
		wrote <- err
	}()
	reply, rerr := io.ReadAll(io.LimitReader(conn, MaxReply+1))
	// A service may answer before it has read the whole request; let the
	// request finish before the connection closes.
	werr := <-wrote
	if ctx.Err() != nil {
		return nil, sched.Cause(ctx)
	}
	if rerr != nil {
		return nil, fmt.Errorf("service %s: %v", s.Name, rerr)
	}
	if len(reply) > MaxReply {
		return nil, fmt.Errorf("service %s: the reply is larger than 64 MiB", s.Name)
	}
	reply = bytes.TrimSuffix(reply, []byte("\n"))
	if len(bytes.TrimSpace(reply)) == 0 {
		if werr != nil {
			return nil, fmt.Errorf("service %s closed the call without a reply: %v", s.Name, werr)
		}
		return nil, fmt.Errorf("service %s closed the call without a reply", s.Name)
	}
	return reply, nil
}

// dial connects to a service, waiting up to ListenWait while it starts.
func (x *Extensions) dial(ctx context.Context, socket string) (*net.UnixConn, error) {
	wctx, cancel := context.WithTimeout(ctx, x.ListenWait)
	defer cancel()
	var d net.Dialer
	for {
		c, err := d.DialContext(wctx, "unix", socket)
		if err == nil {
			return c.(*net.UnixConn), nil
		}
		select {
		case <-wctx.Done():
			return nil, errors.Join(err, wctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// lineLog sends a service's output to the log a line at a time.
type lineLog struct {
	f   func(string, ...any)
	buf []byte
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.f("%s", strings.TrimRight(string(l.buf[:i]), "\r"))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 4096 {
		l.f("%s", l.buf)
		l.buf = nil
	}
	return len(p), nil
}
