package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launchprofile"
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
	manager := account.New(ctx, account.HelperCredentials{Path: filepath.Join(filepath.Dir(executable), "fennec-credentials")}, "https://api.runta.com")
	services := newDaemonServices(home, manager)
	controller := &controlplane.Controller{Account: manager, Prepare: services.prepare, Session: services.session, Services: services.snapshot}
	controller.Additional = services.handlers(controller)
	installBrowserTerminal(ctx, services, controller)
	settings := newDesktopSettings(home)
	controller.Settings = settings.snapshot
	controller.Additional["settings.get"] = func(context.Context, json.RawMessage) (any, error) { return settings.snapshot(), nil }
	controller.Additional["settings.set"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		before := settings.snapshot().(desktopPreferences).Magpie
		value, err := settings.set(false)(ctx, raw)
		if err != nil {
			return nil, err
		}
		if before != value.(desktopPreferences).Magpie {
			services.mu.Lock()
			services.ready = map[string]bool{}
			services.reconcileSelectionLocked()
			services.mu.Unlock()
			if _, _, _, ok := manager.Session(); ok {
				if _, err := controller.Reconcile(ctx); err != nil {
					return nil, err
				}
			}
		}
		return value, nil
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
	return manager, services, controller
}
