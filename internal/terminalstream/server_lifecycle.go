package terminalstream

import (
	"context"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// The client normally pings every two seconds. Expire a connection even when
// SSH never delivers EOF (for example after a lost route or a sleeping Mac).
// Only incoming frames renew the lease: output from an agent is not evidence
// that its terminal client is still reachable. Agents remain owned by Herdr.
func serverLease(ctx context.Context, cancel context.CancelFunc, timeout time.Duration) func() {
	started := time.Now()
	var lastFrame atomic.Int64
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				remaining := timeout - (time.Since(started) - time.Duration(lastFrame.Load()))
				if remaining <= 0 {
					cancel()
					return
				}
				timer.Reset(remaining)
			}
		}
	}()
	return func() { lastFrame.Store(int64(time.Since(started))) }
}

func (session *serverSession) closePTY() {
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.closed {
		session.closed = true
		if session.file != nil {
			session.file.Close()
		}
	}
}

// Cancellation must release a writer blocked on the slave PTY before waiting
// for its process. HUP alone only cancels the attachment's context; it cannot
// interrupt a terminal write or terminal-state restoration. A stubborn wrapper
// gets a bounded grace period, without ever signaling Herdr's agent process.
// Call the returned function immediately after Wait, before canceling a
// normally completed session, to disarm cleanup and join any callback.
func (session *serverSession) watchAttachment(command *exec.Cmd) func() {
	exited := make(chan struct{})
	finished := make(chan struct{})
	stop := context.AfterFunc(session.ctx, func() {
		defer close(finished)
		session.window.close()
		session.closePTY()
		_ = command.Process.Signal(syscall.SIGHUP)
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-exited:
		case <-timer.C:
			_ = command.Process.Kill()
		}
	})
	return func() {
		close(exited)
		if !stop() {
			<-finished
		}
	}
}
