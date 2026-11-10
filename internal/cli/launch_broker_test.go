package cli

import (
	"context"
	"fennec/internal/launcher"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectLaunchReturnsActionableError(t *testing.T) {
	original := brokerProjectCommand
	t.Cleanup(func() { brokerProjectCommand = original })
	brokerProjectCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo 'cloud agent model is not configured' >&2; exit 1")
	}
	_, err := launchBrokerProject(context.Background(), "fennec", brokerConfiguration{}, launchProjectRequest{}, launcher.LaunchIntent{}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cloud agent model is not configured") {
		t.Fatal("project launch error was hidden", err)
	}
}

func TestBrokerEnforcesParentGrants(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "src")
	os.Mkdir(sub, 0700)
	cfg := brokerConfiguration{Token: "parent-token", RuntimeID: "parent-runtime", Device: "device", Agents: []string{"codex"}, Roots: []string{root}}
	intent := launcher.LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "codex", Directory: sub}
	req, err := brokerRequest(cfg, intent)
	if err != nil || req.Token != cfg.Token || req.RuntimeID != cfg.RuntimeID || req.Version != "" {
		t.Fatal("parent identity not preserved", err)
	}
	narrow := cfg
	narrow.Roots = []string{sub}
	if _, err := brokerRequest(narrow, intent); err == nil {
		t.Fatal("subdirectory grant expanded to repository root")
	}
	intent.Agent = "pi"
	if _, err = brokerRequest(cfg, intent); err == nil {
		t.Fatal("disabled agent accepted")
	}
	intent.Agent = "codex"
	intent.Directory = t.TempDir()
	if _, err = brokerRequest(cfg, intent); err == nil {
		t.Fatal("ungranted directory accepted")
	}
	link := filepath.Join(root, "escape")
	os.Symlink(intent.Directory, link)
	intent.Directory = link
	if _, err = brokerRequest(cfg, intent); err == nil {
		t.Fatal("symlink bypassed grant")
	}
}

func TestBrokerRejectsBroadHomeGrant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "project")
	os.MkdirAll(root, 0700)
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	cfg := brokerConfiguration{Token: "test", RuntimeID: "r", Device: "d", Agents: []string{"codex"}, Roots: []string{home}}
	intent := launcher.LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "codex", Directory: root}
	if _, err := brokerRequest(cfg, intent); err == nil {
		t.Fatal("home-wide grant accepted")
	}
	cfg.Roots = []string{root}
	if _, err := brokerRequest(cfg, intent); err != nil {
		t.Fatal("scoped project rejected", err)
	}
}

func TestSelectedButUnreadyAgentCannotLaunch(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	cfg := brokerConfiguration{Token: "parent", RuntimeID: "runtime", Device: "device", Agents: []string{"claude"}, Selected: []string{"claude", "opencode"}, Roots: []string{root}}
	request := launcher.LaunchIntent{RequestID: strings.Repeat("c", 32), Agent: "claude", Directory: root}
	if _, err := brokerRequest(cfg, request); err != nil {
		t.Fatal("ready peer blocked", err)
	}
	request.Agent = "opencode"
	if _, err := brokerRequest(cfg, request); err == nil || !strings.Contains(err.Error(), "model or runtime is not ready") {
		t.Fatal("unready selection was authorized or misreported as disabled", err)
	}
	path := filepath.Join(root, "takeover.json")
	if err := launcher.PublishEnabled(path, cfg.Selected); err != nil {
		t.Fatal(err)
	}
	selected, err := launcher.EnabledAgents(path)
	if err != nil || !selected["opencode"] || !selected["claude"] {
		t.Fatal("unready agent silently changed to local", selected, err)
	}
}

func TestAgentTakeoverDiscoversEachSessionProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := brokerConfiguration{Token: "test", RuntimeID: "runtime", Device: "device", Agents: []string{"claude"}, AutoProjects: true}
	for _, name := range []string{"app", "sdk"} {
		root := filepath.Join(home, name)
		os.Mkdir(root, 0700)
		if err := exec.Command("git", "-C", root, "init", "-q").Run(); err != nil {
			t.Fatal(err)
		}
		intent := launcher.LaunchIntent{RequestID: strings.Repeat("a", 32), Agent: "claude", Directory: root}
		if req, err := brokerRequest(cfg, intent); err != nil || !req.CloudWorkspace {
			t.Fatal("agent startup must not wait for implicit sync", err)
		}
	}
	if err := exec.Command("git", "-C", home, "init", "-q").Run(); err != nil {
		t.Fatal(err)
	}
	intent := launcher.LaunchIntent{RequestID: strings.Repeat("b", 32), Agent: "claude", Directory: home}
	if request, err := brokerRequest(cfg, intent); err != nil || !request.CloudWorkspace {
		t.Fatal("home must launch without whole-home sync", err)
	}
	intent.Agent = "codex"
	intent.Directory = filepath.Join(home, "app")
	if _, err := brokerRequest(cfg, intent); err == nil {
		t.Fatal("disabled agent accepted")
	}
}

func TestNonGitSessionUsesCloudWorkspace(t *testing.T) {
	root := t.TempDir()
	cfg := brokerConfiguration{Token: "test", RuntimeID: "r", Device: "d", Agents: []string{"claude"}, AutoProjects: true}
	req, err := brokerRequest(cfg, launcher.LaunchIntent{RequestID: strings.Repeat("d", 32), Agent: "claude", Directory: root})
	if err != nil || !req.CloudWorkspace {
		t.Fatal(req, err)
	}
}
