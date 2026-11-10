package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fennec/internal/control"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const daemonLabel = "com.runta.fennec.daemon"

func daemonTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) + "/" + daemonLabel }
func daemonPlist(binary, logPath string) []byte {
	escape := func(s string) string { var out bytes.Buffer; xml.EscapeText(&out, []byte(s)); return out.String() }
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + daemonLabel + `</string><key>ProgramArguments</key><array><string>` + escape(binary) + `</string><string>control-daemon</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>5</integer><key>ProcessType</key><string>Background</string><key>StandardErrorPath</key><string>` + escape(logPath) + `</string></dict></plist>`)
}
func ensureControl(ctx context.Context) error {
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	packaged := runtime.GOOS == "darwin" && filepath.Base(binary) == "fennec-daemon" && filepath.Base(filepath.Dir(binary)) == "MacOS" && !strings.HasPrefix(binary, "/tmp/") && !strings.HasPrefix(binary, os.TempDir())
	expected := ""
	if packaged {
		data, readErr := os.ReadFile(filepath.Join(filepath.Dir(binary), "../Resources/control-build-id"))
		if readErr != nil {
			return fmt.Errorf("incomplete Fennec bundle: missing control build identity")
		}
		expected = strings.TrimSpace(string(data))
		if len(expected) != 64 {
			return fmt.Errorf("invalid control build identity")
		}
	}
	ping := func() (string, error) {
		probe, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var result struct {
			BuildID string `json:"build_id"`
		}
		err := (control.Client{Socket: socket}).Call(probe, "daemon.ping", nil, &result)
		return result.BuildID, err
	}
	actual, err := ping()
	if err == nil && (expected == "" || actual == expected) {
		return nil
	}
	var fault *control.Fault
	if err != nil && (!errors.As(err, &fault) || fault.Code != "daemon_unavailable") {
		return err
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("start fennec control-daemon first")
	}
	if !packaged {
		return fmt.Errorf("start the daemon from the installed Fennec bundle; development binaries can run control-daemon in the foreground")
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Dir(socket)); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private control directory required")
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(socket), "control-start.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	actual, err = ping()
	if err == nil && actual == expected {
		return nil
	}
	if err == nil && actual != expected {
		if exec.CommandContext(ctx, "/bin/launchctl", "bootout", daemonTarget()).Run() != nil {
			return fmt.Errorf("unable to replace the previous Fennec service")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	directory := filepath.Join(home, "Library/LaunchAgents")
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	logs := filepath.Join(home, ".config/fennec/logs")
	if err = os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	plist := filepath.Join(directory, daemonLabel+".plist")
	data := daemonPlist(binary, filepath.Join(logs, "daemon.log"))
	// The LaunchAgent is per-user and contains paths only, never credentials.
	file, err := os.CreateTemp(directory, ".fennec-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), plist); err != nil {
		return err
	}
	if err = exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run(); err != nil {
		if err = exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plist).Run(); err != nil {
			if exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() != nil {
				return fmt.Errorf("unable to register Fennec daemon")
			}
		}
	} else if err = exec.CommandContext(ctx, "/bin/launchctl", "kickstart", daemonTarget()).Run(); err != nil {
		return fmt.Errorf("unable to start Fennec daemon")
	}
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		actual, err = ping()
		if err == nil && actual == expected {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon did not become ready: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
func daemonCommand(arguments []string, output io.Writer) error {
	if len(arguments) != 1 {
		return fmt.Errorf("usage: fen daemon start|stop|status")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch arguments[0] {
	case "start":
		return ensureControl(ctx)
	case "stop":
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("stop the foreground control-daemon process")
		}
		if err := exec.CommandContext(ctx, "/bin/launchctl", "bootout", daemonTarget()).Run(); err != nil {
			return fmt.Errorf("unable to stop Fennec daemon")
		}
		return nil
	case "status":
		socket, err := controlSocket()
		if err != nil {
			return err
		}
		var result json.RawMessage
		if err = (control.Client{Socket: socket}).Call(ctx, "daemon.ping", nil, &result); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(result)
	default:
		return fmt.Errorf("unknown daemon command")
	}
}

func waitForBroker(ctx context.Context) error {
	if err := ensureControl(ctx); err != nil {
		return err
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	client := control.Client{Socket: socket}
	started := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state struct {
			Account struct {
				Status string `json:"status"`
			} `json:"account"`
			Runtime struct {
				Stage string `json:"stage"`
			} `json:"runtime"`
			Services struct {
				Ready bool `json:"broker_ready"`
			} `json:"services"`
		}
		if err := client.Call(ctx, "state.get", nil, &state); err != nil {
			return err
		}
		if state.Services.Ready {
			return nil
		}
		if state.Account.Status == "signed_out" && time.Since(started) > 200*time.Millisecond {
			return fmt.Errorf("sign in with fen login first")
		}
		if state.Runtime.Stage == "failed" {
			return fmt.Errorf("runtime preparation failed; inspect fen status and use fen runtime retry")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
