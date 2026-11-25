package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/applog"
	"fennec/internal/audit"
	"fennec/internal/cloudstate"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/installation"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	"fennec/internal/sandbox"
	"fennec/internal/sshcarrier"
	"fennec/internal/stackdump"
	"fennec/internal/terminalstream"
	"fennec/internal/updater"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// Daily log in logs/, kept for seven days and exported from Settings.
	logs := applog.Install(home)
	defer logs.Close()
	log.Printf("fennec daemon starting: pid=%d build=%.12s version=%s executable=%s os=%s/%s", os.Getpid(), control.BuildID(), updater.BundleVersion(updater.BundlePath(executable)), executable, runtime.GOOS, runtime.GOARCH)
	// SIGUSR1 writes every goroutine's stack to logs/ for hangs that log nothing.
	stackdump.Install(ctx, filepath.Join(home, ".config/fennec/logs"))
	manager, services, controller := newControlApplication(ctx, home, executable)
	defer func() { cancel(); services.close() }()
	// A stop request (launchctl bootout when Fennec is upgraded or restarted)
	// first lets ChatGPT's requests in flight finish over the still-open cloud
	// link, then stops everything; a second signal stops at once.
	go func() {
		select {
		case <-signals.Done():
			stopSignals()
			services.chatgpt.drain(chatgptDrainLimit)
			cancel()
		case <-ctx.Done():
		}
	}()
	// Acquire the control socket before any Keychain access so competing starts
	// cannot create duplicate system authorization prompts.
	return control.Serve(ctx, socket, controller.Handlers(), func() {
		if err := refreshInstalledLaunchers(home, executable); err != nil {
			_ = writePublicJSON(filepath.Join(home, ".config/fennec/diagnostics/launchers.json"), map[string]string{"error": err.Error()})
		}
		if app := installedBundleForTrashWatch(os.Args[0], home); app != "" {
			// The bundle an update replaced is kept while CLI clients still run
			// from it: pruned now, and hourly until those clients have exited.
			go func() {
				installation.PruneStaging(filepath.Dir(app))
				ticker := time.NewTicker(time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						installation.PruneStaging(filepath.Dir(app))
					}
				}
			}()
		}
		if terminalIntegrationWanted(home) {
			reconcileTerminalIntegrationSoon(home, executable)
		}
		controller.Start(ctx)
		if controller.UpdateState != nil {
			startControlUpdater(ctx, controller)
		}
		startTrashWatcher(ctx, cancel, home, os.Args[0])
		go services.chatgpt.run(ctx)
		go func() {
			defer launchprofile.Begin("account.restore")()
			restore, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			restorePrincipal(restore, home, manager)
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
	controller := &controlplane.Controller{Account: manager, Connectivity: network, Prepare: services.prepare, Session: services.session, Services: services.snapshot, CloudUnreachable: services.cloudUnreachable}
	controller.Additional = services.handlers(controller)
	// runRuntimePresence calls this when the cloud computer was deleted outside
	// Fennec, to tear down services bound to it and prepare a replacement.
	services.onRuntimeDeleted = func(ctx context.Context, epoch uint64, owner, runtimeID string) error {
		return controller.ReplaceDeletedRuntime(ctx, epoch, runtimeID, func(ctx context.Context) error {
			return services.quiesceCloud(ctx, owner, runtimeID, epoch)
		})
	}
	controller.Additional["transfer.open"] = services.openTransfer
	controller.Additional["hc.open"] = services.openHC
	for method, handler := range newBYOCControl(home, manager).handlers() {
		controller.Additional[method] = handler
	}
	controller.Additional["desktop.session.seen"] = services.desktopSessionSeen
	services.chatgpt = newChatGPTBridge(home, services)
	for method, handler := range services.terminalCoverageHandlers() {
		controller.Additional[method] = handler
	}
	for method, handler := range services.chatgpt.handlers() {
		controller.Additional[method] = handler
	}
	for name, handler := range services.hookHandlers() {
		controller.Additional[name] = handler
	}
	for name, handler := range newArchiveControl(services).handlers() {
		controller.Additional[name] = handler
	}
	installBrowserTerminal(ctx, services, controller)
	installMagpiePanel(ctx, services, controller, settings)
	installPreviewServer(ctx, services)
	installBoard(ctx, services, controller)
	installDesktopWindowSettings(home, controller)
	installLogExport(home, executable, controller)
	controller.Settings = func() any {
		auditRes, _ := auditStore.Query(audit.QueryOptions{Limit: 1})
		return desktopSettingsResponse(home, settings.snapshot().(desktopPreferences), auditRes.Summary)
	}
	controller.Additional["audit.list"] = func(_ context.Context, raw json.RawMessage) (any, error) {
		var opts audit.QueryOptions
		if len(raw) > 0 && json.Unmarshal(raw, &opts) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid audit query"}
		}
		return queryAudit(home, auditStore, opts)
	}
	controller.Additional["audit.allow"] = allowAudit(home, auditStore)
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
				if err := sshcarrier.Revoke(home); err != nil {
					return err
				}
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
