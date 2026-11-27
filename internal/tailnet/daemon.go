package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Hostname is the cloud computer's name in the tailnet. Every Runta cloud
// computer's OS hostname is "runta", which says nothing in a device list.
const Hostname = "fennec-cloud"

const serviceName = "fennec-tailscaled.service"

// job is the sign-in worker's progress, shared through job.json because the
// worker outlives the operation that started it.
type job struct {
	Stage   string `json:"stage"` // installing, starting, login, done, failed
	Error   string `json:"error,omitempty"`
	Detail  string `json:"detail,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Started int64  `json:"started"`
}

func (j job) active() bool { return j.Stage != "" && j.Stage != "done" && j.Stage != "failed" }

func readJob(p paths) job {
	var value job
	data, err := os.ReadFile(p.job())
	if err != nil || json.Unmarshal(data, &value) != nil {
		return job{}
	}
	if value.active() && !alive(value.PID) {
		value.Stage, value.Error = "failed", ErrStartFailed
		if value.Detail == "" {
			value.Detail = "sign-in was interrupted"
		}
	}
	return value
}

func writeJob(p paths, value job) error {
	if err := os.MkdirAll(p.state(), 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(value)
	tmp := p.job() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p.job())
}

func alive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// cli runs the bundled tailscale CLI against Fennec's tailscaled.
func cli(ctx context.Context, p paths, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, p.tailscale(), append([]string{"--socket=" + p.socket()}, args...)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return out, fmt.Errorf("%w: %s", err, lastLine(text))
		}
	}
	return out, err
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func daemonArgs(p paths) []string {
	return []string{"--tun=userspace-networking", "--statedir=" + p.state(), "--socket=" + p.socket(), "--port=0"}
}

// systemd reports whether systemd runs this computer; tests replace it.
var systemd = func() bool {
	info, err := os.Stat("/run/systemd/system")
	return err == nil && info.IsDir()
}

func systemctl(ctx context.Context, args ...string) error {
	if os.Geteuid() != 0 {
		args = append([]string{"--user"}, args...)
	}
	return exec.CommandContext(ctx, "systemctl", args...).Run()
}

func unitPath(home string) string {
	if os.Geteuid() != 0 {
		return filepath.Join(home, ".config/systemd/user", serviceName)
	}
	return filepath.Join("/etc/systemd/system", serviceName)
}

func unit(p paths) []byte {
	quoted := []string{strconv.Quote(p.tailscaled())}
	for _, arg := range daemonArgs(p) {
		quoted = append(quoted, strconv.Quote(arg))
	}
	target := "multi-user.target"
	if os.Geteuid() != 0 {
		target = "default.target"
	}
	return []byte("[Unit]\nDescription=Tailscale for Fennec\nAfter=network-online.target\nWants=network-online.target\n[Service]\nType=simple\nExecStart=" + strings.Join(quoted, " ") + "\nRestart=on-failure\nRestartSec=5\n[Install]\nWantedBy=" + target + "\n")
}

// ensureDaemon starts tailscaled (a systemd unit when systemd runs the
// computer, otherwise a detached process) and waits until it answers.
func ensureDaemon(ctx context.Context, p paths) error {
	if err := os.MkdirAll(p.state(), 0700); err != nil {
		return err
	}
	if systemd() {
		path := unitPath(p.home)
		want := unit(p)
		old, _ := os.ReadFile(path)
		if !bytes.Equal(old, want) {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(path+".tmp", want, 0644); err != nil {
				return err
			}
			if err := os.Rename(path+".tmp", path); err != nil {
				return err
			}
			if err := systemctl(ctx, "daemon-reload"); err != nil {
				return err
			}
			if err := systemctl(ctx, "enable", serviceName); err != nil {
				return err
			}
			if err := systemctl(ctx, "restart", serviceName); err != nil {
				return err
			}
		} else if systemctl(ctx, "is-active", "--quiet", serviceName) != nil {
			if err := systemctl(ctx, "start", serviceName); err != nil {
				return err
			}
		}
	} else if !responding(ctx, p) {
		log, err := os.OpenFile(p.daemonLog(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		command := exec.Command(p.tailscaled(), daemonArgs(p)...)
		command.Stdout, command.Stderr = log, log
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		err = command.Start()
		log.Close()
		if err != nil {
			return err
		}
		os.WriteFile(p.pidFile(), []byte(strconv.Itoa(command.Process.Pid)+"\n"), 0600)
		go command.Wait()
	}
	deadline := time.Now().Add(30 * time.Second)
	for !responding(ctx, p) {
		if time.Now().After(deadline) {
			return fmt.Errorf("tailscaled did not start")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return nil
}

func responding(ctx context.Context, p paths) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := cli(ctx, p, "status", "--json")
	return err == nil
}

// StartLogin begins signing the cloud computer in. Installing Tailscale and
// waiting for the browser can take minutes, so a detached worker
// (`fennec-terminal tailnet up`, see RunUp) does it; the Mac polls Status.
func StartLogin(ctx context.Context, home string) (Status, error) {
	p := paths{home}
	if current := readJob(p); current.active() {
		return ReadStatus(ctx, home, false), nil
	}
	if status := ReadStatus(ctx, home, false); status.State == "running" {
		return status, nil
	}
	binary := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	if err := os.MkdirAll(p.state(), 0700); err != nil {
		return Status{}, err
	}
	log, err := os.OpenFile(p.upLog(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return Status{}, err
	}
	defer log.Close()
	command := exec.Command(binary, "tailnet", "up")
	command.Stdout, command.Stderr = log, log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		return Status{}, err
	}
	// The worker records its own progress; only fill in until it does, so a
	// fast worker's later stage isn't overwritten.
	if readJob(p).PID != command.Process.Pid {
		if err = writeJob(p, job{Stage: "installing", PID: command.Process.Pid, Started: time.Now().Unix()}); err != nil {
			command.Process.Kill()
			return Status{}, err
		}
	}
	go command.Wait()
	return ReadStatus(ctx, home, false), nil
}

// networkBlockedGrace is how long RunUp lets tailscaled retry a registration
// the egress gateway rejects before it reports network_blocked.
var networkBlockedGrace = 30 * time.Second

// RunUp is the sign-in worker: install, start tailscaled, then `tailscale up`,
// which waits until the user approves the login URL in the browser.
func RunUp(ctx context.Context, home string) error {
	p := paths{home}
	current := job{Stage: "installing", PID: os.Getpid(), Started: time.Now().Unix()}
	fail := func(code string, err error) error {
		current.Stage, current.Error, current.Detail = "failed", code, err.Error()
		writeJob(p, current)
		return err
	}
	writeJob(p, current)
	if err := install(ctx, p); err != nil {
		return fail(ErrInstallFailed, err)
	}
	current.Stage = "starting"
	writeJob(p, current)
	// A fresh tailscaled: the last attempt's login error stays in its health
	// report otherwise, and a retry would look like it failed at once.
	stopDaemon(ctx, p)
	if err := ensureDaemon(ctx, p); err != nil {
		return fail(ErrStartFailed, err)
	}
	current.Stage = "login"
	writeJob(p, current)
	upCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cli(upCtx, p, "up", "--reset", "--hostname="+Hostname, "--accept-dns=false", "--accept-routes=false", "--timeout=15m")
		done <- err
	}()
	// `tailscale up` keeps retrying a registration the network rejects;
	// stop as soon as tailscaled reports why, so the panel can say so. A
	// network_blocked answer gets a grace period first: the Mac has just added
	// Tailscale to the gateway's TLS passthrough (FEN-129), and the gateway
	// may still be applying it while tailscaled makes its first attempts.
	blockedAfter := time.Now().Add(networkBlockedGrace)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				return fail(ErrLoginFailed, err)
			}
			current.Stage = "done"
			return writeJob(p, current)
		case <-ticker.C:
			readCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			raw, err := cli(readCtx, p, "status", "--json")
			stop()
			var b backend
			if err != nil || json.Unmarshal(raw, &b) != nil || b.BackendState == "Running" || b.AuthURL != "" {
				continue
			}
			for _, message := range b.Health {
				if code := classifyLoginError(message); code != "" {
					if code == ErrNetworkBlocked && time.Now().Before(blockedAfter) {
						continue
					}
					cancel()
					<-done
					return fail(code, fmt.Errorf("%s", message))
				}
			}
		}
	}
}

// stopDaemon stops Fennec's tailscaled, if it runs.
func stopDaemon(ctx context.Context, p paths) {
	if systemd() {
		if systemctl(ctx, "is-active", "--quiet", serviceName) == nil {
			systemctl(ctx, "stop", serviceName)
		}
	} else if data, err := os.ReadFile(p.pidFile()); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 1 && alive(pid) {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); responding(ctx, p) && time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
	}
}

// Logout signs the cloud computer out of the tailnet, stops a pending
// sign-in, and stops tailscaled, so the panel shows Tailscale as off with no
// leftover error.
func Logout(ctx context.Context, home string) (Status, error) {
	p := paths{home}
	if current := readJob(p); current.active() && current.PID > 1 && current.PID != os.Getpid() {
		// The worker leads its own session: this also stops `tailscale up`.
		syscall.Kill(-current.PID, syscall.SIGTERM)
		current.Stage, current.Error, current.Detail = "failed", "", "canceled"
		writeJob(p, current)
	}
	if p.installed() && responding(ctx, p) {
		logoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cli(logoutCtx, p, "logout") // best effort: a signed-out node has nothing to log out
	}
	stopDaemon(ctx, p)
	os.Remove(p.job())
	return ReadStatus(ctx, home, false), nil
}
