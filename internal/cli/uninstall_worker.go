package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/agent"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"fennec/internal/runta"
	"fennec/internal/setup"
	"fennec/internal/uninstall"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func runUninstallWorker(path string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	workDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	selfDir, err := filepath.EvalSymlinks(filepath.Dir(self))
	if err != nil {
		return err
	}
	if filepath.Base(path) != "work.json" || workDir != selfDir || !strings.HasPrefix(filepath.Base(workDir), "fennec-uninstall-") {
		return fmt.Errorf("uninstall worker must run from its private staging directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return fmt.Errorf("private uninstall work file required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var work uninstallWork
	if json.Unmarshal(data, &work) != nil {
		return fmt.Errorf("invalid uninstall work")
	}
	record := work.Record
	record.WorkerPID = os.Getpid()
	record.WorkerPath = self
	plan := record.Plan
	dir := filepath.Dir(path)
	receipt := filepath.Join(dir, "result.json")
	marker := uninstall.Marker(plan.Local.Home)
	ownsIntent := false
	report := func(err error) error {
		record.Stage = "failed"
		record.Error = err.Error()
		record.ErrorCode = "uninstall_failed"
		var fault *control.Fault
		var api *runta.APIError
		if errors.As(err, &fault) {
			record.ErrorCode = fault.Code
		} else if errors.As(err, &api) && (api.Status == 401 || api.Status == 403) {
			record.ErrorCode = "login_required"
		}
		_ = uninstall.WriteRecord(receipt, record)
		if ownsIntent {
			_ = uninstall.WriteRecord(marker, record)
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err = uninstall.ConfirmPlan(plan, record.Request, time.Now()); err != nil {
		return report(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return report(err)
	}
	if resolved, e := filepath.EvalSymlinks(home); e == nil {
		home = resolved
	}
	if home != plan.Local.Home {
		return report(fmt.Errorf("uninstall account changed"))
	}
	source := filepath.Join(plan.Local.App, "Contents/MacOS/fen")
	build, err := fileSHA(source)
	if err != nil || build != plan.SourceBuild {
		return report(fmt.Errorf("application changed; review uninstall again"))
	}
	copied, err := fileSHA(self)
	if err != nil || copied != plan.SourceBuild {
		return report(fmt.Errorf("uninstall worker identity mismatch"))
	}
	zdot, xdg := setup.LocalShellDirectories(ctx)
	current, err := uninstall.DiscoverLocal(home, source, agent.ManagedEntryPaths(home), zdot, xdg)
	if err != nil {
		return report(err)
	}
	if !reflect.DeepEqual(current, plan.Local) {
		return report(fmt.Errorf("local installation changed; review uninstall again"))
	}
	if err = validateLocalUninstall(current); err != nil {
		return report(err)
	}
	if err = os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		return report(err)
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(marker), "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return report(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return report(fmt.Errorf("another uninstall is running"))
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if pending, e := uninstall.ReadRecord(marker); e == nil {
		if !matchesPendingUninstall(plan, pending) {
			return report(fmt.Errorf("another uninstall is pending"))
		}
		record.CloudDeleted = pending.CloudDeleted
	} else if !os.IsNotExist(e) {
		return report(e)
	}
	ownsIntent = true
	credentials := account.HelperCredentials{Path: filepath.Join(plan.Local.App, "Contents/MacOS/fennec-credentials")}
	progress := func(stage string) error {
		if stage == "complete" {
			return nil
		} // Publish completion only after the restart guard is removed.
		record.Stage = stage
		record.Error = ""
		record.ErrorCode = ""
		if stage == "disabling_login" && record.Request.DeleteCloud {
			record.CloudDeleted = true
		}
		if err := uninstall.WriteRecord(receipt, record); err != nil {
			return err
		}
		if stage != "complete" {
			return uninstall.WriteRecord(marker, record)
		}
		return nil
	}
	steps := uninstall.Steps{Progress: progress,
		Quiesce: func(ctx context.Context) error {
			var reply any
			step, stop := context.WithTimeout(ctx, 30*time.Second)
			defer stop()
			err := (control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}).Call(step, "uninstall.quiesce", nil, &reply)
			if err != nil {
				var fault *control.Fault
				if !errors.As(err, &fault) || fault.Code != "daemon_unavailable" {
					return fmt.Errorf("cannot stop cloud preparation: %w", err)
				}
				if exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() == nil {
					return fmt.Errorf("Fennec background service is not responding; restart Fennec and retry")
				}
			}
			return nil
		},
		DeleteCloud: func(ctx context.Context, target uninstall.CloudTarget) error {
			if record.CloudDeleted {
				return nil
			}
			token, err := credentials.Read(ctx)
			if err != nil {
				return &control.Fault{Code: "login_required", Message: "Sign in before deleting the cloud computer"}
			}
			return (&runta.Client{Endpoint: "https://api.runta.com", Token: token}).DeleteOwnedShared(ctx, target.ID, target.Owner, target.Revision)
		},
		DisableLogin: func(ctx context.Context) error {
			binary := filepath.Join(plan.Local.App, "Contents/MacOS/Fennec")
			output, err := exec.CommandContext(ctx, binary, "--platform-login-item", "off").Output()
			if err != nil {
				return fmt.Errorf("unable to turn off launch at login")
			}
			var state struct {
				Error   bool `json:"error"`
				Enabled bool `json:"enabled"`
			}
			if json.Unmarshal(output, &state) != nil || state.Error || state.Enabled {
				return fmt.Errorf("launch at login did not turn off")
			}
			return nil
		},
		StopLocal: func(ctx context.Context) error {
			lock, err := acquireControlStartLock(ctx, filepath.Join(home, ".config/fennec/ipc"))
			if err != nil {
				return err
			}
			defer lock.Close()
			if exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() == nil {
				if err := exec.CommandContext(ctx, "/bin/launchctl", "bootout", daemonTarget()).Run(); err != nil {
					return fmt.Errorf("unable to unregister background service")
				}
				if err := waitServiceRemoval(ctx, func() bool { return exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() == nil }); err != nil {
					return err
				}
			}
			return stopUninstallProcesses(ctx, plan.Local.Apps, work.ParentPID)
		},
		RestoreCommands: func(context.Context) error {
			for _, entry := range plan.Local.Entries {
				if err := launcher.UninstallEntry(entry); err != nil {
					return err
				}
			}
			for _, file := range plan.Local.ShellFiles {
				if err := launcher.UninstallShellFile(file); err != nil {
					return err
				}
			}
			return nil
		},
		DeleteCredentials: func(ctx context.Context) error {
			err := credentials.Delete(ctx)
			if errors.Is(err, account.ErrNotFound) {
				return nil
			}
			return err
		},
		RemoveData: func(ctx context.Context) error {
			// Remove the preferences through cfprefsd as well as their on-disk plist.
			_ = exec.CommandContext(ctx, "/usr/bin/defaults", "delete", "com.runta.fennec").Run()
			return plan.Local.RemoveData()
		},
		RemoveApps: func(context.Context) error {
			var others []string
			for _, app := range plan.Local.Apps {
				if app != plan.Local.App {
					others = append(others, app)
				}
			}
			if err := uninstall.RemoveLocalApps(others); err != nil {
				return err
			}
			return uninstall.RemoveLocalApps([]string{plan.Local.App})
		},
	}
	if err = uninstall.Execute(ctx, record.Request, plan.Cloud, steps); err != nil {
		return report(err)
	}
	// App and all active local callers are gone before removing the restart guard.
	if err = os.RemoveAll(filepath.Dir(marker)); err != nil {
		return report(err)
	}
	ownsIntent = false
	record.Stage = "complete"
	record.Error = ""
	record.ErrorCode = ""
	if err = uninstall.WriteRecord(receipt, record); err != nil {
		return err
	}
	_ = os.Remove(path)
	_ = os.Remove(self)
	if work.ParentPID == 0 {
		_ = os.RemoveAll(dir)
	}
	return nil
}

func stopUninstallProcesses(ctx context.Context, apps []string, exclude int) error {
	allowed := map[string]bool{}
	for _, app := range apps {
		for _, name := range []string{"Fennec", "fen", "fennec-daemon"} {
			allowed[filepath.Join(app, "Contents/MacOS", name)] = true
		}
	}
	list := func() (map[int]string, error) {
		output, err := exec.CommandContext(ctx, "/bin/ps", "-ww", "-U", strconv.Itoa(os.Getuid()), "-o", "pid=,comm=").Output()
		if err != nil {
			return nil, err
		}
		return ownedUninstallProcesses(string(output), allowed, exclude, os.Getpid()), nil
	}
	processes, err := list()
	if err != nil {
		return err
	}
	for pid := range processes {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := list()
		if err != nil {
			return err
		}
		if len(current) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			for pid := range current {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
			return fmt.Errorf("some Fennec processes did not exit; retry uninstall")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func ownedUninstallProcesses(output string, allowed map[string]bool, exclude, self int) map[int]string {
	result := map[int]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		split := strings.IndexAny(line, " \t")
		if split < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:split])
		path := strings.TrimSpace(line[split:])
		if err == nil && pid > 1 && pid != exclude && pid != self && allowed[path] {
			result[pid] = path
		}
	}
	return result
}
