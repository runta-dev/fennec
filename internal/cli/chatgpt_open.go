package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// chatgptOpen enables the cloud bridge and makes sure ChatGPT.app runs with it,
// starting it when it is not running.
func chatgptOpen(output io.Writer) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("ChatGPT.app is only available on macOS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := ensureControl(ctx); err != nil {
		return err
	}
	socket, err := controlSocket()
	if err != nil {
		return err
	}
	var state struct {
		URL string `json:"url"`
	}
	if err := (control.Client{Socket: socket}).Call(ctx, "chatgpt.set", map[string]any{"enabled": true}, &state); err != nil {
		return err
	}
	if state.URL == "" {
		return fmt.Errorf("ChatGPT cloud connection is unavailable")
	}
	relaunched, err := restartChatGPT(ctx, state.URL)
	if err != nil {
		return err
	}
	if chatgptPID(ctx) == 0 {
		if err := launchChatGPT(ctx, state.URL); err != nil {
			return err
		}
	} else if !relaunched {
		exec.CommandContext(ctx, "/usr/bin/open", "-b", chatgptBundleID).Run()
	}
	pid, err := waitChatGPT(ctx, state.URL)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]any{"pid": pid, "relaunched": relaunched, "cloud": true})
}

// restartChatGPT applies url (empty means the bundled local app-server) to a
// running ChatGPT.app. A mismatched instance is asked to quit with SIGTERM, the
// same graceful path as Quit, never force-killed, then reopened. It does
// nothing when ChatGPT is not running or already uses url.
func restartChatGPT(ctx context.Context, url string) (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, nil
	}
	pid := chatgptPID(ctx)
	if pid == 0 || chatgptEnvironmentMatches(ctx, pid, url) {
		return false, nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return false, fmt.Errorf("unable to quit ChatGPT: %w", err)
	}
	for chatgptPID(ctx) == pid {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("ChatGPT did not quit; quit it and reopen")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := launchChatGPT(ctx, url); err != nil {
		return true, err
	}
	_, err := waitChatGPT(ctx, url)
	return true, err
}

func launchChatGPT(ctx context.Context, url string) error {
	arguments := []string{"-b", chatgptBundleID}
	if url != "" {
		arguments = append(arguments, "--env", chatgptEnvironment+"="+url)
	}
	if err := exec.CommandContext(ctx, "/usr/bin/open", arguments...).Run(); err != nil {
		return fmt.Errorf("unable to open ChatGPT")
	}
	return nil
}

func waitChatGPT(ctx context.Context, url string) (int, error) {
	for {
		if pid := chatgptPID(ctx); pid > 0 && chatgptEnvironmentMatches(ctx, pid, url) {
			return pid, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("ChatGPT did not start with the selected location")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func chatgptPID(ctx context.Context) int {
	data, err := exec.CommandContext(ctx, "/usr/bin/lsappinfo", "info", "-only", "pid", "-app", chatgptBundleID).Output()
	if err != nil {
		return 0
	}
	_, value, ok := strings.Cut(strings.TrimSpace(string(data)), "=")
	if !ok {
		return 0
	}
	pid, _ := strconv.Atoi(value)
	return pid
}

// chatgptEnvironmentMatches reports whether the process carries exactly url, or
// no bridge URL at all when url is empty.
func chatgptEnvironmentMatches(ctx context.Context, pid int, url string) bool {
	data, err := exec.CommandContext(ctx, "/bin/ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	current := ""
	for _, field := range strings.Fields(string(data)) {
		if value, ok := strings.CutPrefix(field, chatgptEnvironment+"="); ok {
			current = value
		}
	}
	return current == url
}
