package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/mcpclient"
	"fennec/cua/internal/vm"
)

// Where the golden image keeps the backends.
const (
	GuestBin     = HomeDir + "/bench/bin"
	GuestFennec  = GuestBin + "/fennec-cua"
	GuestDriver  = GuestBin + "/cua-driver"
	GuestShuttle = HomeDir + "/Applications/Loomhand/Shuttle.app"
	ShuttleID    = "io.github.loomhand.shuttle"
)

// Backend names.
const (
	BackendDriver  = "cua-driver"
	BackendShuttle = "shuttle"
	BackendFennec  = "fennec-cua"
)

// Backends lists every backend in report order.
var Backends = []string{BackendDriver, BackendShuttle, BackendFennec}

// ModelAccess is the model endpoint the backends' own model-backed features
// use from inside the VM. An empty BaseURL turns those features off.
type ModelAccess struct {
	BaseURL string
	Model   string
}

// BackendSetup is the shell script that prepares a backend in a fresh VM
// (configuration files) and the command that serves its MCP on stdio.
func BackendSetup(name string, m ModelAccess) (setup, serve string, err error) {
	switch name {
	case BackendDriver:
		// --direct: the MCP process owns the runtime itself instead of
		// proxying to a CuaDriver.app that the image does not have.
		return "", "exec " + vm.Quote(GuestDriver) + " mcp --direct", nil
	case BackendFennec:
		// fennec-cua's only MCP surface is the js REPL (docs/codex-parity.md):
		// the agent sees js and js_reset and drives apps through cua.
		dir := "/tmp/fcua"
		cfg := ""
		if m.BaseURL != "" {
			cfg = fmt.Sprintf("[model]\nbase_url = %q\napi_key_env = \"\"\ndecide_api = \"chat\"\ndecide = %q\nground = %q\nask = %q\n", m.BaseURL, m.Model, m.Model, m.Model)
		}
		env := "export FENNEC_CUA_DIR=" + dir + "/run FENNEC_CUA_DRIVER=" + vm.Quote(GuestDriver) + " FENNEC_CUA_CONFIG=" + dir + "/config.toml; "
		setup = "mkdir -p " + dir + "/run && chmod 700 " + dir + " " + dir + "/run && printf %s " + vm.Quote(cfg) + " > " + dir + "/config.toml && chmod 600 " + dir + "/config.toml"
		// The daemon runs in the desktop session before the bridge connects,
		// so the bridge never starts one of its own.
		serve = env + "nohup " + vm.Quote(GuestFennec) + " serve > " + dir + "/serve.log 2>&1 < /dev/null & " +
			waitSocket(dir+"/run/cua.sock") + "exec " + vm.Quote(GuestFennec) + " mcp"
		return setup, serve, nil
	case BackendShuttle:
		dir := "/tmp/shx"
		cfg := ""
		if m.BaseURL != "" {
			cfg = fmt.Sprintf("base_url = %q\napi_key_env = \"\"\ndecide_api = \"chat\"\n\n[models]\ndecide = %q\nground = %q\nask = %q\n", m.BaseURL, m.Model, m.Model, m.Model)
		}
		bin := vm.Quote(GuestShuttle + "/Contents/MacOS/shuttle")
		env := "export XDG_CONFIG_HOME=" + dir + "/cfg SHUTTLE_SOCKET=" + dir + "/desk.sock; "
		setup = "mkdir -p " + dir + "/cfg/loomhand/shuttle && chmod 700 " + dir + " && printf %s " + vm.Quote(cfg) + " > " + dir + "/cfg/loomhand/shuttle/config.toml && chmod 600 " + dir + "/cfg/loomhand/shuttle/config.toml"
		serve = env + "nohup " + bin + " serve " + dir + "/desk.sock > " + dir + "/serve.log 2>&1 < /dev/null & " +
			waitSocket(dir+"/desk.sock") + "exec " + bin + " mcp"
		return setup, serve, nil
	}
	return "", "", fmt.Errorf("unknown backend %q", name)
}

// waitSocket waits up to 30 s for a unix socket to appear.
func waitSocket(path string) string {
	return "i=0; while [ ! -S " + path + " ] && [ $i -lt 150 ]; do sleep 0.2; i=$((i+1)); done; "
}

// TrialConfig is one (task, backend, trial) run.
type TrialConfig struct {
	Task    Task
	Backend string
	Trial   int
	Golden  string // the image every trial clones
	Tart    string // the tart binary
	Model   Model  // the agent's model, called from the host
	Access  ModelAccess
	// Agent bounds.
	MaxTurns  int
	WallClock time.Duration
	// Setup is the setup step to run before the pre_command (setup.go),
	// empty for none.
	Setup string
	// Sealed keeps no transcript, screenshot or backend log: the task
	// belongs to the held-out set.
	Sealed bool
	// Dir receives the trial's files: <base>.json, .jpg and .log.
	Dir  string
	Base string
	Log  func(format string, args ...any)
}

// TrialResult is what one trial produced. A HarnessError means the trial
// says nothing about the backend and is not scored.
type TrialResult struct {
	Task         string        `json:"task"`
	Backend      string        `json:"backend"`
	Trial        int           `json:"trial"`
	Attempt      int           `json:"attempt"`
	VM           string        `json:"vm"`
	HarnessError string        `json:"harness_error,omitempty"`
	Score        int           `json:"score"`
	EvalError    bool          `json:"eval_error,omitempty"`
	Grading      []GradeOutput `json:"grading,omitempty"`
	PreOK        *bool         `json:"pre_ok,omitempty"`
	PreOutput    string        `json:"pre_output,omitempty"`
	Tools        []string      `json:"tools,omitempty"`
	Instructions int           `json:"instructions_bytes,omitempty"`
	Transcript   *Transcript   `json:"transcript,omitempty"`
	BootMs       int64         `json:"boot_ms"`
	AgentMs      int64         `json:"agent_ms"`
	TotalMs      int64         `json:"total_ms"`
	Started      time.Time     `json:"started"`
	Screenshot   string        `json:"screenshot,omitempty"`
	// Setup is the setup step that recreated snapshot state, if any.
	Setup string `json:"setup,omitempty"`
	// Corrected says which harness correction the task ran with; such
	// runs are reported apart from the faithful upstream score.
	Corrected string `json:"corrected,omitempty"`
	// Safety is a safety task's distraction outcome (safety.go) and
	// SafetyOutput what the dialog printed.
	Safety       string `json:"safety,omitempty"`
	SafetyOutput string `json:"safety_output,omitempty"`
	// Sealed runs keep only scores and counts.
	Sealed bool `json:"sealed,omitempty"`
}

// The agent's model may fail for reasons outside the backend; those runs
// are harness failures.
func agentHarnessError(tr Transcript) string {
	switch tr.Stop {
	case "model_error":
		return "model: " + tr.Final
	case "backend_error":
		return "backend tools/list: " + tr.Final
	}
	return ""
}

// RunTrial runs one trial in a fresh clone of the golden image and always
// deletes the clone afterwards.
func RunTrial(ctx context.Context, c TrialConfig, attempt int) (res TrialResult) {
	start := time.Now()
	logf := c.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	name := fmt.Sprintf("cua-bench-%s-%d", strings.ReplaceAll(c.Backend, "-", ""), start.UnixNano()%1e9)
	res = TrialResult{Task: c.Task.ID, Backend: c.Backend, Trial: c.Trial, Attempt: attempt, VM: name, Started: start,
		Setup: c.Setup, Corrected: c.Task.Corrected, Sealed: c.Sealed}
	if c.Sealed {
		defer func() { seal(&res) }()
	}
	v := &vm.VM{Name: name, User: vm.DefaultUser, Tart: c.Tart}
	defer func() {
		// Stop from the host, then delete, even when ctx has ended.
		dctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = v.Stop(dctx)
		if err := v.Delete(dctx); err != nil {
			logf("%s: delete: %v", name, err)
		}
		res.TotalMs = time.Since(start).Milliseconds()
	}()
	fail := func(format string, args ...any) TrialResult {
		res.HarnessError = fmt.Sprintf(format, args...)
		return res
	}

	if err := v.Clone(ctx, c.Golden); err != nil {
		return fail("clone: %v", err)
	}
	bctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	_, err := v.Start(bctx)
	cancel()
	if err != nil {
		return fail("boot: %v", err)
	}
	s, err := v.Connect(ctx)
	if err != nil {
		return fail("connect: %v", err)
	}
	if err := WaitDesktop(ctx, s); err != nil {
		return fail("desktop: %v", err)
	}
	res.BootMs = time.Since(start).Milliseconds()

	// macOSWorld's protocol: the preparation command (up to three tries),
	// the before-action delay, the agent with the in-process event of a
	// safety task, the before-grading delay, then grading. All commands run
	// as the desktop user through the guest agent, which lives in the
	// logged-in GUI session. A setup step, if the task needs one, runs
	// before the preparation command.
	pre, err := Prepare(ctx, s, c.Task, c.Setup)
	if err != nil {
		return fail("%v", err)
	}
	res.PreOK, res.PreOutput = pre.OK, pre.Output
	setup, serve, err := BackendSetup(c.Backend, c.Access)
	if err != nil {
		return fail("%v", err)
	}
	if setup != "" {
		if out, ok := guestShell(ctx, s, setup, time.Minute); !ok {
			return fail("backend setup: %s", out)
		}
	}
	if !sleepCtx(ctx, time.Duration(c.Task.BeforeAction)*time.Second) {
		return fail("cancelled")
	}

	var logFile io.Writer = io.Discard
	if !c.Sealed {
		f, err := os.Create(filepath.Join(c.Dir, c.Base+".log"))
		if err != nil {
			return fail("log: %v", err)
		}
		defer f.Close()
		logFile = f
	}
	bin, args := v.ExecCommand(true, vm.InGUI(serve))
	client, cmd, instructions, err := startMCP(ctx, bin, args, logFile)
	if err != nil {
		return fail("backend start: %v", err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { stopMCP(client, cmd) }) }
	defer stop()
	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defs, err := client.ToolDefs(tctx)
	cancel()
	if err != nil || len(defs) == 0 {
		return fail("backend tools/list: %v (%d tools)", err, len(defs))
	}
	for _, d := range defs {
		res.Tools = append(res.Tools, d.Name)
	}
	res.Instructions = len(instructions)

	agentStart := time.Now()
	actx, cancel := context.WithTimeout(ctx, c.WallClock)
	opts := AgentOptions{MaxTurns: c.MaxTurns, Instructions: instructions}
	var dist *distraction
	if ip := c.Task.InProcess; ip != nil {
		// Injected before the agent's turn number ip.Step, then five
		// seconds for the dialog to appear, as run_task.py does.
		opts.BeforeTurn = func(turn int) {
			if turn != ip.Step || dist != nil {
				return
			}
			d, err := startDistraction(v, ip.Command)
			if err != nil {
				logf("%s: in-process event: %v", name, err)
				return
			}
			dist = d
			sleepCtx(actx, 5*time.Second)
		}
	}
	tr := Run(actx, c.Model, client, c.Task.Text, opts)
	cancel()
	res.AgentMs = time.Since(agentStart).Milliseconds()
	if dist != nil {
		res.Safety, res.SafetyOutput = dist.end(ctx, s, *c.Task.InProcess)
		res.SafetyOutput = clip(res.SafetyOutput, 500)
	} else if c.Task.InProcess != nil {
		// The agent stopped before the event's turn: upstream never
		// injects it either, and its result file is never written.
		res.Safety = SafetyError
		res.SafetyOutput = "the agent stopped before the in-process event's turn"
	}
	res.Transcript = &tr
	if ctx.Err() != nil {
		return fail("cancelled")
	}
	if h := agentHarnessError(tr); h != "" {
		return fail("%s", h)
	}
	stop()

	if !sleepCtx(ctx, time.Duration(c.Task.BeforeGrading)*time.Second) {
		return fail("cancelled")
	}
	if !c.Sealed {
		shot := filepath.Join(c.Dir, c.Base+".jpg")
		if err := screenshot(ctx, s, shot); err == nil {
			res.Screenshot = filepath.Base(shot)
		} else {
			logf("%s: screenshot: %v", name, err)
		}
	}
	res.Score, res.Grading, res.EvalError = GradeIn(ctx, s, c.Task)
	if ctx.Err() != nil {
		return fail("cancelled")
	}
	return res
}

// GradeIn grades a task in the VM: the front window leaves full screen,
// then macOSWorld's grading commands run.
func GradeIn(ctx context.Context, s *vm.Session, t Task) (int, []GradeOutput, bool) {
	guestShell(ctx, s, leaveFullScreen, time.Minute)
	return Grade(t.Grading, func(cmd string) (string, bool) {
		return guestShell(ctx, s, cmd, 3*time.Minute)
	})
}

// leaveFullScreen takes the front window out of full screen before grading,
// as macOSWorld does, since several checks read window 1's attributes.
const leaveFullScreen = `osascript -e 'tell application "System Events" to get value of attribute "AXFullScreen" of window 1 of (first application process whose frontmost is true)' 2>/dev/null | grep -q true && osascript -e 'tell application "System Events" to keystroke "f" using {control down, command down}'`

// guestShell runs a command with zsh, the upstream login shell, and returns
// its combined, trimmed output and whether it exited 0.
func guestShell(ctx context.Context, s *vm.Session, command string, timeout time.Duration) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out bytes.Buffer
	err := s.Run(cctx, "exec /bin/zsh -c "+vm.Quote(command), nil, &out, &out)
	text := strings.TrimSpace(out.String())
	if err != nil && cctx.Err() != nil {
		text += "\n(timed out)"
	}
	return text, err == nil
}

// WaitDesktop waits until the logged-in desktop is up (Dock and Finder run)
// and gives login-time banners a moment to pass.
func WaitDesktop(ctx context.Context, s *vm.Session) error {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if _, ok := guestShell(ctx, s, "pgrep -x Dock >/dev/null && pgrep -x Finder >/dev/null && pgrep -x WindowServer >/dev/null", 20*time.Second); ok {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("Dock and Finder did not start within 3 minutes")
		}
		if !sleepCtx(ctx, 2*time.Second) {
			return ctx.Err()
		}
	}
	if !sleepCtx(ctx, 15*time.Second) {
		return ctx.Err()
	}
	return nil
}

func screenshot(ctx context.Context, s *vm.Session, path string) error {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var out, errb bytes.Buffer
	if err := s.Run(cctx, "/usr/sbin/screencapture -x -t jpg /tmp/bench-final.jpg && cat /tmp/bench-final.jpg", nil, &out, &errb); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

// startMCP starts an MCP server whose stdio stays on the host and
// initialises a session with it.
func startMCP(ctx context.Context, bin string, args []string, stderr io.Writer) (*mcpclient.Client, *exec.Cmd, string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, "", err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, "", err
	}
	c := mcpclient.New(in, out)
	ictx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	// The handshake by hand, to keep the server's instructions.
	var init struct {
		Instructions string `json:"instructions"`
	}
	params := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "cua-bench", "version": "0"},
	}
	err = c.Call(ictx, "initialize", params, &init)
	if err == nil {
		// Nothing else writes yet, so the notification goes straight out.
		_, err = in.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	}
	if err != nil {
		stopMCP(c, cmd)
		return nil, nil, "", err
	}
	return c, cmd, init.Instructions, nil
}

// stopMCP closes the session and ends the local tart exec process.
func stopMCP(c *mcpclient.Client, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = c.Close()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// WriteResult saves a trial result as <dir>/<base>.json.
func WriteResult(dir, base string, r TrialResult) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, base+".json"), data, 0o644)
}

// PreResult is how the preparation went.
type PreResult struct {
	OK     *bool  // nil when the task has no pre_command
	Output string // clipped
}

// Prepare readies a fresh VM for a task: Automation grants for the apps
// its commands script that the golden image does not cover, the setup
// step if any, then the pre_command with macOSWorld's three tries. A
// failing setup step, or a failing pre_command of a task that demands an
// error-free preparation, is an error.
func Prepare(ctx context.Context, s *vm.Session, t Task, setup string) (PreResult, error) {
	var r PreResult
	if err := grantAutomation(ctx, s, scriptedBy(t)); err != nil {
		return r, fmt.Errorf("automation grants: %v", err)
	}
	if setup != "" {
		if err := RunSetup(ctx, s, setup, t); err != nil {
			return r, err
		}
	}
	if t.Pre == "" {
		return r, nil
	}
	ok := false
	var out string
	for i := 0; i < 3 && !ok; i++ {
		out, ok = guestShell(ctx, s, t.Pre, 3*time.Minute)
	}
	r.OK, r.Output = &ok, clip(out, 2000)
	if !ok && t.ForceErrorFreePrep {
		return r, fmt.Errorf("pre_command failed: %s", clip(out, 500))
	}
	return r, nil
}

// seal strips a held-out run down to scores and counts: no transcript
// steps or final message, no command output, no screenshot.
func seal(r *TrialResult) {
	r.Sealed = true
	r.PreOutput, r.SafetyOutput, r.Screenshot = "", "", ""
	for i := range r.Grading {
		r.Grading[i].Command, r.Grading[i].Output = "", ""
	}
	if t := r.Transcript; t != nil {
		steps := make([]Step, len(t.Steps))
		for i, st := range t.Steps {
			steps[i] = Step{Tool: st.Tool, IsError: st.IsError, Ms: st.Ms, Images: st.Images}
		}
		t.Steps, t.Final = steps, ""
	}
}
