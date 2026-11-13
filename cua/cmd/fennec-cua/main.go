// Command fennec-cua is Fennec's computer-use host for macOS.
//
//	fennec-cua mcp                 MCP on stdio, bridged to the daemon (started if needed)
//	fennec-cua mcp --http ADDR     run the daemon with MCP over Streamable HTTP on ADDR too
//	fennec-cua serve [--http ADDR] run the daemon in the foreground
//	fennec-cua stop                stop every call until resume
//	fennec-cua resume              lift a stop
//	fennec-cua status              print the daemon's status
//	fennec-cua quit                end the daemon (the next command starts it again)
//	fennec-cua do TOOL [JSON | k=v ...]  one tool call; captures are saved to files
//	fennec-cua js [--session NAME] [CODE | -]  one js call in a named session that persists across invocations
//	fennec-cua js-reset | js-end [--session NAME]  reset or end a named session
//	fennec-cua install [--app-dir DIR] [--bin-dir DIR]  install the standalone app for this user
//	fennec-cua uninstall [--purge]  remove it (and, with --purge, its data)
//	fennec-cua doctor [--json] [--no-start]  check the install, daemon, permissions, driver and browser
//	fennec-cua permissions [--request] [--open]  the macOS permissions and how to grant them
//	fennec-cua mcp-config [claude|codex|json|http]  how to connect an agent
//	fennec-cua skill path | install [--claude] [--codex] [--dest DIR]  the agent skill
//	fennec-cua browser profiles     list Google Chrome profiles to import from
//	fennec-cua browser import [--profile NAME] [--replace]  import Chrome sign-ins (cookies) into the bundled Chromium
//	fennec-cua browser status       the bundled Chromium and the last import
//	fennec-cua version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fennec/cua/internal/actions"
	"fennec/cua/internal/ax"
	"fennec/cua/internal/browser"
	"fennec/cua/internal/chromeimport"
	"fennec/cua/internal/chromium"
	"fennec/cua/internal/cli"
	"fennec/cua/internal/daemon"
	"fennec/cua/internal/desk"
	"fennec/cua/internal/driver"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/extensions"
	"fennec/cua/internal/flows"
	"fennec/cua/internal/hotkey"
	"fennec/cua/internal/macos"
	"fennec/cua/internal/mcpserver"
	"fennec/cua/internal/models"
	"fennec/cua/internal/plan"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/record"
	"fennec/cua/internal/sched"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintln(os.Stderr, "fennec-cua:", err)
		}
		os.Exit(1)
	}
}

const usage = `usage: fennec-cua COMMAND
  mcp                                  MCP on stdio for an agent (starts the daemon on demand)
  js [--session NAME] [CODE | -]       run REPL code from a shell; the session persists across calls
  js-reset | js-end [--session NAME]   reset or end that session
  doctor [--json] [--no-start]         check install, daemon, permissions, driver and browser
  permissions [--request] [--open]     the macOS permissions and how to grant them
  mcp-config [claude|codex|json|http]  how to connect an agent
  skill path | install [--claude] [--codex] [--dest DIR]
  install [--app-dir DIR] [--bin-dir DIR] | uninstall [--purge]
  status | stop | resume | quit | serve [--http ADDR] | version
  do TOOL [JSON | k=v ...]             one call without a persistent session
  browser profiles | import [--profile NAME] [--replace] | status`

func run(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// Through the ~/.local/bin link, find the app the binary sits in: the
	// driver, the browser and the permission grants belong to it.
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	// LaunchServices opening the app (no arguments, launchd as parent)
	// serves the daemon in place.
	if cli.LaunchedByLaunchd(os.Getppid(), self, args) {
		args = []string{"serve"}
	}
	if len(args) == 0 {
		return errors.New(usage)
	}
	paths, err := daemon.DefaultPaths()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch args[0] {
	case "version":
		fmt.Println(engine.Version)
		return nil
	case "serve":
		addr, err := httpFlag(args[1:])
		if err != nil {
			return err
		}
		return serve(ctx, paths, self, addr)
	case "mcp":
		addr, err := httpFlag(args[1:])
		if err != nil {
			return err
		}
		if addr != "" {
			return serve(ctx, paths, self, addr)
		}
		return bridge(ctx, paths, self)
	case "do":
		return do(ctx, paths, self, args[1:])
	case "js":
		return jsCmd(ctx, paths, self, args[1:], os.Stdin, os.Stdout)
	case "js-reset", "js_reset":
		return jsResetCmd(ctx, paths, self, args[1:], os.Stdout)
	case "js-end":
		return jsEndCmd(ctx, paths, args[1:], os.Stdout)
	case "install":
		return installCmd(ctx, paths, self, args[1:], os.Stdout)
	case "uninstall":
		return uninstallCmd(ctx, paths, args[1:], os.Stdout)
	case "doctor":
		return doctorCmd(ctx, paths, self, args[1:], os.Stdout)
	case "permissions":
		return permissionsCmd(ctx, paths, self, args[1:], os.Stdout)
	case "mcp-config":
		return mcpConfigCmd(self, args[1:], os.Stdout)
	case "skill":
		return skillCmd(self, args[1:], os.Stdout)
	case "quit":
		quit, err := quitDaemon(ctx, paths)
		if err == nil && !quit {
			fmt.Println("no daemon runs")
		}
		return err
	case "browser":
		return browserCmd(ctx, paths, args[1:])
	case "stop", "resume", "status":
		cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
		defer ccancel()
		rep, err := daemon.Control(cctx, paths, args[0])
		if err != nil {
			return fmt.Errorf("%s: %w (is the daemon running?)", args[0], err)
		}
		if rep.Status != nil {
			out, _ := json.MarshalIndent(rep.Status, "", "  ")
			fmt.Println(string(out))
		}
		return nil
	}
	return fmt.Errorf("unknown command %q; %s", args[0], usage)
}

// httpFlag reads an optional --http ADDR (or --http=ADDR).
func httpFlag(args []string) (string, error) {
	switch {
	case len(args) == 0:
		return "", nil
	case len(args) == 2 && args[0] == "--http" && args[1] != "":
		return args[1], nil
	case len(args) == 1 && strings.HasPrefix(args[0], "--http=") && len(args[0]) > len("--http="):
		return strings.TrimPrefix(args[0], "--http="), nil
	}
	return "", errors.New(usage)
}

func info() mcpserver.Info {
	return mcpserver.Info{Name: "fennec-cua", Version: engine.Version, Instructions: engine.Instructions, IDPrefix: "mcp"}
}

type platform struct{}

func (platform) AccessibilityTrusted() bool             { return macos.AccessibilityTrusted() }
func (platform) ScreenCaptureAllowed() bool             { return macos.ScreenCaptureAllowed() }
func (platform) MainDisplayPixels() (w, h int, ok bool) { return macos.MainDisplayPixels() }

// policyConfig is the input policy the configuration sets.
func policyConfig(cfg daemon.Config) policy.Config {
	return policy.Config{Input: cfg.Desk.Input, Allowlist: cfg.Safety.Allow, Confirm: cfg.Safety.Confirm, LockedInput: cfg.Desk.LockedInput}
}

func serve(ctx context.Context, paths daemon.Paths, self, httpAddr string) error {
	// `fennec-cua quit` (the daemon.quit control) ends the daemon.
	ctx, quit := context.WithCancel(ctx)
	defer quit()
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	logger := log.New(io.MultiWriter(logFile, os.Stderr), "fennec-cua ", log.LstdFlags|log.Lmicroseconds)

	// The configuration: a file that does not load closes every tool but
	// status until it is fixed.
	cfgPath, err := daemon.ConfigPath()
	if err != nil {
		return err
	}
	cfg, broken := daemon.LoadConfig(cfgPath)
	if broken != nil {
		logger.Printf("configuration: %v", broken)
		cfg = daemon.DefaultConfig()
	}
	pol, err := policy.New(policyConfig(cfg), nil)
	if err != nil {
		if broken == nil {
			broken = err
		}
		pol, _ = policy.New(policy.Config{}, nil)
	}
	input := pol.Input

	stopper := &sched.Stopper{}
	dk := desk.New()
	observer := &view.Observer{Source: dk, Refs: view.NewRegistry(view.MaxRefs), Input: input}

	// Captures and pointer, key and typing input go through cua-driver;
	// without it observe refuses image and vision, and act answers
	// unavailable for driver steps while accessibility steps still work.
	bin := os.Getenv("FENNEC_CUA_DRIVER")
	if bin == "" {
		bin = driver.BundledBin(self)
	}
	var (
		imager  engine.Imager
		client  *driver.Client
		service *vision.Service
		sup     *driver.Supervisor
	)
	if bin != "" {
		// A result the driver marks isError is an error for every caller.
		client = &driver.Client{Socket: paths.Driver, Activity: pol.Activity, ToolErrors: true}
		defer client.Close()
		service = vision.NewService(vision.DriverCapturer{Client: client}, windowID(observer.Refs))
		imager = visionImager{s: service, allow: pol.Allow}
		dk.DriverPath = bin
		sup = &driver.Supervisor{Bin: bin, Socket: paths.Driver, Log: logger, Embedded: driverEmbedded(self), Foreground: input == protocol.InputForeground, OverlayOff: !cfg.Driver.Overlay}
		// The driver's own windows (its overlay) are never an agent's.
		dk.ExcludePIDs = func() []int { return []int{sup.PID()} }
	} else {
		logger.Printf("no bundled cua-driver; desktop tools are unavailable")
	}

	// The act tool shares the observer's refs, so refs from observe resolve.
	// Its backend shares one fresh window list between the plan and the
	// actions executor (actions.Lists).
	executor := &actions.Executor{B: actions.Watch(desk.NewActions(dk, client), &actions.Lists{}), Refs: observer.Refs, Policy: pol}
	acting := &record.Acting{}
	webTree := &ax.WebTree{Get: ax.WebTreeOn, Set: ax.SetWebTree}
	var held sync.Map // pids whose page tree plans turned on
	defer held.Range(func(pid, _ any) bool { _ = webTree.Release(pid.(int), "plans"); return true })
	px := &plan.Executor{
		X:        executor,
		Acting:   acting,
		Observer: observer,
		Log:      logger.Printf,
		WebTree: func(ctx context.Context, pid int) (bool, error) {
			on, err := webTree.Hold(pid, "plans")
			if err == nil {
				held.Store(pid, true)
			}
			return on, err
		},
	}
	// Waits for a window change read the full list only when the cheap
	// fingerprint of the on-screen windows moves (or now and then).
	px.WindowChange = func(ctx context.Context, limit time.Duration, list func(context.Context) (bool, error)) (bool, error) {
		return ax.WindowPoll{Fingerprint: func() (uint64, bool) {
			ws, err := ax.ServerWindows(true)
			if err != nil {
				return 0, false
			}
			return ax.Fingerprint(ws), true
		}, List: list}.Run(ctx, limit)
	}
	if service != nil {
		px.Screen = screen{c: service.Capturer}
		px.Sessions = service.Sessions
		px.WindowImage = func(ctx context.Context, pid int, key string) (int, int, error) {
			id, ok := cgID(key)
			if !ok {
				return 0, 0, errors.New("the window has no window server id")
			}
			c, err := service.Capturer.Window(ctx, "plan", pid, id)
			if err != nil {
				return 0, 0, err
			}
			return c.W, c.H, nil
		}
	}

	// Flows live beside the configuration; recordings beside the daemon.
	store, err := flows.NewStore(filepath.Join(filepath.Dir(cfgPath), "flows"))
	if err != nil {
		logger.Printf("flow store: %v", err)
		store = nil
	}
	var chains *flows.Chains
	if store != nil {
		chains = &flows.Chains{Store: store}
		px.Chains, px.Store = chains, store
	}
	recorder := &record.Recorder{
		Dir:     filepath.Join(paths.Dir, "recordings"),
		Acting:  acting,
		Stopped: stopper.Stopped,
		Log:     logger.Printf,
		Desk: &record.LiveDesk{Source: dk, Capture: func(ctx context.Context, w view.RawWindow) ([]byte, error) {
			if service == nil {
				return nil, driver.ErrNoDriver
			}
			id, ok := cgID(w.Key)
			if !ok {
				return nil, errors.New("the window has no window server id")
			}
			c, err := service.Capturer.Window(ctx, "recorder", w.PID, id)
			if err != nil {
				return nil, err
			}
			return c.PNG, nil
		}},
	}
	// A stop ends every call in flight (a running script among them) and then
	// a recording in progress, keeping what it recorded.
	halt := &policy.Halt{Recording: func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := recorder.Finish(ctx); err != nil {
			logger.Printf("recording at stop: %v", err)
		}
	}}
	stopper.OnState(halt.OnState)
	fl := &engine.Flows{Store: store, Chains: chains, Recorder: recorder, Run: px.Run, Gate: pol.Gate}

	eng := &engine.Engine{
		Turn:     &sched.Turn{},
		Stopper:  stopper,
		Platform: platform{},
		Input:    input,
		Admit:    pol.Admit,
		Broken:   broken,
		RunOff:   !cfg.Tools.Run,
	}
	eng.Impl = map[string]engine.Tool{
		protocol.ToolObserve: engine.ObserveToolWith(observer, imager),
		protocol.ToolAct:     px.Tool(),
		protocol.ToolWait:    px.WaitTool(),
		protocol.ToolFlow:    engine.FlowTool(fl),
	}
	// Models: the run tool always answers (a missing model answers its
	// calls unavailable); the client is built once the daemon listens.
	mdl := &modelBox{}
	eng.Impl[protocol.ToolRun] = eng.RunTool(mdl)
	// The REPL's app inventory and paste.
	eng.Models = mdl
	eng.Impl[protocol.ToolApps] = eng.AppsTool(driverApps(client))
	eng.Impl[protocol.ToolPaste] = eng.PasteTool(pasteboard{})
	px.Models, px.VisionModel = mdl, cfg.Model.Models.Verify != nil
	// Browser tabs: the bundled Chromium over CDP, one agent window per
	// REPL session, started on demand. Its input is CDP input into the
	// agent's own tabs, so it needs no desk turn; the allowlist (as the app
	// "Chromium"), confirmations and Stop apply to it.
	var browsers *browser.Manager
	var browserProfile string
	chromiumBin := chromium.Locate(self)
	if bin := chromiumBin; bin != "" {
		profile, err := chromium.DefaultProfileDir()
		if err != nil {
			return err
		}
		// Cookies are encrypted with a key in the "Fennec Safe Storage"
		// Keychain item when the build carries the renamed item (the
		// bundled one); any other build keeps the mock keychain and never
		// touches the user's own "Chromium Safe Storage" item.
		ss, renamed := chromium.SafeStorageOf(bin)
		mock := !renamed
		if !renamed {
			logger.Printf("this Chromium build does not use the %q Keychain item; its cookies use the mock keychain (development only)", chromium.SafeStorageService)
		} else if !loginKeychain() {
			// Without a login keychain (a temporary HOME, a test account)
			// Chromium would raise "Keychain Not Found" on the desktop.
			mock = true
			logger.Printf("no login keychain under this HOME; the browser's cookies use the mock keychain")
		}
		chrome := chromium.New(chromium.Options{
			Bin: bin, ProfileDir: profile, Log: logger.Printf,
			Headless:     os.Getenv(chromium.EnvHeadless) == "1",
			MockKeychain: mock, SafeStorageService: ss.Service,
		})
		browserProfile = profile
		defer chrome.Close()
		browsers = browser.NewManager(browser.Options{
			Browser: chrome, Log: logger.Printf, Gate: pol.Gate,
			Allowed: func() bool { return pol.Allow.Admits(browser.AppName) },
			Import: &browser.ImportConfig{
				ChromeDir:  chromeimport.DefaultChromeDir(),
				Keys:       chromeimport.ChromeKeychain,
				StatusFile: filepath.Join(filepath.Dir(profile), "chrome-import.json"),
			},
		})
		eng.Browsers = browsers
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			browsers.CloseAll(ctx)
		}()
	} else {
		logger.Printf("the bundled Chromium was not found; browser tabs are unavailable")
	}
	fl.Decider, fl.Caption = mdl, flows.ModelCaptioner(mdl)

	// Named sessions keep a REPL runtime across connections, for
	// `fennec-cua js` from a shell.
	named := &mcpserver.Named{}
	mcpInfo := info()
	mcpInfo.Named = named
	d := &daemon.Daemon{
		Paths:       paths,
		Handler:     eng,
		Stopper:     stopper,
		Info:        mcpInfo,
		Log:         logger,
		SocketGroup: cfg.Daemon.SocketGroup,
		Starting: func(ctx context.Context) error {
			if sup != nil {
				if err := sup.Start(ctx); err != nil {
					return fmt.Errorf("cua-driver: %w", err)
				}
			}
			keyEnvs := []string{}
			if cfg.Model.APIKeyEnv != "" {
				keyEnvs = append(keyEnvs, cfg.Model.APIKeyEnv)
			}
			ext, err := extensions.Open(ctx, extensions.Options{
				ConfigPath: cfgPath, Plugins: cfg.Tools.Plugins, ConfigBroken: broken != nil,
				DaemonDir: paths.Dir, KeyEnvs: keyEnvs, Log: logger.Printf,
			})
			if err != nil {
				return fmt.Errorf("extensions: %w", err)
			}
			eng.Ext = ext
			return nil
		},
		Serving: func(ctx context.Context) {
			if broken != nil {
				return
			}
			c, err := models.New(cfg.Model)
			if err != nil {
				logger.Printf("models: %v", err)
				return
			}
			mdl.set(c)
		},
	}
	defer func() {
		if eng.Ext != nil {
			eng.Ext.Close()
		}
		if sup != nil {
			sup.Wait()
		}
	}()

	// The kill hotkey stops every call. Carbon delivers presses on the
	// application event loop, which this process does not run yet.
	// TODO: run a Cocoa run loop on the main thread (Fennec.app's helper)
	// and register with hotkey.Carbon{} there; until then the hotkey is
	// parsed here and the stop answers through the control socket.
	if reg, err := hotkey.Start(cfg.Safety.KillHotkey, nil, func() { stopper.Stop() }); err != nil {
		logger.Printf("kill hotkey: %v", err)
	} else if reg.Message != "" {
		logger.Printf("kill hotkey: %s", reg.Message)
	}

	if httpAddr != "" {
		h, err := mcpserver.NewHTTP(eng, mcpserver.Info{Name: "fennec-cua", Version: engine.Version, Instructions: engine.Instructions, IDPrefix: "mcp-http"}, mcpserver.HTTPConfig{Token: os.Getenv(mcpserver.TokenEnv)})
		if err != nil {
			return err
		}
		go func() {
			if err := h.ListenAndServe(ctx, httpAddr); err != nil && ctx.Err() == nil {
				logger.Printf("MCP over HTTP on %s: %v", httpAddr, err)
			}
		}()
		logger.Printf("serving MCP over HTTP on %s", httpAddr)
	}
	logger.Printf("serving %s", paths.MCP)
	d.Ops = daemonOps(func() daemonInfo {
		di := daemonInfo{PID: os.Getpid(), Executable: self, Version: engine.Version, Driver: bin, Chromium: chromiumBin, Browser: browsers != nil, Sessions: named.Names()}
		if sup != nil && sup.PID() > 0 {
			di.DriverRunning = driverAnswers(paths.Driver)
		}
		return di
	}, quit)
	if browsers != nil {
		for k, op := range browserOps(browsers, browserProfile) {
			d.Ops[k] = op
		}
	}
	return d.Run(ctx)
}

// modelBox is the run tool's model adapter: it calls the model client once
// one is built, and reports each call's usage. Model keys stay in the
// client; scripts see only results.
type modelBox struct {
	mu sync.Mutex
	c  *models.Client
}

func (m *modelBox) set(c *models.Client) {
	m.mu.Lock()
	m.c = c
	m.mu.Unlock()
}

func (m *modelBox) Call(ctx context.Context, op string, args json.RawMessage) (any, protocol.Usage, *protocol.Error) {
	m.mu.Lock()
	c := m.c
	m.mu.Unlock()
	if c == nil {
		return nil, nil, &protocol.Error{Code: protocol.ErrModelUnavailable, Message: "no model endpoint is configured or it is still starting"}
	}
	tally := &models.Tally{}
	ctx = models.WithTally(ctx, tally)
	var (
		out any
		err error
	)
	switch op {
	case "decide":
		a, perr := models.ParseDecideArgs(args)
		if perr != nil {
			return nil, nil, modelErr(perr)
		}
		out, err = c.Decide(ctx, a)
	case "ask":
		a, perr := models.ParseAskArgs(args)
		if perr != nil {
			return nil, nil, modelErr(perr)
		}
		out, err = c.Ask(ctx, a)
	case "ground":
		a, perr := models.ParseGroundArgs(args)
		if perr != nil {
			return nil, nil, modelErr(perr)
		}
		out, err = c.Ground(ctx, a)
	default:
		return nil, nil, &protocol.Error{Code: protocol.ErrInvalid, Message: "model op " + strconv.Quote(op) + " is not decide, ask or ground"}
	}
	if err != nil {
		return nil, tally.Snapshot(), modelErr(err)
	}
	return out, tally.Snapshot(), nil
}

func modelErr(err error) *protocol.Error {
	var me *models.Error
	if errors.As(err, &me) {
		return me.Protocol()
	}
	return &protocol.Error{Code: protocol.ErrInvalid, Message: err.Error()}
}

// screen captures the main display for the plan's model checks.
type screen struct{ c vision.Capturer }

func (s screen) Shot(ctx context.Context) (*plan.Shot, error) {
	c, err := s.c.Desktop(ctx, "plan", 0)
	if err != nil {
		return nil, err
	}
	f, err := c.Pixels()
	if err != nil {
		return nil, err
	}
	return &plan.Shot{Frame: f, Scale: c.DesktopScale()}, nil
}

// visionImager adapts vision.Service to the engine's observe hook.
type visionImager struct {
	s     *vision.Service
	allow policy.Allowlist
}

func (v visionImager) Observe(ctx context.Context, agent string, req protocol.ObserveRequest, base engine.Observer) (*protocol.ObserveResult, error) {
	return maskedObserve(ctx, v.s, v.allow, agent, req, base)
}

// windowID resolves a window ref to its window server id.
func windowID(refs *view.Registry) func(ref string) (uint32, bool) {
	return func(ref string) (uint32, bool) {
		e, err := refs.Lookup(ref)
		if err != nil || !e.Window {
			return 0, false
		}
		return cgID(e.WindowKey)
	}
}

func cgID(key string) (uint32, bool) {
	s, ok := strings.CutPrefix(key, "cg:")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err == nil && n > 0
}

// bridge connects stdio to the daemon's MCP socket, starting the daemon on
// first use; the handshake and status are answered locally until then.
func bridge(ctx context.Context, paths daemon.Paths, self string) error {
	local := &engine.Engine{Stopper: &sched.Stopper{}, Platform: platform{}}
	return mcpserver.Bridge(ctx, os.Stdin, os.Stdout, mcpserver.BridgeConfig{
		Socket:   paths.MCP,
		Embedded: cli.Embedded(),
		Start:    func() (<-chan error, error) { return cli.Start(cli.StartCommand(self)) },
		Local:    local,
		Info:     info(),
	})
}

// do sends one tool call and prints its answer; images go to files.
func do(ctx context.Context, paths daemon.Paths, self string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: fennec-cua do TOOL [JSON | k=v ...]")
	}
	raw, err := cli.ParseDoArgs(args[1:])
	if err != nil {
		return err
	}
	if err := ensureDaemon(ctx, paths, self); err != nil {
		return err
	}
	res, err := cli.Do(ctx, paths.MCP, args[0], raw)
	if err != nil {
		return err
	}
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			fmt.Println(c.Text)
		case "image":
			p, err := cli.SaveCapture("", c.Data)
			if err != nil {
				return err
			}
			fmt.Println("saved capture:", p)
		}
	}
	if res.IsError {
		return errors.New(args[0] + " failed")
	}
	return nil
}

func (m *modelBox) client() (*models.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c == nil {
		return nil, &models.Error{Code: protocol.ErrModelUnavailable, Message: "no model endpoint is configured or it is still starting"}
	}
	return m.c, nil
}

// Ready says whether the client is built.
func (m *modelBox) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c != nil
}

func (m *modelBox) Decide(ctx context.Context, args models.DecideArgs) (*models.Decided, error) {
	c, err := m.client()
	if err != nil {
		return nil, err
	}
	return c.Decide(ctx, args)
}

func (m *modelBox) Ask(ctx context.Context, args models.AskArgs) (*models.Asked, error) {
	c, err := m.client()
	if err != nil {
		return nil, err
	}
	return c.Ask(ctx, args)
}

func (m *modelBox) Ground(ctx context.Context, args models.GroundArgs) (*models.Grounded, error) {
	c, err := m.client()
	if err != nil {
		return nil, err
	}
	return c.Ground(ctx, args)
}
