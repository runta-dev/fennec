package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/applog"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/runta"
	"fennec/internal/updater"
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// installLogExport adds logs.export: it writes a zip of the last seven days of
// local logs plus a status summary into ~/.config/fennec/exports and returns
// its path. The Settings pages then ask the user where to save it (THI-145).
func installLogExport(home, executable string, controller *controlplane.Controller) {
	controller.Additional["logs.export"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		now := time.Now()
		log.Printf("exporting logs")
		extra := []applog.Entry{{Name: "system.txt", Data: logSummary(ctx, home, executable, controller, now)}}
		if runtime.GOOS == "darwin" {
			extra = append(extra, applog.Entry{Name: "macos-unified.log", Data: unifiedLog(ctx)})
		}
		path, err := applog.WriteExport(home, now, extra)
		if err != nil {
			log.Printf("log export failed: %v", err)
			return nil, fmt.Errorf("Could not export logs")
		}
		return map[string]string{"path": path, "name": "fennec-logs-" + now.Format("20060102-150405") + ".zip"}, nil
	}
}

func logSummary(ctx context.Context, home, executable string, controller *controlplane.Controller, now time.Time) []byte {
	app := updater.BundlePath(executable)
	zone, offset := now.Zone()
	pairs := [][2]string{
		{"exported_at", now.Format(time.RFC3339) + " (" + now.UTC().Format(time.RFC3339) + ")"},
		{"time_zone", fmt.Sprintf("%s %+d", zone, offset/3600)},
		{"app_version", updater.BundleVersion(app)},
		{"app_path", app},
		{"build_id", control.BuildID()},
		{"control_protocol", fmt.Sprint(control.Version)},
		{"os", runtime.GOOS + "/" + runtime.GOARCH},
		{"os_version", commandLine(ctx, "sw_vers", "-productVersion")},
		{"os_build", commandLine(ctx, "sw_vers", "-buildVersion")},
		{"environment", runta.ActiveEnvironment(home).Name},
	}
	if controller.Account != nil {
		account := controller.Account.Snapshot()
		pairs = append(pairs, [2]string{"account_status", account.Status}, [2]string{"account_error", account.Error})
	}
	state := controller.RuntimeStatus()
	pairs = append(pairs,
		[2]string{"runtime_stage", state.Stage},
		[2]string{"runtime_error", state.Error},
		[2]string{"runtime_offline", fmt.Sprint(state.Offline)},
		[2]string{"runtime_running", fmt.Sprint(state.Running)},
		[2]string{"runtime_id", state.RuntimeID},
	)
	// Catalog events are large and carry nothing about failures.
	for _, kind := range []string{"runtime_progress", "connector_ready", "shared_services_ready"} {
		if event, ok := state.Events[kind]; ok {
			data, _ := json.Marshal(event)
			pairs = append(pairs, [2]string{"event_" + kind, string(data)})
		}
	}
	if free := diskFree(home); free != "" {
		pairs = append(pairs, [2]string{"disk_free", free})
	}
	return applog.SummaryLines(pairs)
}

func commandLine(ctx context.Context, name string, args ...string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func diskFree(home string) string {
	out := commandLine(context.Background(), "df", "-h", home)
	if lines := strings.Split(out, "\n"); len(lines) == 2 {
		return strings.Join(strings.Fields(lines[1]), " ")
	}
	return ""
}

// unifiedLog collects the last day of the native app's own messages
// (NSLog from Fennec and the Desktop window), which never reach logs/.
func unifiedLog(ctx context.Context) []byte {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/log", "show", "--last", "1d", "--style", "compact", "--predicate", `process == "Fennec" OR process == "fennec-desktop"`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return []byte(err.Error() + "\n")
	}
	if err := cmd.Start(); err != nil {
		return []byte(err.Error() + "\n")
	}
	data, _ := io.ReadAll(io.LimitReader(stdout, 8<<20))
	// Past the limit the remaining output is not needed.
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
	_ = cmd.Wait()
	if ctx.Err() != nil {
		data = append(data, []byte("\n[stopped after 15 s]\n")...)
	}
	return data
}
