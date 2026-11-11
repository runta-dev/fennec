package runtime

import (
	_ "embed"
	"encoding/json"
	"fennec/internal/localssh"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

//go:embed fennec_prompt.md
var fennecPromptTemplate string

// FennecPrompt contains the complete startup context in the harness-readable
// instructions. No separate metadata file needs to be discovered by the agent.
func FennecPrompt(home string, request LocalLaunch) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	quote := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	return strings.NewReplacer("{{SESSION}}", quote(request.ID), "{{CLOUD_DIRECTORY}}", quote(filepath.Join(home, ".fen", request.ID)), "{{SSH_HOST}}", quote(localssh.RegisteredAlias(home, request.Device)), "{{LOCAL_DIRECTORY}}", quote(request.LocalDirectory)).Replace(fennecPromptTemplate), nil
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
