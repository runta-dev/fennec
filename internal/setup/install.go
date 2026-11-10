package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

var exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

// AgentInstallScript uses only a fixed package allowlist and validated versions.
// A dedicated prefix avoids overwriting other software in a reused runtime.
func AgentInstallScript(agent Agent) (string, error) {
	pkg, ok := map[string]string{"claude": "@anthropic-ai/claude-code", "codex": "@openai/codex", "pi": "@earendil-works/pi-coding-agent", "opencode": "opencode-ai"}[agent.ID]
	if !ok || !exactVersion.MatchString(agent.Version) {
		return "", fmt.Errorf("unsupported agent or non-exact version")
	}
	if agent.ID == "opencode" && !strings.HasPrefix(agent.Version, "1.") && !strings.HasPrefix(agent.Version, "2.") {
		return "", ErrAgentDistributionUnsupported
	}
	if agent.ID == "opencode" && strings.HasPrefix(agent.Version, "2.") {
		pkg = "@opencode/cli"
	}
	return agentInstallScript(agent.ID, pkg, agent.Version), nil
}

// EnsureAgentScript reuses any existing cloud binary. Only a missing binary
// is installed from the publisher's current latest stable release.
func EnsureAgentScript(agent Agent) (string, error) {
	pkg, ok := map[string]string{"claude": "@anthropic-ai/claude-code", "codex": "@openai/codex", "pi": "@earendil-works/pi-coding-agent", "opencode": "@opencode/cli"}[agent.ID]
	if !ok || agent.Version != "latest" {
		return "", fmt.Errorf("unsupported agent or install-if-missing request")
	}
	return ensureAgentScript(agent.ID, pkg), nil
}

func ensureAgentScript(id, pkg string) string {
	script := agentInstallScript(id, pkg, "latest")
	return `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
if [ -x "$HOME/.local/fennec/agents/bin/` + id + `" ]; then
 mkdir -p "$HOME/.local/bin"
 ln -sfn "$HOME/.local/fennec/agents/bin/` + id + `" "$HOME/.local/bin/` + id + `"
 exit 0
fi
` + script
}

func agentInstallScript(id, pkg, requested string) string {
	return `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
command -v npm >/dev/null || { echo 'Node.js/npm is required in the runtime image' >&2; exit 69; }
umask 077
mkdir -p "$HOME/.local/fennec/agents" "$HOME/.local/bin" "$HOME/.local/fennec/install-locks"
# Lock before checking the active version; another device may have just installed it.
exec 9>"$HOME/.local/fennec/install-locks/` + id + `.lock"
flock -w 180 9 || { echo 'Agent installation is busy; retry preparation' >&2; exit 75; }
requested=` + requested + `
version=$requested
if [ "$requested" = latest ]; then
 version=$(npm view ` + pkg + `@latest version)
fi
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
binary="$HOME/.local/fennec/agents/bin/` + id + `"
if [ -x "$binary" ]; then
 actual=$("$binary" --version 2>/dev/null) || actual=
 if printf '%s\n' "$actual" | grep -Eq '(^|[[:space:]])v?'"$version"'([[:space:]]|$)'; then
  ln -sfn "$binary" "$HOME/.local/bin/` + id + `"
  exit 0
 fi
fi
# Install in a fresh prefix so npm failure cannot damage the active version.
mkdir -p "$HOME/.local/fennec/agent-versions" "$HOME/.local/fennec/agents/bin"
stage=$(mktemp -d "$HOME/.local/fennec/agent-versions/` + id + `-$version.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
npm install --global --prefix "$stage" -- ` + pkg + `@` + requested + `
actual=$("$stage/bin/` + id + `" --version)
printf '%s\n' "$actual" | grep -Eq '(^|[[:space:]])v?'"$version"'([[:space:]]|$)'
# Keep the verified prefix: existing processes may still use older installations.
trap - EXIT HUP INT TERM
python3 - "$stage/bin/` + id + `" "$binary" "$HOME/.local/bin/` + id + `" <<'ACTIVATE'
import os,sys,uuid
source,binary,alias=sys.argv[1:]
for target,link in ((binary,source),(alias,binary)):
 temporary=target+'.'+uuid.uuid4().hex
 try:
  os.symlink(link,temporary)
  os.replace(temporary,target)
 finally:
  if os.path.lexists(temporary):os.unlink(temporary)
ACTIVATE
`
}

// OpenCode v2 uses a separate distribution; never silently substitute v1 npm.
var ErrAgentDistributionUnsupported = errors.New("agent distribution requires a supported exact-version installer")

// setupOutput retains a bounded diagnostic tail for installer failures only.
type setupOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *setupOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > 4096 {
		b.data = b.data[len(b.data)-4096:]
	}
	return n, nil
}
func (b *setupOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }

// Exec runs a finite setup step and closes the SSH session on cancellation.
func Exec(ctx context.Context, client *ssh.Client, script string) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(script)
	diagnostics := &setupOutput{}
	session.Stdout = diagnostics
	session.Stderr = diagnostics
	done := make(chan error, 1)
	go func() { done <- session.Run("sh -s") }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %s", err, diagnostics.String())
		}
		return nil
	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		session.Close()
		return ctx.Err()
	}
}

// AgentInstallFailures reports independent agent failures after shared setup succeeds.
type AgentInstallFailures struct{ Agents map[string]error }

func (e *AgentInstallFailures) Error() string {
	return fmt.Sprintf("%d agent installations failed", len(e.Agents))
}

func InstallAgents(ctx context.Context, client *ssh.Client, agents []Agent, emit func(string)) error {
	emit("installing_node")
	if err := Exec(ctx, client, NodeSetupScript); err != nil {
		return fmt.Errorf("install Node.js: %w", err)
	}
	if err := SyncAgentManifest(ctx, client, agents); err != nil {
		return err
	}
	return installAgentSet(ctx, agents, emit, func(script string) error { return Exec(ctx, client, script) })
}

// SyncAgentManifest records desired versions, not successful installations.
func SyncAgentManifest(ctx context.Context, client *ssh.Client, agents []Agent) error {
	manifest, err := json.Marshal(agents)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(manifest)
	return Exec(ctx, client, `set -eu
umask 077
mkdir -p "$HOME/.config/fennec"
staged=$(mktemp "$HOME/.config/fennec/agents.XXXXXX")
trap 'rm -f -- "$staged"' EXIT
printf '%s' '`+encoded+`' | base64 -d > "$staged"
mv "$staged" "$HOME/.config/fennec/agents.json"
`)
}

func installAgentSet(ctx context.Context, agents []Agent, emit func(string), execute func(string) error) error {
	failures := &AgentInstallFailures{Agents: map[string]error{}}
	for _, agent := range agents {
		if err := ctx.Err(); err != nil {
			return err
		}
		script, err := AgentInstallScript(agent)
		if errors.Is(err, ErrAgentDistributionUnsupported) {
			failures.Agents[agent.ID] = err
			continue
		}
		if err != nil {
			return err
		}
		emit("installing_" + agent.ID)
		if err = execute(script); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var exit *ssh.ExitError
			if !errors.As(err, &exit) {
				return fmt.Errorf("agent installation transport failed: %w", err)
			}
			failures.Agents[agent.ID] = err
		}
	}
	if len(failures.Agents) > 0 {
		return failures
	}
	return nil
}
