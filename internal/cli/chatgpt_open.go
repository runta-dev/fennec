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

// chatgptOpen enables the cloud bridge and makes sure ChatGPT.app runs with it.
// A running instance without the bridge URL is asked to quit (SIGTERM, the
// same graceful path as Quit) and relaunched; it is never force-killed.
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
	relaunched := false
	if pid := chatgptPID(ctx); pid > 0 {
		if chatgptUsesURL(ctx, pid, state.URL) {
			exec.CommandContext(ctx, "/usr/bin/open", "-b", chatgptBundleID).Run()
			return json.NewEncoder(output).Encode(map[string]any{"pid": pid, "relaunched": false, "cloud": true})
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("unable to quit ChatGPT: %w", err)
		}
		for chatgptPID(ctx) == pid {
			select {
			case <-ctx.Done():
				return fmt.Errorf("ChatGPT did not quit; quit it and run fen chatgpt open again")
			case <-time.After(200 * time.Millisecond):
			}
		}
		relaunched = true
	}
	if err := exec.CommandContext(ctx, "/usr/bin/open", "-b", chatgptBundleID, "--env", chatgptEnvironment+"="+state.URL).Run(); err != nil {
		return fmt.Errorf("unable to open ChatGPT")
	}
	for {
		if pid := chatgptPID(ctx); pid > 0 && chatgptUsesURL(ctx, pid, state.URL) {
			return json.NewEncoder(output).Encode(map[string]any{"pid": pid, "relaunched": relaunched, "cloud": true})
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("ChatGPT did not start with the cloud connection")
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

func chatgptUsesURL(ctx context.Context, pid int, url string) bool {
	data, err := exec.CommandContext(ctx, "/bin/ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	for _, field := range strings.Fields(string(data)) {
		if field == chatgptEnvironment+"="+url {
			return true
		}
	}
	return false
}
