package chromium

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fennec/cua/internal/cdp"
)

// Environment overrides.
const (
	// EnvApp names Chromium.app (or its executable) for development;
	// "off" turns the browser surface off.
	EnvApp = "FENNEC_CUA_CHROMIUM"
	// EnvProfile names the managed profile directory.
	EnvProfile = "FENNEC_CUA_CHROMIUM_PROFILE"
	// EnvHeadless set to 1 runs the browser without windows.
	EnvHeadless = "FENNEC_CUA_CHROMIUM_HEADLESS"
)

// AppName is the bundled app's directory name.
const AppName = "Chromium.app"

// Executable returns the executable inside an app bundle path, or the
// path itself when it is already a file inside one.
func Executable(p string) string {
	if strings.HasSuffix(strings.TrimRight(p, "/"), ".app") {
		return filepath.Join(p, "Contents", "MacOS", strings.TrimSuffix(filepath.Base(strings.TrimRight(p, "/")), ".app"))
	}
	return p
}

// Locate finds the Chromium executable: EnvApp, then the bundle beside
// fennec-cua (self): Fennec.app/Contents/Helpers/Chromium.app for a
// fennec-cua in Contents/MacOS, or Chromium.app next to the binary in a
// staging directory. "" means none (or EnvApp is "off").
func Locate(self string) string {
	if v := os.Getenv(EnvApp); v != "" {
		if v == "off" {
			return ""
		}
		return Executable(v)
	}
	if self == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	dir := filepath.Dir(self)
	for _, app := range []string{
		filepath.Join(dir, "..", "Helpers", AppName),
		filepath.Join(dir, AppName),
	} {
		bin := Executable(filepath.Clean(app))
		if st, err := os.Stat(bin); err == nil && !st.IsDir() {
			return bin
		}
	}
	return ""
}

// DefaultProfileDir is the managed profile: EnvProfile, else
// ~/Library/Application Support/fennec/cua/Chromium. It is never a
// Google Chrome profile.
func DefaultProfileDir() (string, error) {
	if v := os.Getenv(EnvProfile); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "fennec", "cua", "Chromium"), nil
}

// Options configure a managed browser.
type Options struct {
	Bin        string
	ProfileDir string
	Headless   bool
	// MockKeychain runs Chromium with --use-mock-keychain: its cookie key
	// is a fixed public password, so cookies are protected only by the
	// 0700 profile directory. Builds without the renamed Safe Storage
	// item (SafeStorageOf) and tests use it; the bundled build keeps its
	// key in the "Fennec Safe Storage" Keychain item.
	MockKeychain bool
	// SafeStorageService names the Keychain item the build uses (state
	// and logs only; the name is compiled into the build).
	SafeStorageService string
	// Env is added to the child's environment (tests point HOME at a
	// temporary keychain).
	Env []string
	// Args are appended (tests).
	Args []string
	// IdleTimeout shuts the browser down after this long with no page
	// open and no command; zero means DefaultIdle, negative never.
	IdleTimeout time.Duration
	// StartTimeout bounds a launch; zero means 30 s.
	StartTimeout time.Duration
	Log          func(string, ...any)
}

// DefaultIdle is how long an unused browser with no pages stays up.
const DefaultIdle = 10 * time.Minute

// Args is the command line of a launch.
func Args(o Options) []string {
	args := []string{
		"--remote-debugging-pipe",
		"--user-data-dir=" + o.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-crash-restore-bubble",
		"--disable-features=Translate,MediaRouter,DialMediaRouteProvider",
		"--disable-component-update",
	}
	if o.MockKeychain {
		args = append(args, "--use-mock-keychain")
	}
	if o.Headless {
		args = append(args, "--headless", "--window-size=1280,800")
	} else {
		args = append(args, "--no-startup-window")
	}
	return append(args, o.Args...)
}

// Env is the child's environment: the caller's, with Chromium's notice
// about missing Google API keys silenced (the bundle carries none).
func Env(base []string) []string {
	out := make([]string, 0, len(base)+3)
	for _, e := range base {
		if strings.HasPrefix(e, "GOOGLE_API_KEY=") || strings.HasPrefix(e, "GOOGLE_DEFAULT_CLIENT_ID=") || strings.HasPrefix(e, "GOOGLE_DEFAULT_CLIENT_SECRET=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOOGLE_API_KEY=no", "GOOGLE_DEFAULT_CLIENT_ID=no", "GOOGLE_DEFAULT_CLIENT_SECRET=no")
}

// Browser is a supervised Chromium: started by the first Conn, started
// again after it exits, shut down when idle or by Close.
type Browser struct {
	opts Options

	mu       sync.Mutex
	proc     *process
	gen      int
	closed   bool
	lastUse  time.Time
	crashes  []time.Time
	starting chan struct{}
	idleStop chan struct{}
}

type process struct {
	cmd  *exec.Cmd
	conn *cdp.Conn
	done chan struct{} // closed when the process has exited
	tail *tail
	stop atomic.Bool // the supervisor ended it
}

// New makes a supervisor; nothing starts until Conn.
func New(o Options) *Browser {
	if o.IdleTimeout == 0 {
		o.IdleTimeout = DefaultIdle
	}
	if o.StartTimeout == 0 {
		o.StartTimeout = 30 * time.Second
	}
	return &Browser{opts: o}
}

// Available says whether the executable exists.
func (b *Browser) Available() bool {
	st, err := os.Stat(b.opts.Bin)
	return b.opts.Bin != "" && err == nil && !st.IsDir()
}

// Running says whether a browser process is up.
func (b *Browser) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.proc != nil && !b.proc.exited()
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (b *Browser) logf(format string, args ...any) {
	if b.opts.Log != nil {
		b.opts.Log(format, args...)
	}
}

// Conn returns the live connection, starting the browser when it is not
// running. gen counts starts: a new value means every target of an
// earlier one is gone.
func (b *Browser) Conn(ctx context.Context) (*cdp.Conn, int, error) {
	for {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return nil, 0, errors.New("the browser is shut down")
		}
		b.lastUse = time.Now()
		if p := b.proc; p != nil && !p.exited() {
			select {
			case <-p.conn.Done():
			default:
				gen := b.gen
				b.mu.Unlock()
				return p.conn, gen, nil
			}
		}
		if b.starting != nil {
			wait := b.starting
			b.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}
		if n := b.recentCrashes(); n >= 3 {
			b.mu.Unlock()
			return nil, 0, errors.New("Chromium exited three times within a minute; it is not started again for now")
		}
		old := b.proc
		b.starting = make(chan struct{})
		b.mu.Unlock()
		if old != nil {
			old.kill()
		}
		p, err := b.start(ctx)
		b.mu.Lock()
		close(b.starting)
		b.starting = nil
		if err != nil {
			b.mu.Unlock()
			return nil, 0, err
		}
		if b.closed {
			b.mu.Unlock()
			p.kill()
			return nil, 0, errors.New("the browser is shut down")
		}
		b.proc = p
		b.gen++
		gen := b.gen
		if b.idleStop == nil && b.opts.IdleTimeout > 0 {
			b.idleStop = make(chan struct{})
			go b.idleLoop(b.idleStop)
		}
		b.mu.Unlock()
		go b.watch(p, gen)
		return p.conn, gen, nil
	}
}

func (b *Browser) recentCrashes() int {
	n := 0
	for _, t := range b.crashes {
		if time.Since(t) < time.Minute {
			n++
		}
	}
	return n
}

// watch notes an exit the supervisor did not ask for.
func (b *Browser) watch(p *process, gen int) {
	<-p.done
	p.conn.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.proc == p && !p.stopping() {
		b.crashes = append(b.crashes, time.Now())
		b.logf("chromium (start %d) exited: %s", gen, p.tail.String())
	}
}

func (b *Browser) start(ctx context.Context) (*process, error) {
	if !b.Available() {
		return nil, errors.New("the bundled Chromium is missing")
	}
	if err := os.MkdirAll(b.opts.ProfileDir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(b.opts.ProfileDir, 0o700)
	if err := clearStaleOwner(b.opts.ProfileDir, b.opts.Bin, b.logf); err != nil {
		return nil, err
	}
	mig, err := prepareCookieStore(ctx, b.opts, b.logf)
	if err != nil {
		return nil, err
	}
	p, err := b.launch(ctx)
	if err != nil {
		if mig != nil {
			mig.restore()
		}
		return nil, err
	}
	if mig != nil {
		if err := mig.finish(ctx, p.conn, b.opts, b.logf); err != nil {
			b.logf("chromium: recording the cookie migration: %v", err)
		}
	}
	return p, nil
}

// launch starts the process and waits for its DevTools answer.
func (b *Browser) launch(ctx context.Context) (*process, error) {
	// fd 3 carries commands to Chromium, fd 4 its replies.
	cmdR, cmdW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	repR, repW, err := os.Pipe()
	if err != nil {
		cmdR.Close()
		cmdW.Close()
		return nil, err
	}
	cmd := exec.Command(b.opts.Bin, Args(b.opts)...)
	cmd.Env = append(Env(os.Environ()), b.opts.Env...)
	cmd.ExtraFiles = []*os.File{cmdR, repW}
	t := &tail{max: 8192}
	cmd.Stdout, cmd.Stderr = t, t
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		cmdR.Close()
		cmdW.Close()
		repR.Close()
		repW.Close()
		return nil, fmt.Errorf("start Chromium: %w", err)
	}
	cmdR.Close()
	repW.Close()
	p := &process{cmd: cmd, done: make(chan struct{}), tail: t}
	p.conn = cdp.New(repR, cmdW, func() { cmdW.Close(); repR.Close() })
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	hctx, cancel := context.WithTimeout(ctx, b.opts.StartTimeout)
	defer cancel()
	v, err := p.conn.Call(hctx, "", "Browser.getVersion", nil)
	if err != nil {
		p.kill()
		if p.exited() {
			return nil, fmt.Errorf("Chromium exited before it answered (%s)", strings.TrimSpace(t.String()))
		}
		return nil, fmt.Errorf("Chromium did not answer: %w", err)
	}
	var ver struct {
		Product string `json:"product"`
	}
	_ = json.Unmarshal(v, &ver)
	b.logf("chromium started: %s, pid %d", ver.Product, cmd.Process.Pid)
	return p, nil
}

func (p *process) stopping() bool { return p.stop.Load() }

// kill ends the process group: a polite SIGTERM, then SIGKILL.
func (p *process) kill() {
	p.stop.Store(true)
	p.conn.Close()
	if p.cmd.Process == nil {
		return
	}
	pid := p.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-p.done
	}
}

// shutdown asks the browser to close, then kills what remains.
func (p *process) shutdown() {
	p.stop.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, _ = p.conn.Call(ctx, "", "Browser.close", nil)
	cancel()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		p.kill()
	}
}

// Close shuts the browser down for good (daemon exit).
func (b *Browser) Close() {
	b.mu.Lock()
	b.closed = true
	p := b.proc
	b.proc = nil
	if b.idleStop != nil {
		close(b.idleStop)
		b.idleStop = nil
	}
	b.mu.Unlock()
	if p != nil && !p.exited() {
		p.shutdown()
	}
}

// Stop shuts the running browser down; the next Conn starts it again.
func (b *Browser) Stop() {
	b.mu.Lock()
	p := b.proc
	b.proc = nil
	b.mu.Unlock()
	if p != nil && !p.exited() {
		p.shutdown()
	}
}

// idleLoop stops a browser that has had no page and no command for
// IdleTimeout.
func (b *Browser) idleLoop(stop chan struct{}) {
	tick := time.NewTicker(b.opts.IdleTimeout / 4)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		b.mu.Lock()
		p := b.proc
		idle := time.Since(b.lastUse) >= b.opts.IdleTimeout
		b.mu.Unlock()
		if p == nil || p.exited() || !idle {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pages, err := pageCount(ctx, p.conn)
		cancel()
		if err == nil && pages > 0 {
			continue
		}
		b.logf("chromium idle for %v with no pages; shutting it down", b.opts.IdleTimeout)
		b.mu.Lock()
		if b.proc == p {
			b.proc = nil
		}
		b.mu.Unlock()
		p.shutdown()
	}
}

func pageCount(ctx context.Context, c *cdp.Conn) (int, error) {
	v, err := c.Call(ctx, "", "Target.getTargets", nil)
	if err != nil {
		return 0, err
	}
	var r struct {
		TargetInfos []struct {
			Type string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(v, &r); err != nil {
		return 0, err
	}
	n := 0
	for _, t := range r.TargetInfos {
		if t.Type == "page" {
			n++
		}
	}
	return n, nil
}

// clearStaleOwner handles a profile another process still holds
// (SingletonLock is a symlink to "host-pid"): a leftover of this same
// bundled Chromium (a daemon that died) is stopped; anything else is an
// error, since a second Chromium on the profile would only hand its
// command line to the first and exit.
func clearStaleOwner(profile, bin string, logf func(string, ...any)) error {
	target, err := os.Readlink(filepath.Join(profile, "SingletonLock"))
	if err != nil {
		return nil
	}
	i := strings.LastIndexByte(target, '-')
	if i < 0 {
		return nil
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 1 {
		return nil
	}
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return nil // gone; Chromium clears the stale lock itself
	}
	exe, err := processPath(pid)
	if err != nil {
		return nil
	}
	same := func(a, b string) bool {
		ra, err1 := filepath.EvalSymlinks(a)
		rb, err2 := filepath.EvalSymlinks(b)
		return err1 == nil && err2 == nil && ra == rb
	}
	if !same(exe, bin) {
		return fmt.Errorf("the managed browser profile is in use by another program (pid %d)", pid)
	}
	logf("stopping a leftover Chromium (pid %d) on the managed profile", pid)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(200 * time.Millisecond)
	return nil
}

// tail keeps the last bytes Chromium printed, for the log.
type tail struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
