package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fennec/internal/sessionid"
)

var nativeLoginSystemEnvKeys = []string{
	"NODE_EXTRA_CA_CERTS",
	"NODE_USE_SYSTEM_CA",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"CURL_CA_BUNDLE",
	"GIT_SSL_CAINFO",
	"REQUESTS_CA_BUNDLE",
	"PIP_CERT",
	"AWS_CA_BUNDLE",
	"GRPC_DEFAULT_SSL_ROOTS_FILE_PATH",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"ALL_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
	"all_proxy",
	"TZ",
}

type NativeLogin struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Created time.Time `json:"created"`
}

func (p NativeLogin) Validate() error {
	if !sessionid.Valid(p.ID) || !map[string]bool{"codex": true, "claude": true, "pi": true}[p.Agent] {
		return fmt.Errorf("invalid agent login")
	}
	return nil
}
func nativeLoginDirectory(home, id string) string {
	return filepath.Join(home, ".local/fennec/native-logins", id)
}
func validateNativeLoginDirectory(home, root string) error {
	for path := root; path != filepath.Clean(home); path = filepath.Dir(path) {
		if path == "/" || path == "." {
			return fmt.Errorf("invalid login directory")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid login directory")
		}
	}
	return nil
}
func readCloudLogins(home string) (map[string]string, error) {
	return readLoginSelections(home, ".cloud-logins.json")
}
func readLoginSelections(home, name string) (map[string]string, error) {
	data, err := readNativeFile(filepath.Join(home, ".local/fennec/native", name))
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var logins map[string]string
	if json.Unmarshal(data, &logins) != nil || logins == nil {
		return nil, fmt.Errorf("invalid cloud login selection")
	}
	for agent, id := range logins {
		if (NativeLogin{ID: id, Agent: agent}).Validate() != nil {
			return nil, fmt.Errorf("invalid cloud login selection")
		}
	}
	return logins, nil
}

// NativeAccountRoot chooses an explicitly completed cloud login. Absence means
// local-to-cloud migration; invalid selection never silently chooses another account.
func NativeAccountRoot(home, agent string) (string, bool, error) {
	base := filepath.Join(home, ".local/fennec/native")
	if _, err := os.Lstat(base); err == nil {
		if err = validateNativeLoginDirectory(home, base); err != nil {
			return "", false, err
		}
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	logins, err := readCloudLogins(home)
	if err != nil {
		return "", false, err
	}
	if id := logins[agent]; id != "" {
		root := nativeLoginDirectory(home, id)
		if err := validateNativeLoginDirectory(home, filepath.Join(root, agent)); err != nil {
			return "", false, err
		}
		raw, err := readNativeFile(filepath.Join(root, "login.json"))
		if err != nil {
			return "", false, err
		}
		var plan NativeLogin
		if json.Unmarshal(raw, &plan) != nil || plan.ID != id || plan.Agent != agent {
			return "", false, fmt.Errorf("cloud login identity mismatch")
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("invalid cloud login directory")
		}
		return root, true, nil
	}
	return filepath.Join(home, ".local/fennec/native"), false, nil
}
func PrepareNativeLogin(ctx context.Context, home string, plan NativeLogin) error {
	return withNativeLoginLock(ctx, home, func() error { return prepareNativeLogin(home, plan) })
}
func prepareNativeLogin(home string, plan NativeLogin) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if !NativeAgentMode(home) {
		return fmt.Errorf("switch off Magpie before signing in to an agent")
	}
	parent := ".local/fennec/native-logins"
	if err := ensureNativeDirectory(home, parent); err != nil {
		return err
	}
	root := nativeLoginDirectory(home, plan.ID)
	pending, err := readLoginSelections(home, ".pending-logins.json")
	if err != nil {
		return err
	}
	if err := os.Mkdir(root, 0700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		raw, e := readNativeFile(filepath.Join(root, "login.json"))
		var old NativeLogin
		if e != nil || json.Unmarshal(raw, &old) != nil || old.ID != plan.ID || old.Agent != plan.Agent {
			return fmt.Errorf("login identity is already in use")
		}
		if pending[plan.Agent] != plan.ID {
			return fmt.Errorf("a newer login has replaced this request")
		}
		return nil
	}
	if err := os.Mkdir(filepath.Join(root, plan.Agent), 0700); err != nil {
		return err
	}
	if err := atomicCredential(filepath.Join(root, "env.json"), []byte(`{}`)); err != nil {
		return err
	}
	if plan.Agent == "codex" {
		if err := atomicCredential(filepath.Join(root, "codex/config.toml"), []byte("cli_auth_credentials_store='file'\n")); err != nil {
			return err
		}
	}
	plan.Created = time.Now().UTC()
	raw, _ := json.Marshal(plan)
	if err := atomicCredential(filepath.Join(root, "login.json"), raw); err != nil {
		return err
	}
	pending[plan.Agent] = plan.ID
	raw, _ = json.Marshal(pending)
	return atomicCredential(filepath.Join(home, ".local/fennec/native/.pending-logins.json"), raw)
}
func NativeLoginCommand(home string, plan NativeLogin) ([]string, []string, error) {
	if err := plan.Validate(); err != nil {
		return nil, nil, err
	}
	root := nativeLoginDirectory(home, plan.ID)
	if err := validateNativeLoginDirectory(home, filepath.Join(root, plan.Agent)); err != nil {
		return nil, nil, err
	}
	raw, err := readNativeFile(filepath.Join(root, "login.json"))
	var stored NativeLogin
	if err != nil || json.Unmarshal(raw, &stored) != nil || stored.ID != plan.ID || stored.Agent != plan.Agent {
		return nil, nil, fmt.Errorf("login has not been prepared")
	}
	if time.Since(stored.Created) > 20*time.Minute {
		return nil, nil, fmt.Errorf("login expired; start again")
	}
	ensureCloudSearchTools(home)
	args := map[string][]string{"codex": {"login", "--device-auth"}, "claude": {"auth", "login", "--claudeai"}, "pi": {"--no-extensions", "--no-skills", "--no-context-files", "--no-session", "--no-tools"}}[plan.Agent]
	pathParts := []string{
		filepath.Join(home, ".local/fennec/node/bin"),
		filepath.Join(home, ".local/fennec/agents/bin"),
		filepath.Join(home, ".local/bin"),
	}
	if currentPath := os.Getenv("PATH"); currentPath != "" {
		pathParts = append(pathParts, currentPath)
	}
	env := []string{"HOME=" + root, "PATH=" + strings.Join(pathParts, string(os.PathListSeparator)), "TERM=xterm-256color", "LANG=C.UTF-8"}
	for _, key := range nativeLoginSystemEnvKeys {
		if value := os.Getenv(key); value != "" && !strings.ContainsRune(value, 0) {
			env = append(env, key+"="+value)
		}
	}
	env = ensureDefaultSystemCerts(env)
	switch plan.Agent {
	case "codex":
		env = append(env, "CODEX_HOME="+filepath.Join(root, "codex"))
	case "claude":
		env = append(env, "CLAUDE_CONFIG_DIR="+filepath.Join(root, "claude"))
	case "pi":
		env = append(env, "PI_CODING_AGENT_DIR="+filepath.Join(root, "pi"))
	}
	return args, env, nil
}
func ActivateNativeLogin(ctx context.Context, home string, plan NativeLogin) error {
	return withNativeLoginLock(ctx, home, func() error { return activateNativeLogin(home, plan) })
}
func activateNativeLogin(home string, plan NativeLogin) error {
	if _, _, err := NativeLoginCommand(home, plan); err != nil {
		return err
	}
	root := nativeLoginDirectory(home, plan.ID)
	credential := map[string]string{"codex": "codex/auth.json", "claude": "claude/.credentials.json", "pi": "pi/auth.json"}[plan.Agent]
	if _, err := readNativeFile(filepath.Join(root, credential)); err != nil {
		return fmt.Errorf("finish signing in before continuing")
	}
	if !nativeHasCredentials(root, plan.Agent) {
		return fmt.Errorf("finish signing in before continuing")
	}
	if !NativeAgentMode(home) {
		return fmt.Errorf("agent settings changed; sign in again")
	}
	pending, err := readLoginSelections(home, ".pending-logins.json")
	if err != nil {
		return err
	}
	if pending[plan.Agent] != plan.ID {
		return fmt.Errorf("a newer login has replaced this request")
	}
	logins, err := readCloudLogins(home)
	if err != nil {
		return err
	}
	logins[plan.Agent] = plan.ID
	raw, _ := json.Marshal(logins)
	return atomicCredential(filepath.Join(home, ".local/fennec/native/.cloud-logins.json"), raw)
}
func withNativeLoginLock(ctx context.Context, home string, operation func() error) error {
	if err := ensureNativeDirectory(home, ".local/fennec/native"); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(home, ".local/fennec/native/.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}
func RunNativeLogin(ctx context.Context, home string, plan NativeLogin, input io.Reader, output, diagnostic io.Writer) error {
	if err := PrepareNativeLogin(ctx, home, plan); err != nil {
		return err
	}
	args, env, err := NativeLoginCommand(home, plan)
	if err != nil {
		return err
	}
	binary, err := exec.LookPath(plan.Agent)
	if err != nil {
		return fmt.Errorf("agent is not installed on the cloud computer")
	}
	step, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	command := exec.CommandContext(step, binary, args...)
	command.Env = env
	command.Dir = nativeLoginDirectory(home, plan.ID)
	command.Stdin = input
	command.Stdout = output
	command.Stderr = diagnostic
	if plan.Agent == "pi" {
		fmt.Fprintln(diagnostic, "Enter /login to connect a provider. After signing in, exit Pi to continue.")
	}
	if err = command.Run(); err != nil {
		return fmt.Errorf("agent sign-in was not completed")
	}
	return ActivateNativeLogin(ctx, home, plan)
}

func CancelNativeLogin(ctx context.Context, home string, plan NativeLogin) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	root := nativeLoginDirectory(home, plan.ID)
	if err := validateNativeLoginDirectory(home, root); err != nil {
		return err
	}
	raw, err := readNativeFile(filepath.Join(root, "login.json"))
	var stored NativeLogin
	if err != nil || json.Unmarshal(raw, &stored) != nil || stored.ID != plan.ID || stored.Agent != plan.Agent {
		return fmt.Errorf("login session is unavailable")
	}
	return withNativeLoginLock(ctx, home, func() error {
		active, err := readCloudLogins(home)
		if err != nil {
			return err
		}
		if active[plan.Agent] == plan.ID {
			return fmt.Errorf("sign-in has already completed")
		}
		pending, err := readLoginSelections(home, ".pending-logins.json")
		if err != nil {
			return err
		}
		if pending[plan.Agent] == plan.ID {
			delete(pending, plan.Agent)
		}
		data, _ := json.Marshal(pending)
		return atomicCredential(filepath.Join(home, ".local/fennec/native/.pending-logins.json"), data)
	})
}

// Keep only the public attempt identity after a confirmed stop. The manifest
// makes cancellation idempotent without retaining a canceled OAuth credential.
func DiscardNativeLogin(ctx context.Context, home string, plan NativeLogin) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	return withNativeLoginLock(ctx, home, func() error {
		active, err := readCloudLogins(home)
		if err != nil {
			return err
		}
		pending, err := readLoginSelections(home, ".pending-logins.json")
		if err != nil {
			return err
		}
		if active[plan.Agent] == plan.ID || pending[plan.Agent] == plan.ID {
			return fmt.Errorf("login is still selected or pending")
		}
		root := nativeLoginDirectory(home, plan.ID)
		if err := validateNativeLoginDirectory(home, root); err != nil {
			return err
		}
		raw, err := readNativeFile(filepath.Join(root, "login.json"))
		var stored NativeLogin
		if err != nil || json.Unmarshal(raw, &stored) != nil || stored.ID != plan.ID || stored.Agent != plan.Agent {
			return fmt.Errorf("login identity mismatch")
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == "login.json" {
				continue
			}
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	})
}
