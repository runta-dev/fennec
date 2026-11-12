package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Restart backoff and lifecycle timing.
const (
	firstBackoff = time.Second
	maxBackoff   = 30 * time.Second
	healthyRun   = 30 * time.Second
	stopGrace    = 3 * time.Second
	// ListenWait bounds how long a started driver may take to listen.
	ListenWait = 10 * time.Second
	listenPoll = 10 * time.Millisecond
	stalePoll  = 100 * time.Millisecond
)

// Launch settings: environment the driver reads.
const (
	EnvTelemetry      = "CUA_DRIVER_RS_TELEMETRY_ENABLED"
	EnvEmbedded       = "CUA_DRIVER_EMBEDDED"
	EnvConsentAlert   = "CUA_DRIVER_ALLOW_CAPTURE_CONSENT_ALERT"
	EnvWindowWatch    = "CUA_DRIVER_WINDOW_CHANGE_TIMEOUT_MS"
	WindowWatchMS     = "50"
	GlideMS           = "1"
	overlayOffWarning = "driver overlay = false has no effect on macOS: front-window tracking needs the overlay, so it stays on"
)

// BinName is the bundled driver's file name.
const BinName = "cua-driver"

// Backoff spaces restarts of a driver that keeps exiting: 1 s, doubling up
// to 30 s, and back to 1 s after a run that lasted 30 s.
type Backoff struct{ next time.Duration }

// After returns the delay before the next start, given how long the run
// that just ended lasted.
func (b *Backoff) After(ran time.Duration) time.Duration {
	if b.next == 0 || ran >= healthyRun {
		b.next = firstBackoff
	}
	d := b.next
	b.next *= 2
	if b.next > maxBackoff {
		b.next = maxBackoff
	}
	return d
}

// Supervisor keeps a cua-driver child serving on Socket, restarting it with
// a Backoff, and stops it when its context ends: the driver's own stop
// command and TERM, then KILL to its process group 3 s later.
type Supervisor struct {
	Bin    string
	Socket string
	Env    []string
	Log    *log.Logger
	// PIDFile is where the driver writes its pid; empty means Socket with
	// the extension .pid.
	PIDFile string
	// Embedded marks a driver run for a host app that owns the permission
	// prompts: the driver is told so and never raises the one-time screen
	// capture consent alert.
	Embedded bool
	// Foreground bounds the driver's post-action window watch to 50 ms,
	// for foreground input.
	Foreground bool
	// OverlayOff is the configuration asking for no overlay; on macOS the
	// overlay stays on and a warning says so.
	OverlayOff bool
	// ListenWait bounds a start; zero means ListenWait.
	ListenWait time.Duration

	// pause waits d or until ctx ends (false); tests replace it.
	pause func(ctx context.Context, d time.Duration) bool

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

// BundledBin is the driver beside exe (an app bundle's Helpers directory),
// or "" when none is bundled there.
func BundledBin(exe string) string {
	p := filepath.Join(filepath.Dir(exe), BinName)
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

func (s *Supervisor) pidFile() string {
	if s.PIDFile != "" {
		return s.PIDFile
	}
	return strings.TrimSuffix(s.Socket, filepath.Ext(s.Socket)) + ".pid"
}

// Args are the driver's command line: it serves on Socket, writes its pid,
// and draws its agent cursor gliding in 1 ms. The overlay is never turned
// off on macOS.
func (s *Supervisor) Args() []string {
	return []string{"serve", "--socket", s.Socket, "--pid-file", s.pidFile(), "--glide-ms", GlideMS}
}

// Environ is the environment the driver gets on top of the daemon's:
// telemetry off; embedded or allowed to raise the consent alert; a bounded
// window watch for foreground input.
func (s *Supervisor) Environ() []string {
	env := []string{EnvTelemetry + "=false"}
	if s.Embedded {
		env = append(env, EnvEmbedded+"=1")
	} else {
		env = append(env, EnvConsentAlert+"=1")
	}
	if s.Foreground {
		env = append(env, EnvWindowWatch+"="+WindowWatchMS)
	}
	return append(env, s.Env...)
}

// child is one started driver.
type child struct {
	cmd    *exec.Cmd
	exited chan struct{}
	err    error
}

// Start stops a driver an earlier daemon left on Socket, starts the driver
// and waits until it listens, then supervises it in the background until
// ctx ends. It fails when no driver is bundled, when the driver exits before
// listening, or when it does not listen within ListenWait.
func (s *Supervisor) Start(ctx context.Context) error {
	if s.Bin == "" {
		return ErrNoDriver
	}
	first, err := s.first(ctx)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	s.mu.Lock()
	s.done = done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.supervise(ctx, first)
	}()
	return nil
}

// Run supervises until ctx ends, then stops the child. A failed first start
// is logged and retried with the backoff.
func (s *Supervisor) Run(ctx context.Context) {
	done := make(chan struct{})
	s.mu.Lock()
	s.done = done
	s.mu.Unlock()
	defer close(done)
	if s.Bin == "" {
		s.logf("%v", ErrNoDriver)
		return
	}
	first, err := s.first(ctx)
	if err != nil {
		s.logf("%v", err)
	}
	s.supervise(ctx, first)
}

func (s *Supervisor) first(ctx context.Context) (*child, error) {
	if s.OverlayOff {
		s.logf("%s", overlayOffWarning)
	}
	if err := s.stopStale(ctx); err != nil {
		return nil, err
	}
	return s.spawn(ctx)
}

func (s *Supervisor) supervise(ctx context.Context, running *child) {
	var backoff Backoff
	started := time.Now()
	for {
		if running != nil {
			select {
			case <-running.exited:
				s.logf("cua-driver exited (%v); starting it again", running.err)
			case <-ctx.Done():
				s.stop(running)
				return
			}
		}
		delay := backoff.After(time.Since(started))
		if !s.wait(ctx, delay) {
			return
		}
		started = time.Now()
		c, err := s.spawn(ctx)
		if err != nil {
			s.logf("%v", err)
			if ctx.Err() != nil {
				return
			}
		}
		running = c
	}
}

func (s *Supervisor) wait(ctx context.Context, d time.Duration) bool {
	if s.pause != nil {
		return s.pause(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Supervisor) listenWait() time.Duration {
	if s.ListenWait > 0 {
		return s.ListenWait
	}
	return ListenWait
}

func (s *Supervisor) spawn(ctx context.Context) (*child, error) {
	ClearStaleSocket(s.Socket)
	cmd := exec.Command(s.Bin, s.Args()...)
	cmd.Env = append(os.Environ(), s.Environ()...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", s.Bin, err)
	}
	c := &child{cmd: cmd, exited: make(chan struct{})}
	go func() {
		c.err = cmd.Wait()
		close(c.exited)
	}()
	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	give := time.Now().Add(s.listenWait())
	for !answers(s.Socket) {
		select {
		case <-c.exited:
			return nil, fmt.Errorf("cua-driver exited with %v before it listened at %s", exitText(c.err), s.Socket)
		case <-ctx.Done():
			s.kill(c)
			return nil, ctx.Err()
		case <-time.After(listenPoll):
		}
		if time.Now().After(give) {
			s.kill(c)
			return nil, fmt.Errorf("cua-driver did not listen at %s within %s", s.Socket, s.listenWait())
		}
	}
	return c, nil
}

func exitText(err error) string {
	if err == nil {
		return "status 0"
	}
	return err.Error()
}

// stop asks the driver to exit, TERMs its process group, and KILLs the
// group when it has not exited within the grace; whatever of the group is
// still alive afterwards (a forked driver) is killed too.
func (s *Supervisor) stop(c *child) {
	stopCtx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	go s.requestStop(stopCtx)
	pgid := -c.cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-c.exited:
	case <-time.After(stopGrace):
	}
	s.kill(c)
}

// kill ends the child's whole process group at once.
func (s *Supervisor) kill(c *child) {
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	<-c.exited
}

// requestStop runs the driver's own stop command for Socket.
func (s *Supervisor) requestStop(ctx context.Context) {
	cmd := exec.CommandContext(ctx, s.Bin, "stop", "--socket", s.Socket)
	cmd.Env = append(os.Environ(), s.Environ()...)
	_ = cmd.Run()
}

// stopStale stops a driver an earlier (killed) daemon left serving on
// Socket, waiting up to the grace for its socket to go quiet.
func (s *Supervisor) stopStale(ctx context.Context) error {
	if !answers(s.Socket) {
		return nil
	}
	s.logf("stopping the cua-driver an earlier daemon left at %s", s.Socket)
	stopCtx, cancel := context.WithTimeout(ctx, stopGrace)
	defer cancel()
	s.requestStop(stopCtx)
	give := time.Now().Add(stopGrace)
	for answers(s.Socket) {
		if time.Now().After(give) {
			return fmt.Errorf("a cua-driver an earlier daemon left at %s does not stop", s.Socket)
		}
		select {
		case <-time.After(stalePoll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// answers reports whether something accepts connections on socket.
func answers(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ClearStaleSocket removes a socket file at path that refuses a connection,
// left by a driver that died. A socket that answers, a regular file, a
// symlink, a missing path or any other dial error leaves the path alone.
func ClearStaleSocket(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return false
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.Close()
		return false
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return false
	}
	return os.Remove(path) == nil
}

// Wait blocks until Run has returned, or a started supervisor has stopped
// its driver.
func (s *Supervisor) Wait() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
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

// VerifyBinary checks the bundled driver at path against its pinned sha256
// (hex); a mismatch, an unreadable file or a malformed pin is an error that
// names both digests, so a build can fail on it.
func VerifyBinary(path, pinned string) error {
	want := strings.ToLower(strings.TrimSpace(pinned))
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("the cua-driver pin %q is not a sha256 digest", pinned)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("the cua-driver pin %q is not a sha256 digest", pinned)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("cua-driver at %s has sha256 %s, but the pinned release is %s", path, got, want)
	}
	return nil
}
