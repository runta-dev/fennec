package runtime

import (
	_ "embed"
	"encoding/json"
	"fennec/internal/localssh"
	"fennec/internal/setup"
	"fennec/internal/tailnet"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

//go:embed fennec_prompt.md
var fennecPromptTemplate string

// primaryWorkerSection is added to the prompt only when the user picked a
// primary worker computer. The user wants it able to finish work end to end
// while This Mac is offline, so agents ask to move missing access there
// (FEN-179).
//
//go:embed fennec_primary_worker.md
var primaryWorkerSection string

// FennecPrompt contains the complete startup context in the harness-readable
// instructions. No separate metadata file needs to be discovered by the agent.
func FennecPrompt(home string, request LocalLaunch) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	quote := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	// The user can pick one worker computer for builds, compiles and tests
	// (FEN-147); without one, nothing changes for the agent.
	primary, section := "", ""
	if alias := tailnet.PrimaryAlias(home); alias != "" {
		primary = "- Primary worker computer for builds, compiles and tests: SSH alias " + quote(alias) + "\n"
		section = strings.ReplaceAll(primaryWorkerSection, "{{PRIMARY_ALIAS}}", quote(alias))
	}
	return strings.NewReplacer("{{PRIMARY_WORKER}}", primary, "{{PRIMARY_WORKER_SECTION}}", section, "{{SESSION}}", quote(request.ID), "{{CLOUD_DIRECTORY}}", quote(filepath.Join(home, ".fen", request.ID)), "{{SSH_HOST}}", quote(localssh.RegisteredAlias(home, request.Device)), "{{LOCAL_DIRECTORY}}", quote(request.LocalDirectory)).Replace(fennecPromptTemplate), nil
}
func prepareSessionWorkspace(home string, request LocalLaunch) (string, error) {
	prompt, err := FennecPrompt(home, request)
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".fen")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		return "", fmt.Errorf("session root must be a directory")
	}
	cwd := filepath.Join(root, request.ID)
	if err := os.Mkdir(cwd, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if info, err := os.Lstat(cwd); err != nil || !info.IsDir() {
		return "", fmt.Errorf("session directory must not be a symlink")
	}
	if err := writeSessionInstructions(filepath.Join(cwd, "AGENTS.md"), []byte(prompt)); err != nil {
		return "", err
	}
	// Claude's native instruction entrypoint imports the same canonical prompt.
	if request.Agent == "claude" {
		if err := writeSessionInstructions(filepath.Join(cwd, "CLAUDE.md"), []byte("@AGENTS.md\n")); err != nil {
			return "", err
		}
	}
	return cwd, nil
}
func writeSessionInstructions(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if os.IsExist(err) {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("existing session instructions are not a regular file")
		}
		existing, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if string(existing) != string(data) {
			return fmt.Errorf("existing session instructions differ; preserved")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// prepareClaudeProfile readies the Claude Code configuration the launch will
// read. It trusts the Fennec session folder and hides Claude Code's own
// commit and pull request attribution. Through Magpie the gateway token
// already signs Claude in, so first-run onboarding is marked done as well; a
// native account keeps its own onboarding state, which credential sync
// manages.
func prepareClaudeProfile(home, cwd string) error {
	native := setup.NativeAgent(home, "claude")
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if native {
		root, _, err := setup.NativeAccountRoot(home, "claude")
		if err != nil {
			return err
		}
		configDir = filepath.Join(root, "claude")
	} else if configDir == "" {
		configDir = filepath.Join(home, ".claude")
	}
	if err := setup.HideClaudeAttribution(configDir); err != nil {
		return err
	}
	profile := setup.ClaudeProfile(home, configDir)
	if native {
		return setup.TrustClaudeFolders(profile, cwd)
	}
	return setup.PrepareClaudeLaunch(profile, cwd)
}
