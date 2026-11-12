package driver

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Restart backoff.
const (
	firstBackoff = time.Second
	maxBackoff   = 30 * time.Second
	healthyRun   = 30 * time.Second
	stopGrace    = 3 * time.Second
)

// Supervisor keeps a cua-driver child serving on Socket, restarting it with
// a backoff that doubles from 1 s up to 30 s, and back to 1 s after a run
// that lasted 30 s.
type Supervisor struct {
	Bin    string
	Socket string
	Env    []string
	Log    *log.Logger

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

// Run supervises until ctx ends, then stops the child: TERM, and KILL to its
// process group 3 s later.
func (s *Supervisor) Run(ctx context.Context) {
	s.done = make(chan struct{})
	defer close(s.done)
	backoff := firstBackoff
	for ctx.Err() == nil {
		start := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= healthyRun {
			backoff = firstBackoff
		}
		s.logf("cua-driver exited (%v); restarting in %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	cmd := exec.Command(s.Bin, "serve", "--socket", s.Socket)
	cmd.Env = append(os.Environ(), s.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		return err
	case <-ctx.Done():
		pgid := -cmd.Process.Pid
		_ = syscall.Kill(pgid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(stopGrace):
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			<-exited
		}
		return ctx.Err()
	}
}

// Wait blocks until Run has returned.
func (s *Supervisor) Wait() {
	if s.done != nil {
		<-s.done
	}
}

// PID reports the running child, if any.
func (s *Supervisor) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil || s.cmd.ProcessState != nil {
		return 0
	}
	return s.cmd.Process.Pid
}

func (s *Supervisor) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log.Printf(format, args...)
	}
}

// ErrNoDriver is returned when no driver binary is configured.
var ErrNoDriver = errors.New("cua-driver is not bundled")
