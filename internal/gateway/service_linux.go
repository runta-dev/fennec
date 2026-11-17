//go:build linux

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var testUnitDir string
var probeGateway = localProbe
var gatewayDependencies = dependencies

func unitDir(home string) string {
	if testUnitDir != "" {
		return testUnitDir
	}
	if os.Geteuid() == 0 {
		return "/etc/systemd/system"
	}
	return filepath.Join(home, ".config/systemd/user")
}
func installed(home string) bool {
	b, err := os.ReadFile(filepath.Join(unitDir(home), Service))
	return err == nil && strings.Contains(string(b), "Environment="+quote("HOME="+home)+"\n")
}
func control(ctx context.Context, args ...string) ([]byte, error) {
	if os.Geteuid() != 0 {
		args = append([]string{"--user"}, args...)
	}
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gateway service operation %s failed", args[len(args)-1])
	}
	return b, nil
}

// WithLock serializes mode changes, deployment and timer recovery. Configuration
// writers acquire their configuration lock first, then this lifecycle lock.
func WithLock(ctx context.Context, home string, fn func() error) error {
	path := filepath.Join(home, ".local/fennec/install-locks/gateway-service.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`), `%`, `%%`) + `"`
}
func units(home, executable string, user bool) map[string]string {
	target := "multi-user.target"
	if user {
		target = "default.target"
	}
	env := "Environment=" + quote("HOME="+home) + "\nEnvironment=" + quote("PATH="+home+"/.local/fennec/node/bin:"+home+"/.local/fennec/agents/bin:"+home+"/.local/bin:/usr/local/bin:/usr/bin:/bin") + "\n"
	for key, value := range map[string]string{"MAGPIE_ADDR": "127.0.0.1:3425", "DBUS_SESSION_BUS_ADDRESS": "disabled:", "CODEX_HOME": home + "/.local/fennec/native/codex", "CLAUDE_CONFIG_DIR": home + "/.local/fennec/native/claude", "PI_CODING_AGENT_DIR": home + "/.local/fennec/native/pi", "KIMI_CODE_HOME": home + "/.local/fennec/native/kimi"} {
		env += "Environment=" + quote(key+"="+value) + "\n"
	}
	if _, err := os.Stat("/usr/local/share/ca-certificates/runta-egress.crt"); err == nil {
		env += "Environment=NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/runta-egress.crt\n"
	}
	env += "Environment=SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt\n"
	return map[string]string{
		Service:                        "[Unit]\nDescription=Fennec model gateway\nAfter=network.target\nStartLimitIntervalSec=120\nStartLimitBurst=5\n[Service]\nType=exec\n" + env + "ExecStart=" + quote(filepath.Join(home, ".local/bin/magpie")) + " serve\nRestart=always\nRestartSec=2\nTimeoutStopSec=60\nKillMode=mixed\nCPUWeight=100\nUMask=0077\n[Install]\nWantedBy=" + target + "\n",
		"fennec-magpie-health.service": "[Unit]\nDescription=Fennec model gateway health\n[Service]\nType=oneshot\n" + env + "ExecStart=" + quote(executable) + " magpie-service check\nTimeoutStartSec=120\nUMask=0077\n",
		"fennec-magpie-health.timer":   "[Unit]\nDescription=Check Fennec model gateway locally\n[Timer]\nOnBootSec=5\nOnUnitInactiveSec=5\nAccuracySec=1\n[Install]\nWantedBy=timers.target\n",
	}
}

// Install replaces only the verified legacy supervisor, never arbitrary Magpie
// processes. Existing CLI sessions keep their processes and credentials.
func Install(ctx context.Context, home string) error {
	return WithLock(ctx, home, func() error {
		raiseInotifyLimit()
		if _, err := os.Stat("/run/systemd/system"); err != nil {
			return fmt.Errorf("cloud gateway requires systemd")
		}
		if os.Geteuid() != 0 {
			b, err := exec.CommandContext(ctx, "loginctl", "show-user", strconv.Itoa(os.Getuid()), "-p", "Linger", "--value").Output()
			if err != nil || strings.TrimSpace(string(b)) != "yes" {
				return fmt.Errorf("cloud gateway requires a persistent user service manager")
			}
		}
		if _, err := os.Stat(filepath.Join(unitDir(home), Service)); err == nil && !installed(home) {
			return fmt.Errorf("gateway service belongs to another home")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		// Health uses an immutable copy, so ordinary terminal binary upgrades cannot
		// remove its command or change the service implementation beneath a timer.
		data, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		helper := filepath.Join(home, ".local/fennec/gateway/fennec-terminal")
		if err = atomicWrite(helper, data, 0700); err != nil {
			return err
		}
		for name, body := range units(home, helper, os.Geteuid() != 0) {
			if err = atomicWrite(filepath.Join(unitDir(home), name), []byte(body), 0644); err != nil {
				return err
			}
		}
		if _, err = control(ctx, "daemon-reload"); err != nil {
			return err
		}
		if err = retireLegacy(ctx, home); err != nil {
			return err
		}
		// A foreign listener is never adopted or killed.
		active, _ := control(ctx, "is-active", Service)
		if strings.TrimSpace(string(active)) != "active" && portOccupied(ctx) {
			return fmt.Errorf("gateway port is occupied by an unmanaged process")
		}
		if err = atomicWrite(filepath.Join(home, ".config/fennec/gateway-enabled"), []byte("true\n"), 0600); err != nil {
			return err
		}
		failed, _ := control(ctx, "is-failed", Service)
		if strings.TrimSpace(string(failed)) == "failed" {
			if _, err = control(ctx, "reset-failed", Service); err != nil {
				return err
			}
		}
		if _, err = control(ctx, "enable", "--now", Service, "fennec-magpie-health.timer"); err != nil {
			return err
		}
		if err = refreshLocked(ctx, home); err != nil {
			return err
		}
		if err = waitHealthy(ctx); err != nil {
			_ = writeState(home, State{Status: "recovering", Reason: "starting"})
			return err
		}
		return writeState(home, State{Status: "healthy"})
	})
}
func retireLegacy(ctx context.Context, home string) error {
	path := filepath.Join(home, ".config/fennec/magpie-supervisor.pid")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return fmt.Errorf("invalid legacy supervisor identity")
	}
	args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if os.IsNotExist(err) {
		return os.Remove(path)
	}
	if err != nil {
		return err
	}
	if !exactArgument(args, filepath.Join(home, ".local/fennec/magpie-supervisor.sh")) {
		return fmt.Errorf("legacy supervisor identity changed")
	}
	children, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	var owned []string
	for _, id := range strings.Fields(string(children)) {
		exe, _ := os.Readlink("/proc/" + id + "/exe")
		if strings.TrimSuffix(exe, " (deleted)") == filepath.Join(home, ".local/bin/magpie") {
			owned = append(owned, id)
		}
	}
	if err = syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	deadline := time.NewTimer(65 * time.Second)
	defer deadline.Stop()
	for {
		alive := false
		for _, id := range owned {
			if _, err := os.Readlink("/proc/" + id + "/exe"); err == nil {
				alive = true
			}
		}
		if !alive {
			return os.Remove(path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("legacy gateway is still draining")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func waitHealthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if probeGateway(ctx) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("gateway readiness not confirmed: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
func Restart(ctx context.Context, home string) error {
	return WithLock(ctx, home, func() error {
		if !installed(home) {
			return fmt.Errorf("gateway service is not installed")
		}
		if !enabled(home) && !gatewayDependencies(home) {
			return fmt.Errorf("gateway is disabled")
		}
		s := ReadState(home)
		s.Status = "recovering"
		s.Reason = "configuration_changed"
		_ = writeState(home, s)
		if _, err := control(ctx, "restart", Service); err != nil {
			return err
		}
		if err := waitHealthy(ctx); err != nil {
			return err
		}
		s.Status = "healthy"
		s.Reason = ""
		s.Failures = 0
		return writeState(home, s)
	})
}
func enabled(home string) bool {
	b, _ := os.ReadFile(filepath.Join(home, ".config/fennec/gateway-enabled"))
	return strings.TrimSpace(string(b)) == "true"
}

// Mode changes share the same lock as the health timer. Off never installs or
// probes a gateway. The timer drains old dependents and then disables itself.
func SetEnabledLocked(home string, on bool) error {
	if !installed(home) {
		return nil
	}
	wasEnabled := enabled(home)
	if err := atomicWrite(filepath.Join(home, ".config/fennec/gateway-enabled"), []byte(strconv.FormatBool(on)+"\n"), 0600); err != nil {
		return err
	}
	if on && !wasEnabled {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := control(ctx, "enable", "--now", Service, "fennec-magpie-health.timer"); err != nil {
			return err
		}
	}
	return nil
}

func Check(ctx context.Context, home string) error {
	return WithLock(ctx, home, func() error {
		if !installed(home) {
			return nil
		}
		if !enabled(home) && !gatewayDependencies(home) {
			if _, err := control(ctx, "disable", "--now", Service, "fennec-magpie-health.timer"); err != nil {
				return err
			}
			return writeState(home, State{Status: "disabled"})
		}
		s := ReadState(home)
		if s.Status == "failed" {
			return nil
		}
		// systemd also bounds crash loops; the timer must not reset that budget.
		result, _ := control(ctx, "show", Service, "-p", "Result", "--value")
		if strings.TrimSpace(string(result)) == "start-limit-hit" {
			s.Status = "failed"
			s.Reason = "restart_limit"
			return writeState(home, s)
		}
		if probeGateway(ctx) == nil {
			s.Status = "healthy"
			s.Reason = ""
			s.Failures = 0
			return writeState(home, s)
		}
		s.Failures++
		s.Status = "recovering"
		s.Reason = "local_health_failed"
		if s.Failures < 3 {
			return writeState(home, s)
		}
		now := time.Now()
		recent := s.Restarts[:0]
		for _, t := range s.Restarts {
			if now.Sub(t) < 2*time.Minute {
				recent = append(recent, t)
			}
		}
		s.Restarts = recent
		if len(recent) >= 3 {
			s.Status = "failed"
			s.Reason = "restart_limit"
			return writeState(home, s)
		}
		s.Restarts = append(s.Restarts, now)
		s.Failures = 0
		if err := writeState(home, s); err != nil {
			return err
		}
		if _, err := control(ctx, "restart", Service); err != nil {
			return err
		}
		if err := waitHealthy(ctx); err != nil {
			return err
		}
		s.Status = "healthy"
		s.Reason = ""
		return writeState(home, s)
	})
}

// A live process, not a reference counter, owns each dependency. Old sessions
// are conservatively recognized by their agent executable, workspace and lack
// of an isolated native configuration. No environment content is returned.
func dependencies(home string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		base := "/proc/" + e.Name()
		exe, _ := os.Readlink(base + "/exe")
		if !strings.HasPrefix(strings.TrimSuffix(exe, " (deleted)"), home+"/.local/") {
			continue
		}
		env, err := os.ReadFile(base + "/environ")
		if err != nil {
			continue
		}
		if bytes.Contains(env, []byte(DependencyEnv+"=1\x00")) {
			return true
		}
		cwd, _ := os.Readlink(base + "/cwd")
		if !strings.HasPrefix(cwd, home+"/.fen/") {
			continue
		}
		name := filepath.Base(exe)
		if name != "codex" && name != "claude" && name != "claude.exe" && name != "opencode" && name != "node" && name != "bun" {
			continue
		}
		if !bytes.Contains(env, []byte(home+"/.local/fennec/native/")) {
			return true
		}
	}
	return false
}

// Keep encoding/json referenced here for the local status command's stable JSON.
func Status(home string) ([]byte, error) { return json.Marshal(ReadState(home)) }

func NeedsSetup(home string) bool {
	return !installed(home) || ReadState(home).Status == "failed"
}

func exactArgument(args []byte, value string) bool {
	for _, part := range bytes.Split(args, []byte{0}) {
		if string(part) == value {
			return true
		}
	}
	return false
}

// Refresh checks the executable inode of the systemd-owned main process. The
// service manager, never a process-name match, performs the draining restart.
func Refresh(ctx context.Context, home string) error {
	return WithLock(ctx, home, func() error { return refreshLocked(ctx, home) })
}
func refreshLocked(ctx context.Context, home string) error {
	if !installed(home) {
		return nil
	}
	b, err := control(ctx, "show", Service, "-p", "MainPID", "--value")
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return nil
	}
	running, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return err
	}
	target, err := os.Stat(filepath.Join(home, ".local/bin/magpie"))
	if err != nil {
		return err
	}
	if os.SameFile(running, target) {
		return nil
	}
	if _, err = control(ctx, "restart", Service); err != nil {
		return err
	}
	return waitHealthy(ctx)
}

// Preserve the existing best-effort watcher capacity setup. Failure to change a
// host sysctl is not a reason to reject an otherwise usable cloud runtime.
func raiseInotifyLimit() {
	path := "/proc/sys/fs/inotify/max_user_instances"
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || count >= 1024 {
		return
	}
	if os.WriteFile(path, []byte("1024\n"), 0644) == nil {
		_ = os.WriteFile("/etc/sysctl.d/60-inotify-instances.conf", []byte("fs.inotify.max_user_instances = 1024\n"), 0644)
	}
}

func portOccupied(ctx context.Context) bool {
	d := net.Dialer{Timeout: time.Second}
	c, err := d.DialContext(ctx, "tcp", "127.0.0.1:3425")
	if err != nil {
		return false
	}
	c.Close()
	return true
}
