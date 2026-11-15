package cli

import (
	"context"
	"errors"
	"fennec/internal/control"
	"fennec/internal/installation"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

func restartAlreadySatisfied(retiringPID, actualPID int) bool {
	return retiringPID == 0 || retiringPID > 0 && actualPID > 0 && retiringPID != actualPID
}

// The scheduler exits before the UI does. launchd owns the worker independently
// of both retiring processes, so bootout cannot kill the restart half way through.
func appRestartCommand(arguments []string, worker bool) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("application restart requires macOS")
	}
	count := 1
	if worker {
		count = 2
	}
	if len(arguments) != count {
		return fmt.Errorf("application restart requires the retiring UI process")
	}
	parent, err := strconv.Atoi(arguments[0])
	if err != nil || parent <= 1 {
		return fmt.Errorf("invalid application process")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	location, err := installation.Inspect(binary, home)
	if err != nil {
		return err
	}
	if location.Required || location.Application == "" {
		return fmt.Errorf("restart requires the installed Fennec application")
	}
	binary = filepath.Join(location.Application, "Contents/MacOS/fen")
	label := "com.runta.fennec.restart." + strconv.Itoa(parent)
	if worker {
		retiringPID, err := strconv.Atoi(arguments[1])
		if err != nil || retiringPID == 0 || retiringPID < -1 {
			return fmt.Errorf("invalid retiring daemon process")
		}
		defer func() {
			// Remove this completed one-shot registration, never a daemon or
			// transport job. launchd may terminate us as part of removal.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = exec.CommandContext(ctx, "/bin/launchctl", "remove", label).Run()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		err = completeAppRestart(ctx,
			func(ctx context.Context) error {
				ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				return waitApplicationExit(ctx, parent)
			},
			func(ctx context.Context) error { return ensureControlReplacing(ctx, retiringPID) },
			func(ctx context.Context) error {
				return exec.CommandContext(ctx, "/usr/bin/open", "-n", location.Application, "--args", "--installed-application", location.Application).Run()
			})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	// Validate the private directory before launchd opens the worker's log.
	lock, err := acquireControlStartLock(ctx, filepath.Dir(socket))
	if err != nil {
		return err
	}
	defer lock.Close()
	var current struct {
		PID int `json:"pid"`
	}
	probe, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	err = (control.Client{Socket: socket}).Call(probe, "daemon.ping", nil, &current)
	stop()
	if err != nil || current.PID <= 1 {
		current.PID = -1
	}
	// Per-UI identity coalesces repeated requests without replacing a running
	// restart helper. A newly opened UI gets a different process identity.
	target := "gui/" + strconv.Itoa(os.Getuid()) + "/" + label
	if exec.CommandContext(ctx, "/bin/launchctl", "print", target).Run() == nil {
		return nil
	}
	log := filepath.Join(filepath.Dir(socket), "app-restart.log")
	return exec.CommandContext(ctx, "/bin/launchctl", "submit", "-l", label, "-o", log, "-e", log, "--", binary, "app-restart-worker", strconv.Itoa(parent), strconv.Itoa(current.PID)).Run()
}

func waitApplicationExit(ctx context.Context, pid int) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		} else if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("application did not exit: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func completeAppRestart(ctx context.Context, wait, restart, open func(context.Context) error) error {
	if err := wait(ctx); err != nil {
		return err // A UI that refused to exit must not trigger a second app.
	}
	err := restart(ctx)
	// Reopen even after startup failure. The normal UI startup can recover and
	// show current state; never leave the user with a silently vanished app.
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(err, open(openCtx))
}
