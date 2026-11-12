package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/audit"
	"fennec/internal/cloudstate"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	"fennec/internal/sandbox"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

func controlSocket() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config/fennec/ipc/control.sock"), nil
}

func describeControl(output io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	_, _, controller := newControlApplication(context.Background(), home, executable)
	methods := []string{}
	for method := range controller.Handlers() {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return json.NewEncoder(output).Encode(map[string]any{"version": control.Version, "terminal_protocol": terminalstream.ProtocolVersion, "methods": methods})
}

func serveControl(arguments []string) error {
	if len(arguments) != 0 {
		return errors.New("control-daemon takes no arguments")
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	manager, services, controller := newControlApplication(ctx, home, executable)
	defer func() { cancel(); services.close() }()
	// Acquire the control socket before any Keychain access so competing starts
	// cannot create duplicate system authorization prompts.
	return control.Serve(ctx, socket, controller.Handlers(), func() {
		controller.Start(ctx)
		if controller.UpdateState != nil {
			startControlUpdater(ctx, controller)
		}
		startTrashWatcher(ctx, cancel, home, os.Args[0])
		go func() {
			defer launchprofile.Begin("account.restore")()
			restore, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			_ = manager.Restore(restore)
		}()
	})
}

func callControl(arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) != 1 {
		return fmt.Errorf("usage: fen control <method> (JSON parameters on stdin)")
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	params := json.RawMessage(`{}`)
	if input != nil {
		decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
		if err := decoder.Decode(&params); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("invalid JSON parameters")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	var result json.RawMessage
	if err := (control.Client{Socket: socket}).Call(ctx, arguments[0], params, &result); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(result)
}

func newControlApplication(ctx context.Context, home, executable string) (*account.Manager, *daemonServices, *controlplane.Controller) {
	settings := newDesktopSettings(home)
	initialEnv := runta.ActiveEnvironment(home)
	var baseCreds account.Credentials = account.HelperCredentials{Path: filepath.Join(filepath.Dir(executable), "fennec-credentials")}
	if customFile := os.Getenv("FENNEC_CREDENTIALS_FILE"); customFile != "" && filepath.IsAbs(customFile) {
		baseCreds = account.FileCredentials{Path: customFile}
	}
	credentials := &account.ScopedCredentials{
		Base: baseCreds,
		Scope: func() string {
			return runta.ActiveEnvironment(home).API
		},
	}
	manager := account.New(ctx, credentials, initialEnv.API)
	network := connectivity.NewMonitor(nil, 2*time.Second)
	services := newDaemonServices(home, manager, network)
	auditStore := audit.NewStore(home)
	auditStore.Prune()
	services.auditStore = auditStore
	services.sandboxPolicy = func() sandbox.Policy {
		prefs := settings.snapshot().(desktopPreferences)
		return effectiveSandboxPolicy(home, prefs)
	}
	controller := &controlplane.Controller{Account: manager, Connectivity: network, Prepare: services.prepare, Session: services.session, Services: services.snapshot}
	controller.Additional = services.handlers(controller)
	for name, handler := range services.hookHandlers() {
		controller.Additional[name] = handler
	}
	installBrowserTerminal(ctx, services, controller)
	controller.Settings = func() any {
		prefs := settings.snapshot().(desktopPreferences)
		env := runta.ActiveEnvironment(home)
		preset := effectivePermissionPreset(prefs)
		effSandbox := effectiveSandboxPolicy(home, prefs)
		auditRes, _ := auditStore.Query(audit.QueryOptions{Limit: 1})
		customRoots := prefs.SandboxRoots
		if customRoots == nil {
			customRoots = []string{}
		}
		return map[string]any{
			"revision":                prefs.Revision,
			"magpie":                  prefs.Magpie,
			"no_auto_update":          prefs.NoAutoUpdate,
			"update_skip":             prefs.UpdateSkip,
			"environment":             env.Name,
			"api_endpoint":            env.API,
			"dashboard_url":           env.Dashboard,
			"ssh_host":                env.SSHHost,
			"ssh_nodelay":             !prefs.NoSSHNoDelay,
			"no_ssh_nodelay":          prefs.NoSSHNoDelay,
			"permission_mode":         preset,
			"sandbox_mode":            effSandbox.Mode,
			"sandbox_network":         effSandbox.Network,
			"sandbox_roots":           customRoots,
			"effective_sandbox_roots": effSandbox.WritableRoots,
			"audit_summary":           auditRes.Summary,
			"theme":                   prefs.Theme,
			"language":                prefs.Language,
			"initialized":             prefs.Initialized,
		}
	}
	controller.Additional["audit.list"] = func(_ context.Context, raw json.RawMessage) (any, error) {
		var opts audit.QueryOptions
		if len(raw) > 0 && json.Unmarshal(raw, &opts) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid audit query"}
		}
		return auditStore.Query(opts)
	}
	controller.Additional["audit.clear"] = func(context.Context, json.RawMessage) (any, error) {
		if err := auditStore.Clear(); err != nil {
			return nil, err
		}
		return auditStore.Query(audit.QueryOptions{Limit: 1})
	}
	controller.Additional["settings.get"] = func(context.Context, json.RawMessage) (any, error) { return controller.Settings(), nil }
	controller.Additional["settings.set"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		prevPreset := effectivePermissionPreset(settings.snapshot().(desktopPreferences))
		_, err := settings.update(ctx, raw, func() error {
			env := runta.ActiveEnvironment(home)
			if manager.Endpoint() != env.API {
				services.close()
				_ = manager.SetEndpoint(ctx, env.API, env.Dashboard)
			}
			services.mu.Lock()
			services.ready = map[string]bool{}
			services.reconcileSelectionLocked()
			services.mu.Unlock()
			if _, _, _, ok := manager.Session(); ok {
				if _, err := controller.Reconcile(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		nextPreset := effectivePermissionPreset(settings.snapshot().(desktopPreferences))
		if nextPreset == sandbox.PresetAsk && prevPreset != sandbox.PresetAsk {
			_ = os.Remove(filepath.Join(home, ".config/fennec/ssh/trusted_devices.json"))
			_, _ = services.decide(ctx, json.RawMessage(`{"action":"clear_trust"}`))
		}
		return controller.Settings(), nil
	}
	controller.Additional["settings.import"] = settings.set(true)
	for _, operation := range []string{"status", "on", "off", "settings"} {
		controller.Additional["login-item."+operation] = func(ctx context.Context, _ json.RawMessage) (any, error) { return platformLoginItem(ctx, operation) }
	}
	controller.Additional["login-item.initialize"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		value, err := platformLoginItem(ctx, "status")
		if err != nil {
			return nil, err
		}
		state, _ := value.(map[string]any)
		if state["supported"] == true && state["initialized"] != true {
			if state["enabled"] == true {
				return platformLoginItem(ctx, "mark-initialized")
			}
			return platformLoginItem(ctx, "on")
		}
		return value, nil
	}
	installUninstallControl(home, executable, manager, services, controller)
	installUpdateControl(home, executable, settings, network, controller)
	installCloudControl(home, services, controller, func(token string) cloudstate.Backend {
		return runta.NewClient(home, token)
	})
	return manager, services, controller
}
