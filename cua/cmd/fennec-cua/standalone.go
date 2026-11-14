package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fennec/cua/internal/chromium"
	"fennec/cua/internal/cli"
	"fennec/cua/internal/daemon"
	"fennec/cua/internal/driver"
	"fennec/cua/internal/macos"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/standalone"
	"fennec/cua/internal/vision"
)

// daemonStartWait bounds the wait for a daemon a command started: a cold
// start launches the app through LaunchServices and starts cua-driver.
const daemonStartWait = 20 * time.Second

// ensureDaemon starts the daemon when none answers, as the stdio bridge
// does, and waits until it answers.
func ensureDaemon(ctx context.Context, paths daemon.Paths, self string) error {
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return err
	}
	return cli.EnsureDaemon(ctx, paths.MCP, func() (<-chan error, error) {
		if cli.Embedded() {
			return nil, errors.New("embedded mode: the host app starts the daemon")
		}
		return cli.Start(cli.StartCommand(self))
	}, daemonStartWait)
}

// daemonInfo is the daemon.info control answer.
type daemonInfo struct {
	PID           int      `json:"pid"`
	Executable    string   `json:"executable"`
	Version       string   `json:"version"`
	Driver        string   `json:"driver,omitempty"`
	DriverRunning bool     `json:"driverRunning"`
	Chromium      string   `json:"chromium,omitempty"`
	Browser       bool     `json:"browser"`
	Sessions      []string `json:"sessions"`
}

// daemonOps are the daemon's own control operations beside the browser's:
// its identity and health, a quit, the permission prompts and a test
// capture. capture takes one small screen capture through cua-driver; nil
// when there is no driver.
func daemonOps(info func() daemonInfo, quit func(), capture func(context.Context) error) map[string]func(context.Context, json.RawMessage) (any, error) {
	check := func(ctx context.Context) map[string]any {
		if capture == nil {
			return map[string]any{"ok": false, "reason": "no cua-driver, so no screenshots"}
		}
		if err := capture(ctx); err != nil {
			return map[string]any{"ok": false, "reason": err.Error(), "consent": vision.ConsentRequired(err)}
		}
		return map[string]any{"ok": true}
	}
	return map[string]func(context.Context, json.RawMessage) (any, error){
		"daemon.info": func(context.Context, json.RawMessage) (any, error) { return info(), nil },
		"daemon.quit": func(context.Context, json.RawMessage) (any, error) {
			// Answer first; the daemon ends right after.
			time.AfterFunc(100*time.Millisecond, quit)
			return map[string]any{"quitting": true}, nil
		},
		"capture.check": func(ctx context.Context, _ json.RawMessage) (any, error) { return check(ctx), nil },
		"permissions.request": func(ctx context.Context, _ json.RawMessage) (any, error) {
			ax, sr := macos.RequestPermissions()
			out := map[string]any{"accessibility": grantWord(ax), "screen_recording": grantWord(sr)}
			// With the grant in place, macOS 15 and later still ask once
			// whether the app may capture the screen directly; the
			// embedded driver never raises that alert, so the daemon does,
			// here, where the user asked for the prompts.
			if sr {
				if c := check(ctx); c["consent"] == true {
					macos.RequestDirectCapture()
					out["direct_capture"] = "asked: choose Allow in the macOS alert"
				} else if c["ok"] == true {
					out["direct_capture"] = "granted"
				}
			}
			return out, nil
		},
	}
}

func grantWord(ok bool) string {
	if ok {
		return "granted"
	}
	return "denied"
}

// driverAnswers says whether the driver listens on its socket.
func driverAnswers(socket string) bool {
	c, err := net.DialTimeout("unix", socket, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// jsCmd is `fennec-cua js`: one call in a named session's runtime.
func jsCmd(ctx context.Context, paths daemon.Paths, self string, args []string, stdin io.Reader, out io.Writer) error {
	o, err := cli.ParseJSArgs(args, nil)
	if err != nil {
		return err
	}
	if o.Stdin {
		if o.Code, err = cli.ReadCode(stdin); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if err := ensureDaemon(ctx, paths, self); err != nil {
		return err
	}
	res, err := cli.JS(ctx, paths.MCP, o)
	if err != nil {
		return err
	}
	failed, err := cli.PrintResult(out, res, o.Out)
	if err != nil {
		return err
	}
	if failed {
		return errSilent
	}
	return nil
}

// errSilent fails the command after its output already said why.
var errSilent = errors.New("")

func jsResetCmd(ctx context.Context, paths daemon.Paths, self string, args []string, out io.Writer) error {
	session, err := cli.ParseSessionArgs(args, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := ensureDaemon(ctx, paths, self); err != nil {
		return err
	}
	res, err := cli.JSReset(ctx, paths.MCP, session)
	if err != nil {
		return err
	}
	_, err = cli.PrintResult(out, res, "")
	return err
}

func jsEndCmd(ctx context.Context, paths daemon.Paths, args []string, out io.Writer) error {
	session, err := cli.ParseSessionArgs(args, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := cli.JSEnd(ctx, paths.MCP, session); err != nil {
		if mcpserver.NoDaemon(err) {
			_, err = fmt.Fprintln(out, "no daemon runs; nothing to end")
			return err
		}
		return err
	}
	_, err = fmt.Fprintf(out, "session %q ended: its runtime is gone and its unmarked agent tabs close\n", session)
	return err
}

// quitDaemon asks a running daemon to quit and waits until it has. A
// daemon that does not run is not an error.
func quitDaemon(ctx context.Context, paths daemon.Paths) (bool, error) {
	// The process outlives its sockets while it stops the driver and the
	// browser: wait for the process itself, so an install never replaces
	// files it still runs.
	pid := 0
	if di, err := readInfo(ctx, paths); err == nil {
		pid = di.PID
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := daemon.Control(cctx, paths, "daemon.quit"); err != nil {
		if mcpserver.NoDaemon(err) {
			return false, nil
		}
		return false, fmt.Errorf("quit: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, err := os.Stat(paths.Control)
		socketGone := errors.Is(err, fs.ErrNotExist)
		if socketGone && (pid <= 0 || !alive(pid)) {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true, errors.New("the daemon did not quit within 30 s")
}

// alive reports whether process pid exists (signal 0 only checks).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readInfo(ctx context.Context, paths daemon.Paths) (daemonInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rep, err := daemon.Control(cctx, paths, "daemon.info")
	if err != nil {
		if rep.Error != "" {
			return daemonInfo{}, errors.New(rep.Error)
		}
		return daemonInfo{}, err
	}
	var di daemonInfo
	data, _ := json.Marshal(rep.Status)
	if err := json.Unmarshal(data, &di); err != nil {
		return daemonInfo{}, err
	}
	return di, nil
}

// stopDaemonIn quits the running daemon when it runs from inside app (an
// install or uninstall is about to change its files). A daemon of another
// app (Fennec.app) is left alone.
func stopDaemonIn(ctx context.Context, paths daemon.Paths, app string) func() error {
	return func() error {
		di, err := readInfo(ctx, paths)
		if err != nil {
			if mcpserver.NoDaemon(err) {
				return nil
			}
			// An older daemon without daemon.info: leave it.
			return nil
		}
		if !strings.HasPrefix(di.Executable, filepath.Clean(app)+string(filepath.Separator)) {
			return nil
		}
		_, err = quitDaemon(ctx, paths)
		return err
	}
}

type layoutFlags struct{ appDir, binDir string }

func (l *layoutFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&l.appDir, "app-dir", "", "directory for "+standalone.AppName+" (default ~/Applications)")
	fs.StringVar(&l.binDir, "bin-dir", "", "directory for the fennec-cua link (default ~/.local/bin)")
}

func (l layoutFlags) layout() (standalone.Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return standalone.Layout{}, err
	}
	lay := standalone.DefaultLayout(home)
	if l.appDir != "" {
		lay.AppDir = l.appDir
	}
	if l.binDir != "" {
		lay.BinDir = l.binDir
	}
	return lay, nil
}

// currentApp is the standalone app this fennec-cua runs from, or "".
func currentApp(self string) string {
	if app, ok := cli.AppBundle(self); ok && standalone.IsApp(app) {
		return app
	}
	return ""
}

func installCmd(ctx context.Context, paths daemon.Paths, self string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var lf layoutFlags
	lf.register(fs)
	noSetup := fs.Bool("no-setup", false, "do not open the setup window")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: fennec-cua install [--app-dir DIR] [--bin-dir DIR] [--no-setup]")
	}
	lay, err := lf.layout()
	if err != nil {
		return err
	}
	src := currentApp(self)
	if src == "" {
		return fmt.Errorf("run install with the fennec-cua inside the extracted archive's %s (this one is %s)", standalone.AppName, self)
	}
	in := &standalone.Installer{
		Layout: lay, Copy: standalone.Ditto, Verify: standalone.Codesign,
		StopDaemon: stopDaemonIn(ctx, paths, lay.App()),
		Log:        func(f string, a ...any) { fmt.Fprintf(out, "install: "+f+"\n", a...) },
	}
	res, err := in.Install(src, os.Getenv("PATH"))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nInstalled %s\n  app:     %s\n  command: %s -> %s\n", standalone.DisplayName, res.App, res.Link, lay.Exe())
	if res.Unquarantined > 0 {
		fmt.Fprintf(out, "  cleared the quarantine attribute (%s) on %d files of the installed copy, so macOS starts it\n", standalone.QuarantineAttr, res.Unquarantined)
	}
	if !res.OnPath {
		fmt.Fprintf(out, "\n%s is not on PATH. Add it (zsh):\n  echo 'export PATH=\"%s:$PATH\"' >> ~/.zshrc && export PATH=\"%s:$PATH\"\n", lay.BinDir, lay.BinDir, lay.BinDir)
	}
	fmt.Fprintf(out, "\n%s\nConnect an agent (details: fennec-cua mcp-config):\n  claude mcp add --scope user fennec-cua -- %s mcp\n  codex mcp add fennec-cua -- %s mcp\n", standalone.PermissionGuide(res.App), res.Link, res.Link)
	fmt.Fprintf(out, "\nInstall the agent skill: fennec-cua skill install --claude (or --codex, or --dest DIR)\n")
	if !*noSetup && !standalone.SetupDisabled() && standalone.SetupHelper(res.App) != "" {
		if err := standalone.OpenSetup(res.App); err != nil {
			fmt.Fprintf(out, "\nsetup window: %v (run `fennec-cua setup`)\n", err)
		} else {
			fmt.Fprintf(out, "\nOpened the setup window: it walks through the permissions (`fennec-cua setup` opens it again).\n")
		}
	}
	return nil
}

func uninstallCmd(ctx context.Context, paths daemon.Paths, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var lf layoutFlags
	lf.register(fs)
	purge := fs.Bool("purge", false, "also remove settings, flows, the browser profile, daemon files and the app's permission grants")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: fennec-cua uninstall [--purge] [--app-dir DIR] [--bin-dir DIR]")
	}
	lay, err := lf.layout()
	if err != nil {
		return err
	}
	var data []string
	if *purge {
		if fennec := fennecApp(); fennec != "" {
			return fmt.Errorf("--purge refused: %s shares these settings, flows and browser profile; uninstall without --purge", fennec)
		}
		cfgPath, err := daemon.ConfigPath()
		if err != nil {
			return err
		}
		profile, err := chromium.DefaultProfileDir()
		if err != nil {
			return err
		}
		data = append(data, filepath.Dir(cfgPath), profile)
		for _, p := range []string{paths.MCP, paths.Control, paths.Lock, paths.Driver, paths.Log, strings.TrimSuffix(paths.Driver, filepath.Ext(paths.Driver)) + ".pid", filepath.Join(paths.Dir, "recordings")} {
			data = append(data, p)
		}
	}
	in := &standalone.Installer{Layout: lay, StopDaemon: stopDaemonIn(ctx, paths, lay.App())}
	res, err := in.Uninstall(data)
	if err != nil {
		return err
	}
	for _, p := range res.Removed {
		fmt.Fprintln(out, "removed", p)
	}
	for _, p := range res.Kept {
		fmt.Fprintln(out, "kept", p)
	}
	if *purge {
		for _, svc := range []string{"Accessibility", "ScreenCapture"} {
			if o, err := exec.Command("/usr/bin/tccutil", "reset", svc, standalone.BundleID).CombinedOutput(); err != nil {
				fmt.Fprintf(out, "tccutil reset %s %s: %v %s\n", svc, standalone.BundleID, err, strings.TrimSpace(string(o)))
			} else {
				fmt.Fprintf(out, "reset the %s grant of %s\n", svc, standalone.BundleID)
			}
		}
		fmt.Fprintln(out, `The browser's cookie key stays in the login Keychain ("Fennec Safe Storage"); remove it with Keychain Access if nothing else uses it.`)
	} else {
		fmt.Fprintln(out, "Kept settings, flows and the browser profile in ~/Library/Application Support/fennec/cua (shared with Fennec.app if you use it); `uninstall --purge` would have removed them.")
	}
	return nil
}

// fennecApp is an installed Fennec.app, which shares fennec-cua's data.
func fennecApp() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{"/Applications/Fennec.app", filepath.Join(home, "Applications", "Fennec.app")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func permissionsCmd(ctx context.Context, paths daemon.Paths, self string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("permissions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	request := fs.Bool("request", false, "start the daemon and have it ask macOS for the grants (shows the system prompts)")
	open := fs.Bool("open", false, "open the System Settings panes")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: fennec-cua permissions [--request] [--open]")
	}
	app := currentApp(self)
	if app == "" {
		fmt.Fprintf(out, "warning: this fennec-cua (%s) is not inside %s; grants would go to the terminal that starts it\n", self, standalone.AppName)
		app = self
	}
	fmt.Fprint(out, standalone.PermissionGuide(app))
	if *request {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := ensureDaemon(cctx, paths, self); err != nil {
			return err
		}
		rep, err := daemon.Control(cctx, paths, "permissions.request")
		if err != nil {
			if rep.Error != "" {
				return errors.New(rep.Error)
			}
			return err
		}
		data, _ := json.Marshal(rep.Status)
		fmt.Fprintf(out, "\nasked macOS; grants now: %s\n(Screen Recording applies after `fennec-cua quit`.)\n", data)
	}
	if *open {
		for _, p := range standalone.Permissions {
			if p.Required {
				_ = exec.Command("/usr/bin/open", p.URL).Run()
			}
		}
	}
	return nil
}

func doctorCmd(ctx context.Context, paths daemon.Paths, self string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print the checks as JSON")
	noStart := fs.Bool("no-start", false, "do not start the daemon (permissions are then unknown)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return errors.New("usage: fennec-cua doctor [--json] [--no-start]")
	}
	f := standalone.Facts{Exe: self, App: currentApp(self)}
	if f.App != "" {
		f.SignatureErr = standalone.Codesign(f.App)
		f.Quarantined = standalone.Quarantined(f.App)
	}
	f.Driver = os.Getenv("FENNEC_CUA_DRIVER")
	if f.Driver == "" {
		f.Driver = driver.BundledBin(self)
	}
	f.ChromiumOff = os.Getenv(chromium.EnvApp) == "off"
	f.Chromium = chromium.Locate(self)
	if p, err := exec.LookPath(standalone.BinName); err == nil {
		f.Command = p
		if r, err := filepath.EvalSymlinks(p); err == nil && r == self {
			f.CommandOK = true
		}
	}
	if cfgPath, err := daemon.ConfigPath(); err == nil {
		f.ConfigPath = cfgPath
		if _, broken := daemon.LoadConfig(cfgPath); broken != nil {
			f.ConfigErr = broken
		}
		if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
			f.ConfigMissing = true
		}
	}

	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	running := false
	if c, err := net.DialTimeout("unix", paths.MCP, time.Second); err == nil {
		_ = c.Close()
		running = true
	}
	switch {
	case running:
	case *noStart:
		f.DaemonErr = errors.New("not running (--no-start); it starts on the first agent call")
	default:
		if err := ensureDaemon(cctx, paths, self); err != nil {
			f.DaemonErr = err
		} else {
			f.DaemonStarted = true
		}
	}
	if f.DaemonErr == nil {
		rep, err := daemon.Control(cctx, paths, "status")
		if err != nil {
			f.DaemonErr = fmt.Errorf("status: %w", err)
		} else {
			var st protocol.StatusResult
			data, _ := json.Marshal(rep.Status)
			_ = json.Unmarshal(data, &st)
			f.Permissions, f.Stopped = st.Permissions, st.Stopped
		}
		if di, err := readInfo(cctx, paths); err == nil {
			f.DaemonExe, f.DriverRunning, f.BrowserAvailable = di.Executable, di.DriverRunning, di.Browser
		} else {
			// A daemon without daemon.info (an older build): judge the
			// driver by its socket.
			f.DriverRunning = driverAnswers(paths.Driver)
		}
		if f.DriverRunning && f.Permissions["screen_recording"] == "granted" {
			if rep, err := daemon.Control(cctx, paths, "capture.check"); err == nil {
				var c struct {
					OK      bool   `json:"ok"`
					Reason  string `json:"reason"`
					Consent bool   `json:"consent"`
				}
				data, _ := json.Marshal(rep.Status)
				if json.Unmarshal(data, &c) == nil {
					f.CaptureChecked = true
					if !c.OK {
						f.CaptureErr = c.Reason
						if f.CaptureErr == "" {
							f.CaptureErr = "no reason given"
						}
					}
					f.CaptureConsent = c.Consent
				}
			}
		}
	}
	checks := standalone.Diagnose(f)
	if *asJSON {
		data, _ := json.MarshalIndent(map[string]any{"healthy": standalone.Healthy(checks), "checks": checks}, "", "  ")
		fmt.Fprintln(out, string(data))
	} else {
		standalone.Render(out, checks)
	}
	if !standalone.Healthy(checks) {
		return errSilent
	}
	return nil
}

// skillCmd is `fennec-cua skill path | install`.
func skillCmd(self string, args []string, out io.Writer) error {
	usage := errors.New("usage: fennec-cua skill path | install [--claude] [--codex] [--dest DIR]")
	if len(args) == 0 {
		return usage
	}
	app := currentApp(self)
	if app == "" {
		return fmt.Errorf("this fennec-cua (%s) is not inside %s, which carries the skill; the source is cua/skills/fennec-cua in the repository", self, standalone.AppName)
	}
	src := standalone.SkillDir(app)
	switch args[0] {
	case "path":
		if len(args) != 1 {
			return usage
		}
		_, err := fmt.Fprintln(out, src)
		return err
	case "install":
	default:
		return usage
	}
	fs := flag.NewFlagSet("skill install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	claude := fs.Bool("claude", false, "install into ~/.claude/skills (Claude Code)")
	codex := fs.Bool("codex", false, "install into ~/.codex/skills (Codex)")
	dest := fs.String("dest", "", "install into DIR/"+standalone.SkillName)
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return usage
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var dirs []string
	if *claude {
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
	}
	if *codex {
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		dirs = append(dirs, filepath.Join(codexHome, "skills"))
	}
	if *dest != "" {
		dirs = append(dirs, *dest)
	}
	if len(dirs) == 0 {
		return usage
	}
	for _, d := range dirs {
		target := filepath.Join(d, standalone.SkillName)
		if err := standalone.CopyTree(src, target); err != nil {
			return err
		}
		fmt.Fprintln(out, "installed the skill in", target)
	}
	return nil
}

// driverEmbedded says whether cua-driver runs embedded: inside the
// standalone app (or a host that sets FENNEC_CUA_EMBEDDED) it inherits the
// app's grants and never shows its own first-launch permission window or
// prompts; `fennec-cua permissions --request` asks for the app instead.
func driverEmbedded(self string) bool {
	return cli.Embedded() || currentApp(self) != ""
}

// loginKeychain reports whether this HOME has a login keychain, where the
// bundled Chromium keeps its cookie key.
func loginKeychain() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, "Library", "Keychains", "login.keychain-db"))
	return err == nil
}

// captureCheck is daemonOps' test capture over the driver client: a small
// desktop capture on the shared capture lane. nil without a driver.
func captureCheck(client *driver.Client) func(context.Context) error {
	if client == nil {
		return nil
	}
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := vision.DriverCapturer{Client: client}.Desktop(ctx, "", 64)
		return err
	}
}

// setupCmd is `fennec-cua setup`: the app's setup window.
func setupCmd(self string, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: fennec-cua setup")
	}
	app := currentApp(self)
	if app == "" {
		return fmt.Errorf("this fennec-cua (%s) is not inside %s, which holds the setup window; `fennec-cua permissions` prints the steps", self, standalone.AppName)
	}
	if err := standalone.OpenSetup(app); err != nil {
		return err
	}
	fmt.Fprintf(out, "opened the %s setup window\n", standalone.DisplayName)
	return nil
}

// offerSetup opens the setup window when the standalone app's daemon starts
// without a permission it needs, so the user sees what to allow the first
// time an agent calls. The window is the app's, and it comes up at most
// once per daemon.
func offerSetup(self string, logger *log.Logger) {
	app := currentApp(self)
	if app == "" || standalone.SetupDisabled() || cli.Embedded() {
		return
	}
	if macos.AccessibilityTrusted() && macos.ScreenCaptureAllowed() {
		return
	}
	if err := standalone.StartSetupChild(app); err != nil {
		logger.Printf("setup window: %v", err)
		return
	}
	logger.Printf("a permission is missing: opened the setup window")
}
