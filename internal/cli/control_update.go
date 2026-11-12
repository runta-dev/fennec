package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/installation"
	"fennec/internal/updater"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	activeUpdaterMu sync.Mutex
	activeUpdater   *updater.Service
)

func startControlUpdater(ctx context.Context, _ *controlplane.Controller) {
	activeUpdaterMu.Lock()
	svc := activeUpdater
	activeUpdaterMu.Unlock()
	if svc != nil {
		svc.Start(ctx)
	}
}

func installUpdateControl(home, executable string, settings *desktopSettings, network *connectivity.Monitor, controller *controlplane.Controller) *updater.Service {
	bundlePath := updater.BundlePath(os.Args[0])
	if bundlePath == "" {
		bundlePath = updater.BundlePath(executable)
	}
	svc := updater.NewService(updater.Config{
		Bundle:  bundlePath,
		Network: network,
		AutoEnabled: func() bool {
			prefs, ok := settings.snapshot().(desktopPreferences)
			return !ok || !prefs.NoAutoUpdate
		},
		ApplyUpdate: func(ctx context.Context, stagedApp, destinationBundle string) error {
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("bundle installation requires macOS")
			}
			ops := installation.SystemOperations()
			baseOpenUI := ops.OpenUI
			ops.OpenUI = func(openCtx context.Context, appPath string) error {
				return relaunchAppWithRetry(openCtx, home, appPath, baseOpenUI)
			}
			res, err := installation.Install(ctx, stagedApp, destinationBundle, true, ops)
			if err != nil {
				return err
			}
			if !res.UIRestarted {
				scheduleDetachedDaemonRollover(destinationBundle)
			}
			return nil
		},
	})

	activeUpdaterMu.Lock()
	activeUpdater = svc
	activeUpdaterMu.Unlock()

	controller.UpdateState = func() any {
		return svc.Snapshot(context.Background(), false)
	}
	controller.Additional["update.status"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req struct {
			Fresh bool `json:"fresh"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &req) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid update status request"}
		}
		return svc.Snapshot(ctx, req.Fresh), nil
	}
	controller.Additional["update.check"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req struct {
			Download *bool `json:"download"`
		}
		if len(raw) > 0 && json.Unmarshal(raw, &req) != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid update check request"}
		}
		download := true
		if req.Download != nil {
			download = *req.Download
		}
		return svc.Check(ctx, download), nil
	}
	controller.Additional["update.download"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		return svc.Download(ctx), nil
	}
	controller.Additional["update.install"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		snap, err := svc.Install(ctx)
		if err != nil {
			return snap, &control.Fault{Code: "update_failed", Message: err.Error(), Retryable: snap.Retry}
		}
		return snap, nil
	}
	return svc
}

func relaunchAppWithRetry(ctx context.Context, home, appPath string, openOnce func(context.Context, string) error) error {
	waits := []time.Duration{300 * time.Millisecond, 600 * time.Millisecond, time.Second, 2 * time.Second}
	var lastErr error
	for i := 0; i <= len(waits); i++ {
		if i > 0 {
			timer := time.NewTimer(waits[i-1])
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if openOnce != nil {
			if err := openOnce(ctx, appPath); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
	}
	exe := filepath.Join(appPath, "Contents/MacOS/Fennec")
	if info, err := os.Stat(exe); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
		logDir := filepath.Join(home, ".config/fennec/logs")
		_ = os.MkdirAll(logDir, 0700)
		cmd := exec.Command(exe, "--installed-application", appPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			return nil
		}
	}
	return lastErr
}

func scheduleDetachedDaemonRollover(bundlePath string) {
	fenBin := filepath.Join(bundlePath, "Contents/MacOS/fen")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "sleep 0.3; exec " + quote(fenBin) + " start >/dev/null 2>&1"
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
