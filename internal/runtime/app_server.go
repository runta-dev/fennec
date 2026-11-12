package runtime

import (
	"context"
	"errors"
	"fennec/internal/setup"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Codex app-server modes follow the cloud Codex CLI: "gateway" uses the Magpie
// routed ~/.codex configuration, "native" the synchronized native account
// environment. Each mode has its own CODEX_HOME and therefore its own server.
const (
	CodexAppServerGateway = "gateway"
	CodexAppServerNative  = "native"
)

// CodexAppServerSocket is the control socket that `codex app-server --listen
// unix://` and `codex app-server proxy` agree on for a CODEX_HOME.
func CodexAppServerSocket(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

// codexAppServerEnvironment mirrors how the cloud launches Codex CLI in the
// current agent mode, so desktop and terminal sessions share configuration.
func codexAppServerEnvironment(home string) (mode string, env []string, codexHome string, err error) {
	if setup.NativeAgentMode(home) {
		env, err = setup.NativeAgentEnvironment(home, "codex")
		if err != nil {
			return "", nil, "", err
		}
		for _, entry := range env {
			if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
				codexHome = value
			}
		}
		if codexHome == "" {
			return "", nil, "", fmt.Errorf("native codex home unavailable")
		}
		return CodexAppServerNative, env, codexHome, nil
	}
	gateway := os.Getenv("CODEX_HOME")
	if gateway == "" {
		gateway = filepath.Join(home, ".codex")
	}
	codexHome, err = prepareGatewayHome(home, gateway)
	if err != nil {
		return "", nil, "", err
	}
	env = []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CODEX_HOME=") {
			env = append(env, entry)
		}
	}
	return CodexAppServerGateway, append(env, "CODEX_HOME="+codexHome), codexHome, nil
}

// prepareGatewayHome builds the CODEX_HOME for ChatGPT in Magpie mode. Routing
// and model settings come from the Magpie-routed gateway home shared with Codex
// CLI, while the ChatGPT identity links to the user's synchronized native
// account, so ChatGPT shows the same account and limits as on the Mac. The
// gateway home's own credentials are never read or changed.
func prepareGatewayHome(home, gateway string) (string, error) {
	directory := filepath.Join(home, ".local/fennec/chatgpt/codex")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	links := map[string]string{}
	if err := syncChatGPTConfig(filepath.Join(directory, "config.toml"), filepath.Join(gateway, "config.toml")); err != nil {
		return "", err
	}
	for _, name := range []string{"AGENTS.md", "skills", "plugins", "rules", "prompts"} {
		links[name] = filepath.Join(gateway, name)
	}
	if root, _, err := setup.NativeAccountRoot(home, "codex"); err == nil {
		links["auth.json"] = filepath.Join(root, "codex", "auth.json")
	}
	for name, target := range links {
		if err := linkShared(filepath.Join(directory, name), target); err != nil {
			return "", err
		}
	}
	if _, ok := links["auth.json"]; !ok {
		if current, err := os.Readlink(filepath.Join(directory, "auth.json")); err == nil && current != "" {
			os.Remove(filepath.Join(directory, "auth.json"))
		}
	}
	return directory, nil
}

// linkShared points path at target, replacing a stale link or a copy that
// codex rewrote in place. A missing target removes the link.
func linkShared(path, target string) error {
	if current, err := os.Readlink(path); err == nil && current == target {
		if _, err := os.Stat(target); err == nil {
			return nil
		}
	}
	if _, err := os.Stat(target); err != nil {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return os.Remove(path)
		}
		return nil
	}
	temporary := path + ".fennec-link"
	os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// appServerConn reports which mode served the stream.
type appServerConn struct {
	net.Conn
	mode, codexHome string
}

// CodexHome is the CODEX_HOME of the app-server serving this connection.
func (conn appServerConn) CodexHome() string { return conn.codexHome }

func (conn appServerConn) StreamLabel() string { return conn.mode }

// DialCodexAppServer connects to the runtime owner's codex app-server. A running
// server (including one managed by `codex app-server daemon`) is reused; only a
// missing socket starts the already-installed codex, detached from the caller so
// desktop threads survive terminal-channel reconnects. No version is compared
// and nothing is installed here.
func DialCodexAppServer(ctx context.Context, home string) (net.Conn, error) {
	mode, env, codexHome, err := codexAppServerEnvironment(home)
	if err != nil {
		return nil, err
	}
	var overrides []string
	managed := mode == CodexAppServerGateway
	if managed {
		overrides = gatewayOverrides(codexHome)
	}
	socket := CodexAppServerSocket(codexHome)
	conn, err := dialCodexAppServer(ctx, home, socket, env, overrides, managed)
	if err != nil {
		return nil, err
	}
	return appServerConn{Conn: conn, mode: mode, codexHome: codexHome}, nil
}

func dialCodexAppServer(ctx context.Context, home, socket string, env, overrides []string, managed bool) (net.Conn, error) {
	directory := filepath.Dir(socket)
	signaturePath := filepath.Join(directory, "fennec-app-server.args")
	signature := strings.Join(overrides, "\n")
	current := func() bool {
		if !managed {
			return true
		}
		data, err := os.ReadFile(signaturePath)
		return err == nil && string(data) == signature
	}
	if current() {
		if conn, err := dialUnix(ctx, socket); err == nil {
			return conn, nil
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "fennec-start.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if conn, err := dialUnix(ctx, socket); err == nil {
		if current() {
			return conn, nil
		}
		// Fennec owns this CODEX_HOME; restart its server with the new options.
		conn.Close()
		if err := stopAppServers(ctx, filepath.Dir(directory), socket); err != nil {
			return nil, err
		}
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("codex is not installed in the runtime")
	}
	logFile, err := os.OpenFile(filepath.Join(directory, "app-server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	arguments := append([]string{"-c", "features.code_mode_host=true"}, overrides...)
	command := exec.Command(codex, append(arguments, "app-server", "--listen", "unix://")...)
	command.Dir = home
	command.Env = env
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = command.Start()
	logFile.Close()
	if err != nil {
		return nil, err
	}
	if managed {
		if err := os.WriteFile(signaturePath, []byte(signature), 0600); err != nil {
			return nil, err
		}
	}
	exited := make(chan struct{})
	go func() { command.Wait(); close(exited) }()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		if conn, err := dialUnix(ctx, socket); err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			if conn, err := dialUnix(ctx, socket); err == nil {
				return conn, nil
			}
			return nil, fmt.Errorf("codex app-server exited during startup")
		case <-deadline.C:
			return nil, fmt.Errorf("codex app-server did not become ready")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var dialer net.Dialer
	return dialer.DialContext(attempt, "unix", path)
}

// gatewayOverrides makes ChatGPT use Magpie as its model provider without an
// OpenAI login, as the Mac's Magpie configuration does. The gateway home keeps
// routing Codex CLI through openai_base_url; only ChatGPT's server gets these
// options, so ChatGPT.app does not gate sends on the ChatGPT plan's Codex usage.
func gatewayOverrides(codexHome string) []string {
	data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil || !strings.Contains(string(data), "[model_providers.magpie]") {
		return nil
	}
	return []string{"-c", `model_provider="magpie"`, "-c", "model_providers.magpie.requires_openai_auth=false"}
}

// stopAppServers ends the app-server processes serving codexHome, which only
// Fennec starts in its own ChatGPT home, and waits for the socket to close.
func stopAppServers(ctx context.Context, codexHome, socket string) error {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		command, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || !strings.Contains(string(command), "app-server\x00--listen") {
			continue
		}
		environment, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil {
			continue
		}
		for _, variable := range strings.Split(string(environment), "\x00") {
			if variable == "CODEX_HOME="+codexHome {
				pids = append(pids, pid)
				break
			}
		}
	}
	for _, pid := range pids {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := dialUnix(ctx, socket)
		if err != nil {
			return nil
		}
		conn.Close()
		if time.Now().After(deadline) {
			for _, pid := range pids {
				syscall.Kill(pid, syscall.SIGKILL)
			}
			return fmt.Errorf("previous ChatGPT app-server did not stop")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
