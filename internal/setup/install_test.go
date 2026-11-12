package setup

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAllowlist(t *testing.T) {
	for _, a := range []Agent{{"evil", "1.0.0"}, {"claude", "1.0.0;id"}, {"codex", "$(id)"}} {
		if _, err := AgentInstallScript(a); err == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
	s, err := AgentInstallScript(Agent{"codex", "0.166.0"})
	if err != nil || !strings.Contains(s, "@openai/codex@0.166.0") || !strings.Contains(s, "--version") {
		t.Fatalf("%v %s", err, s)
	}
}

func TestEnsureAgentInstallsLatestOnlyWhenMissing(t *testing.T) {
	script, err := EnsureAgentScript(Agent{ID: "codex", Version: "latest"})
	if err != nil || !strings.Contains(script, "if [ -x \"$HOME/.local/fennec/agents/bin/codex\" ]") || strings.Contains(script, "npm view") || !strings.Contains(script, "npm install --global --no-audit --no-fund --loglevel=error --prefix \"$stage\" -- @openai/codex@latest") {
		t.Fatal(err, script)
	}
	if strings.Contains(script, "DetectVersions") {
		t.Fatal("local version used by latest installer")
	}
}

func TestEnsureAgentSkipsExistingAndInstallsMissing(t *testing.T) {
	for _, installed := range []bool{true, false} {
		t.Run(fmt.Sprintf("installed=%v", installed), func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local/fennec/agents/bin")
			os.MkdirAll(bin, 0700)
			os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho 'unexpected version execution' >&2\nexit 99\n"), 0700)
			tools := filepath.Join(home, "tools")
			os.MkdirAll(tools, 0700)
			npm := `#!/bin/sh
if [ "$1" = view ]; then echo 0.167.0; exit 0; fi
printf 'install\n' >> "$HOME/install-count"
while [ "$1" != --prefix ]; do shift; done
shift
prefix=$1
mkdir -p "$prefix/bin"
printf '#!/bin/sh\necho "codex-cli 0.167.0"\n' > "$prefix/bin/codex"
chmod +x "$prefix/bin/codex"
`
			os.WriteFile(filepath.Join(tools, "npm"), []byte(npm), 0700)
			flock := "#!/bin/sh\nwhile [ \"$1\" != \"${1#-}\" ]; do shift; done\nshift\nexec >&$1\n"
			os.WriteFile(filepath.Join(tools, "flock"), []byte(flock), 0700)
			if !installed {
				os.Remove(filepath.Join(bin, "codex"))
			}
			script, err := EnsureAgentScript(Agent{ID: "codex", Version: "latest"})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-s")
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+tools+":/usr/bin:/bin")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, output)
			}
			count, err := os.ReadFile(filepath.Join(home, "install-count"))
			if installed && !os.IsNotExist(err) {
				t.Fatal("current latest release was reinstalled")
			}
			if !installed && (err != nil || string(count) != "install\n") {
				t.Fatal("latest upgrade was not installed", err)
			}
		})
	}
}

func TestAdditionalAgentPackages(t *testing.T) {
	for id, pkg := range map[string]string{"pi": "@earendil-works/pi-coding-agent", "opencode": "opencode-ai"} {
		script, err := AgentInstallScript(Agent{ID: id, Version: "1.5.0"})
		if err != nil || !strings.Contains(script, pkg+"@1.5.0") {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

func TestAlreadyInstalledVersionSkipsNetwork(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/fennec/agents/bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf 'codex-cli 0.166.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(home, "tools")
	os.MkdirAll(tools, 0700)
	// npm exists, but any invocation fails: a correct no-op must not call it.
	os.WriteFile(filepath.Join(tools, "npm"), []byte("#!/bin/sh\nexit 99\n"), 0700)
	script, err := AgentInstallScript(Agent{ID: "codex", Version: "0.166.0"})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-s")
	command.Stdin = strings.NewReader(script)
	command.Env = append(os.Environ(), "HOME="+home, "PATH="+tools+":/usr/bin:/bin")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("no-op failed: %v %s", err, output)
	}
	target, err := os.Readlink(filepath.Join(home, ".local/bin/codex"))
	if err != nil || target != filepath.Join(bin, "codex") {
		t.Fatalf("missing executable link: %s %v", target, err)
	}
}

func TestUpgradePreservesActiveVersionUntilVerified(t *testing.T) {
	for _, mode := range []string{"failure", "wrong-version", "success"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local/fennec/agents/bin")
			os.MkdirAll(bin, 0700)
			active := filepath.Join(bin, "codex")
			os.WriteFile(active, []byte("#!/bin/sh\necho 'codex-cli 0.165.0'\n"), 0700)
			tools := filepath.Join(home, "tools")
			os.MkdirAll(tools, 0700)
			npm := `#!/bin/sh
while [ "$1" != --prefix ]; do shift; done
shift
prefix=$1
mkdir -p "$prefix/bin"
if [ "$INSTALL_MODE" = failure ]; then exit 42; fi
version=0.166.0
if [ "$INSTALL_MODE" = wrong-version ]; then version=0.164.0; fi
printf '#!/bin/sh\necho "codex-cli v%s"\n' "$version" > "$prefix/bin/codex"
chmod +x "$prefix/bin/codex"
`
			os.WriteFile(filepath.Join(tools, "npm"), []byte(npm), 0700)
			script, err := AgentInstallScript(Agent{"codex", "0.166.0"})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-s")
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+tools+":/usr/bin:/bin", "INSTALL_MODE="+mode)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mode == "success") {
				t.Fatalf("unexpected installation: %v %s", err, output)
			}
			current, err := exec.Command(active, "--version").Output()
			if err != nil {
				t.Fatal(err)
			}
			expected := "0.165.0"
			if mode == "success" {
				expected = "0.166.0"
			}
			if !strings.Contains(string(current), expected) {
				t.Fatalf("active version damaged: %s", current)
			}
			dirs, _ := filepath.Glob(filepath.Join(home, ".local/fennec/agent-versions/*"))
			if mode != "success" && len(dirs) != 0 {
				t.Fatal("failed staging directory retained")
			}
		})
	}
}

func TestConcurrentSameVersionInstallsOnce(t *testing.T) {
	home := t.TempDir()
	tools := filepath.Join(home, "tools")
	os.MkdirAll(tools, 0700)
	npm := `#!/bin/sh
while [ "$1" != --prefix ]; do shift; done
shift
prefix=$1
printf 'install\n' >> "$HOME/install-count"
sleep 0.2
mkdir -p "$prefix/bin"
printf '#!/bin/sh\necho "codex-cli 0.166.0"\n' > "$prefix/bin/codex"
chmod +x "$prefix/bin/codex"
`
	os.WriteFile(filepath.Join(tools, "npm"), []byte(npm), 0700)
	script, err := AgentInstallScript(Agent{"codex", "0.166.0"})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			cmd := exec.Command("sh", "-s")
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+tools+":/usr/bin:/bin")
			output, err := cmd.CombinedOutput()
			if err != nil {
				results <- fmt.Errorf("%w: %s", err, output)
			} else {
				results <- nil
			}
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	count, err := os.ReadFile(filepath.Join(home, "install-count"))
	if err != nil || string(count) != "install\n" {
		t.Fatalf("duplicate installation: %q %v", count, err)
	}
}

func TestAgentFailureIsolationAndTransportAbort(t *testing.T) {
	agents := []Agent{{"opencode", "1.18.18"}, {"codex", "0.166.0"}}
	calls := 0
	err := installAgentSet(context.Background(), agents, func(string) {}, func(string) error {
		calls++
		if calls == 1 {
			return fmt.Errorf("package unavailable: %w", &ssh.ExitError{})
		}
		return nil
	})
	var partial *AgentInstallFailures
	if !errors.As(err, &partial) || calls != 2 || len(partial.Agents) != 1 || partial.Agents["opencode"] == nil {
		t.Fatalf("failure isolation: %v calls=%d", err, calls)
	}
	calls = 0
	err = installAgentSet(context.Background(), agents, func(string) {}, func(string) error { calls++; return errors.New("connection lost") })
	if err == nil || errors.As(err, &partial) || calls != 1 {
		t.Fatal("transport error treated as partial", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	err = installAgentSet(ctx, agents, func(string) {}, func(string) error { calls++; cancel(); return &ssh.ExitError{} })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled install continued", err, calls)
	}
}

func TestOpenCodeV2DoesNotUseV1Package(t *testing.T) {
	script, err := AgentInstallScript(Agent{ID: "opencode", Version: "2.1.4"})
	if err != nil || !strings.Contains(script, "@opencode/cli@2.1.4") || strings.Contains(script, "opencode-ai@2.1.4") {
		t.Fatal("v2 routed to v1 installer", err)
	}
}
