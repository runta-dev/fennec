//go:build scenario

package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"fennec/cua/internal/daemon"
	"fennec/cua/internal/fixture"
	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/parity"
)

// Where things live in the VM.
const (
	guestRoot  = "/Users/admin/cua"
	fennecBin  = guestRoot + "/bin/fennec-cua"
	driverBin  = guestRoot + "/bin/cua-driver"
	fixtureApp = guestRoot + "/apps/CUAFixture.app"
	shuttleApp = "/Users/admin/Applications/Loomhand/Shuttle.app"
	shuttleBin = shuttleApp + "/Contents/MacOS/shuttle"
	// Short directories: unix socket paths are limited to about 100 bytes.
	fennecDir  = "/tmp/fcua"
	shuttleDir = "/tmp/shx"
	scratchDir = "/tmp/scn"
)

var (
	flagBackends = flag.String("scenario.backends", "fennec,shuttle", "backends to run: fennec, shuttle or both")
	flagReport   = flag.String("scenario.report", guestRoot+"/scenario-report.json", "where the JSON report goes")
)

// Config is what a backend daemon is started with. Changing it restarts the
// daemon.
type Config struct {
	Input       string   // foreground (default), background, auto
	Allow       []string // app allowlist
	LockedInput bool
}

func (c Config) input() string {
	if c.Input == "" {
		return "foreground"
	}
	return c.Input
}

func (c Config) key() string {
	return fmt.Sprintf("%s|%s|%v", c.input(), strings.Join(c.Allow, ","), c.LockedInput)
}

// Driver performs a scenario's task through one backend.
type Driver func(ctx context.Context, s *Session) (parity.Outcome, error)

// Case is one scenario.
type Case struct {
	Caps   []string
	Scene  string // fixture scene to reset to; empty for real-app scenarios
	Config Config
	// Setup runs after the reset and before each side's driver.
	Setup   func(ctx context.Context) error
	Fennec  Driver
	Shuttle Driver
	// NoShuttle says why the scenario has no shuttle side.
	NoShuttle string
	// Check judges one side. It must hold for fennec-cua; shuttle's verdict
	// is recorded for the comparison.
	Check func(ctx context.Context, r *Result) error
	// Keys are the fixture state paths compared across backends; empty
	// (nil) compares the whole state minus volatile values; an empty list
	// compares none.
	Keys    []string
	Timeout time.Duration
	// Cleanup runs after each side, pass or fail.
	Cleanup func(ctx context.Context)
}

// Result is one side's run, as Check sees it.
type Result struct {
	Backend string
	Outcome parity.Outcome
	State   State // the fixture's raw state after the driver; nil without a scene
	Texts   []string
	Ms      int64
	Session *Session
}

// SideReport is one side in the report.
type SideReport struct {
	parity.Side
	Pass  bool     `json:"pass"`
	Front string   `json:"front,omitempty"` // the active app before the driver ran
	Fail  string   `json:"fail,omitempty"`
	Texts []string `json:"texts,omitempty"`
}

// Entry is one scenario in the report.
type Entry struct {
	Test     string      `json:"test"`
	Caps     []string    `json:"caps"`
	Skip     string      `json:"skip,omitempty"`
	Fennec   *SideReport `json:"fennec,omitempty"`
	Shuttle  *SideReport `json:"shuttle,omitempty"`
	NoReason string      `json:"no_shuttle,omitempty"`
	Agree    *bool       `json:"agree,omitempty"`
	Why      string      `json:"why,omitempty"`
}

type harness struct {
	fx      *fixture.Client
	app     *fixture.App // when the harness launched the fixture
	want    map[string]bool
	mu      sync.Mutex
	entries []Entry

	cur struct {
		name   string
		cfg    string
		client *mcpclient.Client
		procs  []*exec.Cmd
	}
}

var h = &harness{}

func TestMain(m *testing.M) {
	flag.Parse()
	h.loadReport()
	h.want = map[string]bool{}
	for _, b := range strings.Split(*flagBackends, ",") {
		switch strings.TrimSpace(b) {
		case "fennec", parity.Fennec:
			h.want[parity.Fennec] = true
		case parity.Shuttle:
			h.want[parity.Shuttle] = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := h.setup(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "scenario setup:", err)
		os.Exit(2)
	}
	code := m.Run()
	h.stopBackend()
	if h.app != nil {
		_ = h.app.Close()
	}
	os.Exit(code)
}

func (h *harness) setup(ctx context.Context) error {
	for _, d := range []string{fennecDir, shuttleDir, scratchDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// Neither backend may be left over from an earlier run.
	killAll()
	// The fixture must be known to LaunchServices for launch by name and
	// its URL scheme.
	_, _ = sh(ctx, "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "+fixtureApp)
	// cua-driver finds apps to launch by name among the installed ones.
	_, _ = sh(ctx, "mkdir -p $HOME/Applications && ln -sfn "+fixtureApp+" $HOME/Applications/CUAFixture.app")
	sock := os.Getenv("CUA_FIXTURE_SOCKET")
	if sock == "" {
		sock = "/tmp/fx.sock"
	}
	h.fx = fixture.New(sock)
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	_, err := h.fx.Scenes(cctx)
	cancel()
	if err == nil {
		return nil
	}
	app, err := fixture.Launch(ctx, fixtureApp, sock)
	if err != nil {
		return fmt.Errorf("launch the fixture: %w", err)
	}
	h.app, h.fx = app, app.Client
	return nil
}

// killAll ends every backend process: daemons, bridges and drivers.
func killAll() {
	for _, pat := range []string{fennecBin + " serve", shuttleBin + " serve", shuttleBin + " mcp", driverBin, shuttleApp + "/Contents/MacOS/cua-driver"} {
		_ = exec.Command("pkill", "-f", pat).Run()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("pgrep", "-f", "cua-driver").Run() != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = exec.Command("pkill", "-9", "-f", "cua-driver").Run()
}

// use returns a session on the named backend started with cfg, stopping
// the other backend first: the two never run at the same time, since each
// owns a cua-driver.
func (h *harness) use(ctx context.Context, name string, cfg Config) (*mcpclient.Client, error) {
	if h.cur.name == name && h.cur.cfg == cfg.key() && h.cur.client != nil {
		if alive(ctx, h.cur.client) {
			return h.cur.client, nil
		}
	}
	h.stopBackend()
	var err error
	switch name {
	case parity.Fennec:
		err = h.startFennec(ctx, cfg)
	case parity.Shuttle:
		err = h.startShuttle(ctx, cfg)
	default:
		err = fmt.Errorf("unknown backend %q", name)
	}
	if err != nil {
		h.stopBackend()
		return nil, err
	}
	h.cur.name, h.cur.cfg = name, cfg.key()
	return h.cur.client, nil
}

func alive(ctx context.Context, c *mcpclient.Client) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.Tools(cctx)
	return err == nil
}

func (h *harness) stopBackend() {
	if h.cur.client != nil {
		_ = closeSoon(h.cur.client)
	}
	for _, p := range h.cur.procs {
		if p.Process == nil {
			continue
		}
		_ = p.Process.Signal(syscall.SIGTERM)
	}
	for _, p := range h.cur.procs {
		if p.Process == nil {
			continue
		}
		done := make(chan struct{})
		go func() { _ = p.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			_ = p.Process.Kill()
			<-done
		}
	}
	h.cur.client, h.cur.procs, h.cur.name, h.cur.cfg = nil, nil, "", ""
	killAll()
}

func closeSoon(c *mcpclient.Client) error {
	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		return errors.New("close timed out")
	}
}

func logFile(dir string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, "backend.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func (h *harness) startFennec(ctx context.Context, cfg Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[desk]\ninput = %q\nlocked_input = %v\n", cfg.input(), cfg.LockedInput)
	if len(cfg.Allow) > 0 {
		q := make([]string, len(cfg.Allow))
		for i, a := range cfg.Allow {
			q[i] = fmt.Sprintf("%q", a)
		}
		fmt.Fprintf(&b, "[safety]\nallow = [%s]\n", strings.Join(q, ", "))
	}
	cfgPath := filepath.Join(fennecDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
		return err
	}
	lf, err := logFile(fennecDir)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd := exec.Command(fennecBin, "serve")
	cmd.Env = append(os.Environ(), "FENNEC_CUA_DIR="+fennecRun(), "FENNEC_CUA_DRIVER="+driverBin, "FENNEC_CUA_CONFIG="+cfgPath)
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		return err
	}
	h.cur.procs = append(h.cur.procs, cmd)
	paths := daemon.PathsIn(fennecRun())
	deadline := time.Now().Add(30 * time.Second)
	for {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		c, err := mcpclient.Dial(cctx, paths.MCP)
		cancel()
		if err == nil {
			h.cur.client = c
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fennec-cua never served: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func fennecRun() string { return filepath.Join(fennecDir, "run") }

func shuttleEnv() []string {
	return append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Join(shuttleDir, "cfg"), "SHUTTLE_SOCKET="+filepath.Join(shuttleDir, "desk.sock"))
}

func (h *harness) startShuttle(ctx context.Context, cfg Config) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[desk]\ninput = %q\n", cfg.input())
	if cfg.LockedInput {
		b.WriteString("locked_input = true\n")
	}
	if len(cfg.Allow) > 0 {
		q := make([]string, len(cfg.Allow))
		for i, a := range cfg.Allow {
			q[i] = fmt.Sprintf("%q", a)
		}
		fmt.Fprintf(&b, "[safety]\napps = [%s]\n", strings.Join(q, ", "))
	}
	dir := filepath.Join(shuttleDir, "cfg", "loomhand", "shuttle")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(b.String()), 0o600); err != nil {
		return err
	}
	lf, err := logFile(shuttleDir)
	if err != nil {
		return err
	}
	defer lf.Close()
	sock := filepath.Join(shuttleDir, "desk.sock")
	serve := exec.Command(shuttleBin, "serve", sock)
	serve.Env = shuttleEnv()
	serve.Stdout, serve.Stderr = lf, lf
	if err := serve.Start(); err != nil {
		return err
	}
	h.cur.procs = append(h.cur.procs, serve)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("shuttle never served")
		}
		time.Sleep(100 * time.Millisecond)
	}
	mcp := exec.Command(shuttleBin, "mcp")
	mcp.Env = shuttleEnv()
	mcp.Stderr = lf
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	c, err := mcpclient.StartCommand(cctx, mcp)
	if err != nil {
		return fmt.Errorf("shuttle mcp: %w", err)
	}
	h.cur.client = c
	// The first desk call starts the driver; make it before any timing.
	_, _ = c.Tool(cctx, "desk", map[string]any{"verb": "screen"})
	return nil
}

// run runs one scenario on the wanted backends, the running one first so
// a comparison costs one backend switch.
func run(t *testing.T, c Case) {
	t.Helper()
	e := Entry{Test: t.Name(), Caps: c.Caps}
	sides := []string{parity.Fennec, parity.Shuttle}
	if h.cur.name == parity.Shuttle {
		sides = []string{parity.Shuttle, parity.Fennec}
	}
	if c.Shuttle == nil {
		e.NoReason = c.NoShuttle
		if e.NoReason == "" {
			e.NoReason = "spec capability: fennec-cua only"
		}
	}
	var fennecFail string
	for _, name := range sides {
		d := c.Fennec
		if name == parity.Shuttle {
			d = c.Shuttle
		}
		if d == nil || !h.want[name] {
			continue
		}
		sr := h.side(t, c, name, d)
		if name == parity.Fennec {
			e.Fennec = sr
			if !sr.Pass {
				fennecFail = sr.Fail
			}
		} else {
			e.Shuttle = sr
		}
		verdict := "pass"
		if !sr.Pass {
			verdict = "FAIL: " + sr.Fail
		}
		t.Logf("%s: %s (%d ms, %d calls, ok=%v class=%q, front %s)", name, verdict, sr.Ms, sr.Outcome.Calls, sr.Outcome.OK, sr.Outcome.Class, sr.Front)
	}
	if e.Fennec != nil && e.Shuttle != nil {
		ag, why := parity.Judge(e.Fennec.Side, e.Shuttle.Side)
		if ag && e.Fennec.Pass != e.Shuttle.Pass {
			ag, why = false, fmt.Sprintf("checks differ: fennec pass=%v, shuttle pass=%v", e.Fennec.Pass, e.Shuttle.Pass)
		}
		e.Agree, e.Why = &ag, why
		if !ag {
			t.Logf("disagree: %s", why)
		}
	}
	h.add(e)
	if fennecFail != "" {
		t.Errorf("fennec-cua: %s", fennecFail)
	}
}

// skip records a scenario that cannot run in this VM.
func skip(t *testing.T, caps []string, why string) {
	t.Helper()
	h.add(Entry{Test: t.Name(), Caps: caps, Skip: why})
	t.Skip(why)
}

func (h *harness) side(t *testing.T, c Case, name string, d Driver) *SideReport {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fail := func(sr *SideReport, err error) *SideReport {
		sr.Err = err.Error()
		sr.Fail = "harness: " + err.Error()
		return sr
	}
	sr := &SideReport{}
	client, err := h.use(ctx, name, c.Config)
	if err != nil {
		return fail(sr, err)
	}
	if c.Cleanup != nil {
		defer c.Cleanup(context.Background())
	}
	cancelMenus(ctx)
	allowGuestAgentCapture()
	// The previous side's input is not this daemon's own: wait until it
	// no longer looks like the user's, so foreground input does not yield
	// to it. A reset sends no input, so the wait comes first and a scene
	// that changes soon after its reset (late focus) starts fresh.
	if err := waitIdle(ctx, 1200*time.Millisecond); err != nil {
		return fail(sr, err)
	}
	if c.Scene != "" {
		if err := h.fx.Reset(ctx, c.Scene); err != nil {
			return fail(sr, fmt.Errorf("reset %s: %w", c.Scene, err))
		}
		waitScene(ctx)
	}
	if c.Setup != nil {
		if err := c.Setup(ctx); err != nil {
			return fail(sr, fmt.Errorf("setup: %w", err))
		}
		if err := waitIdle(ctx, 1200*time.Millisecond); err != nil {
			return fail(sr, err)
		}
	}
	sr.Front = frontApp()
	s := &Session{c: client, Backend: name}
	start := time.Now()
	o, derr := d(ctx, s)
	o.Calls = s.calls
	sr.Outcome, sr.Ms, sr.Texts = o, time.Since(start).Milliseconds(), s.texts
	if derr != nil {
		sr.Err = derr.Error()
	}
	r := &Result{Backend: name, Outcome: o, Texts: s.texts, Ms: sr.Ms, Session: s}
	if c.Scene != "" {
		st, err := h.fx.RawState(ctx)
		if err != nil {
			return fail(sr, fmt.Errorf("state: %w", err))
		}
		r.State = st
		sr.State = project(st, c.Keys)
	}
	switch {
	case derr != nil:
		sr.Fail = "driver: " + derr.Error()
	case c.Check != nil:
		if err := c.Check(ctx, r); err != nil {
			sr.Fail = err.Error()
		}
	}
	sr.Pass = sr.Fail == ""
	return sr
}

// project keeps the compared state paths, or the whole state without its
// volatile values.
func project(st State, keys []string) map[string]any {
	out := map[string]any{}
	if keys == nil {
		for k, v := range st {
			if k != fixture.VolatileKey {
				out[k] = v
			}
		}
		return out
	}
	for _, k := range keys {
		out[k] = st.Get(k)
	}
	return out
}

func (h *harness) add(e Entry) {
	h.mu.Lock()
	for i, old := range h.entries {
		if old.Test == e.Test {
			h.entries = append(h.entries[:i], h.entries[i+1:]...)
			break
		}
	}
	h.entries = append(h.entries, e)
	h.mu.Unlock()
	if err := h.writeReport(); err != nil {
		fmt.Fprintln(os.Stderr, "scenario report:", err)
	}
}

// loadReport keeps the entries of an earlier run, so a run of some tests
// updates theirs and leaves the rest.
func (h *harness) loadReport() {
	data, err := os.ReadFile(*flagReport)
	if err != nil {
		return
	}
	var old struct {
		Scenarios []Entry `json:"scenarios"`
	}
	if json.Unmarshal(data, &old) == nil {
		h.entries = old.Scenarios
	}
}

func (h *harness) writeReport() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	sort.SliceStable(h.entries, func(i, j int) bool { return h.entries[i].Test < h.entries[j].Test })
	data, err := json.MarshalIndent(map[string]any{"at": time.Now().Format(time.RFC3339), "backends": *flagBackends, "scenarios": h.entries}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*flagReport, data, 0o644)
}
