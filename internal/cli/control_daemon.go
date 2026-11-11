package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fennec/internal/control"
	"fennec/internal/installation"
	"fennec/internal/launchprofile"
	"fennec/internal/uninstall"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const daemonLabel = "com.runta.fennec.daemon"

func daemonTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) + "/" + daemonLabel }
func daemonPlist(binary, logPath string) []byte {
	escape := func(s string) string { var out bytes.Buffer; xml.EscapeText(&out, []byte(s)); return out.String() }
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + daemonLabel + `</string><key>ProgramArguments</key><array><string>` + escape(binary) + `</string><string>control-daemon</string></array><key>RunAtLoad</key><false/><key>KeepAlive</key><dict><key>AfterInitialDemand</key><true/><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>5</integer><key>ProcessType</key><string>Background</string><key>StandardErrorPath</key><string>` + escape(logPath) + `</string></dict></plist>`)
}
func ensureControl(ctx context.Context) error {
	defer launchprofile.Begin("daemon.ensure")()
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" && filepath.Base(filepath.Dir(binary)) == "MacOS" {
		location, err := installation.Inspect(binary, home)
		if err != nil {
			return err
		}
		if location.Required {
			return fmt.Errorf("install Fennec in Applications before starting it")
		}
		binary = filepath.Join(location.Application, "Contents/MacOS/fen")
	}
	packaged := runtime.GOOS == "darwin" && (filepath.Base(binary) == "fen" || filepath.Base(binary) == "fennec-daemon") && filepath.Base(filepath.Dir(binary)) == "MacOS" && !strings.HasPrefix(binary, "/tmp/") && !strings.HasPrefix(binary, os.TempDir())
	plist := filepath.Join(home, "Library/LaunchAgents", daemonLabel+".plist")
	desiredRegistration := daemonPlist(binary, filepath.Join(home, ".config/fennec/logs/daemon.log"))
	actualExecutable := ""
	registrationMatches := func() bool {
		data, err := os.ReadFile(plist)
		return err == nil && bytes.Equal(data, desiredRegistration)
	}
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
			BuildID    string `json:"build_id"`
			Executable string `json:"executable"`
		}
		err := (control.Client{Socket: socket}).Call(probe, "daemon.ping", nil, &result)
		actualExecutable = result.Executable
		return result.BuildID, err
	}
	actual, err := ping()
	if err == nil && (expected == "" || reusableDaemon(expected, actual, binary, actualExecutable, registrationMatches())) {
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
	if uninstallRunning(home) {
		return fmt.Errorf("Fennec is uninstalling")
	}
	lock, err := acquireControlStartLock(ctx, filepath.Dir(socket))
	if err != nil {
		return err
	}
	defer lock.Close()
	actual, err = ping()
	if err == nil && reusableDaemon(expected, actual, binary, actualExecutable, registrationMatches()) {
		return nil
	}
	if uninstallRunning(home) {
		return fmt.Errorf("Fennec is uninstalling")
	}
	// A live job can still point to an old location, or be crash-looping without
	// answering ping. Replace its registration before bootstrapping the new path.
	if exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() == nil {
		if exec.CommandContext(ctx, "/bin/launchctl", "bootout", daemonTarget()).Run() != nil {
			return fmt.Errorf("unable to replace the previous Fennec service")
		}
		if err := waitServiceRemoval(ctx, func() bool { return exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() == nil }); err != nil {
			return err
		}
	}
	directory := filepath.Join(home, "Library/LaunchAgents")
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	logs := filepath.Join(home, ".config/fennec/logs")
	if err = os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	data := desiredRegistration
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
	launchctlDone := launchprofile.Begin("daemon.launchctl")
	if err = exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run(); err != nil {
		if err = exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plist).Run(); err != nil {
			if exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() != nil {
				return fmt.Errorf("unable to register Fennec daemon")
			}
		}
	}
	if err = exec.CommandContext(ctx, "/bin/launchctl", "kickstart", daemonTarget()).Run(); err != nil {
		return fmt.Errorf("unable to start Fennec daemon")
	}
	launchctlDone()
	defer launchprofile.Begin("daemon.wait_ready")()
	return waitControlReady(ctx, expected, func() (string, error) {
		id, err := ping()
		if err == nil && !reusableDaemon(expected, id, binary, actualExecutable, registrationMatches()) {
			return "", fmt.Errorf("waiting for the installed Fennec service")
		}
		return id, err
	},
		func() bool { return exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() == nil },
		func() error {
			if err := exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plist).Run(); err != nil && exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() != nil {
				return err
			}
			return exec.CommandContext(ctx, "/bin/launchctl", "kickstart", daemonTarget()).Run()
		})
}

func reusableDaemon(expected, actual, binary, running string, registrationMatches bool) bool {
	if expected == "" || actual != expected || running == "" || !registrationMatches {
		return false
	}
	canonical := func(path string) string {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			return resolved
		}
		return filepath.Clean(path)
	}
	return canonical(binary) == canonical(running)
}

// A concurrent reconnect can kickstart a registration that is still retiring
// during bootout. Its disappearance must trigger registration again while the
// caller still owns the startup lock, rather than stranding every other client.
func waitControlReady(ctx context.Context, expected string, ping func() (string, error), registered func() bool, bootstrap func() error) error {
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		actual, err := ping()
		if err == nil && actual == expected {
			return nil
		}
		if ctx.Err() == nil && !registered() {
			if err := bootstrap(); err != nil && !registered() {
				return fmt.Errorf("unable to register Fennec daemon: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon did not become ready: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// bootout may acknowledge before launchd drops the old job registration. Never
// kickstart that retiring job: it would disappear after the apparent success.
func waitServiceRemoval(ctx context.Context, present func() bool) error {
	for present() {
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("previous service did not unregister: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return ctx.Err()
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
		socket, err := controlSocket()
		if err != nil {
			return err
		}
		lock, err := acquireControlStartLock(ctx, filepath.Dir(socket))
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := exec.CommandContext(ctx, "/bin/launchctl", "bootout", daemonTarget()).Run(); err != nil {
			return fmt.Errorf("unable to stop Fennec daemon")
		}
		return waitServiceRemoval(ctx, func() bool {
			return exec.CommandContext(ctx, "/bin/launchctl", "print", daemonTarget()).Run() == nil
		})
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
	return waitForAgentBroker(ctx, "")
}

func waitForAgentBroker(ctx context.Context, agent string) error {
	defer launchprofile.Begin("broker.wait_ready")()
	if home, err := os.UserHomeDir(); err == nil && uninstall.Pending(home) {
		return fmt.Errorf("Fennec uninstall is pending; finish it before starting a cloud agent")
	}
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
				Ready  bool     `json:"broker_ready"`
				Active []string `json:"active"`
			} `json:"services"`
		}
		if err := client.Call(ctx, "state.get", nil, &state); err != nil {
			return err
		}
		if state.Services.Ready && brokerAgentReady(agent, state.Services.Active) {
			return nil
		}
		if agent != "" && state.Services.Ready && state.Runtime.Stage == "ready" {
			return fmt.Errorf("%s is not configured to run in the cloud", agent)
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

func brokerAgentReady(agent string, active []string) bool {
	if agent == "" {
		return true
	}
	for _, id := range active {
		if id == agent {
			return true
		}
	}
	return false
}
