package browserset

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// LaunchEnv silences Chromium's notice about missing Google API keys (the
// bundled build carries none) for every launch, by the setup, an agent or
// fennec-cua, as chromium.Env does for fennec-cua's own launches.
var LaunchEnv = map[string]string{"GOOGLE_API_KEY": "no", "GOOGLE_DEFAULT_CLIENT_ID": "no", "GOOGLE_DEFAULT_CLIENT_SECRET": "no"}

// seedPreferences is the profile every task starts from: no first-run or
// default-browser prompts, no translate bar, a clean last exit, downloads
// to ~/Downloads without asking (Chromium's default).
const seedPreferences = `{
  "browser": {"check_default_browser": false, "has_seen_welcome_page": true},
  "distribution": {"skip_first_run_ui": true, "suppress_first_run_default_browser_prompt": true},
  "download": {"prompt_for_download": false, "directory_upgrade": true},
  "profile": {"exit_type": "Normal", "exited_cleanly": true},
  "translate": {"enabled": false}
}
`

const seedLocalState = `{"browser": {"has_seen_welcome_page": true}}
`

// ResetProfile replaces Chromium's profile with the seed. Chromium must not
// be running.
func ResetProfile(profile string) error {
	if err := os.RemoveAll(profile); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(profile, "Default"), 0o700); err != nil {
		return err
	}
	files := map[string]string{
		"First Run":                             "",
		"Local State":                           seedLocalState,
		filepath.Join("Default", "Preferences"): seedPreferences,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(profile, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// ResetState puts the files a task depends on in their start state: an
// empty log, the seed profile and the task's own files. It touches no app.
func ResetState(env Env, d Def) error {
	if err := os.RemoveAll(env.LogDir); err != nil {
		return err
	}
	if err := os.MkdirAll(env.LogDir, 0o755); err != nil {
		return err
	}
	if err := ResetProfile(env.Profile); err != nil {
		return err
	}
	for _, a := range d.Setup {
		var err error
		switch {
		case a.Mkdir != "":
			err = os.MkdirAll(env.Expand(a.Mkdir), 0o755)
		case a.Remove != "":
			err = os.RemoveAll(env.Expand(a.Remove))
		case a.Write != nil:
			p := env.Expand(a.Write.Path)
			if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				err = os.WriteFile(p, []byte(a.Write.Text), 0o644)
			}
		}
		if err != nil {
			return fmt.Errorf("setup: %w", err)
		}
	}
	return nil
}

// pidFile records the detached server's process id, beside the log folder
// (which every setup empties).
func (e Env) pidFile() string { return strings.TrimRight(e.LogDir, "/") + ".pid" }

// StopServer ends a server a previous setup started.
func StopServer(env Env) {
	data, err := os.ReadFile(env.pidFile())
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return
	}
	if syscall.Kill(pid, syscall.SIGTERM) == nil {
		for i := 0; i < 40 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(50 * time.Millisecond)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = os.Remove(env.pidFile())
}

// StartServer starts self ("bench-pages serve") detached from the calling
// command: its own session, no inherited output, so the setup command that
// starts it can exit while it keeps serving.
func StartServer(env Env, self string) error {
	out, err := os.Create(filepath.Join(env.LogDir, "serve.log"))
	if err != nil {
		return err
	}
	defer out.Close()
	cmd := exec.Command(self, "serve", "-addr", env.Addr(), "-log", env.LogDir)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(env.pidFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return WaitServer(env, 10*time.Second)
}

// WaitServer waits until the server answers.
func WaitServer(env Env, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for {
		resp, err := client.Get(env.Base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the page server did not answer at %s: %v", env.Base, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// WaitPage waits until the server has served a page.
func WaitPage(ctx context.Context, env Env, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		log, err := ReadLog(env.LogDir)
		if err != nil {
			return err
		}
		if count(log, KindPage, path) > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Chromium did not load %s within %s", path, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Setup readies the VM for a task: Chromium quit, the launch environment
// set, the server restarted on an empty log, the seed profile and the
// task's files in place, then the task's apps opened, and Chromium on the
// start page last unless a task opens TextEdit, which then comes to the
// front.
func Setup(ctx context.Context, env Env, d Def, self string) error {
	if err := env.Desk.QuitChromium(env.App); err != nil {
		return err
	}
	if err := env.Desk.SetLaunchEnv(LaunchEnv); err != nil {
		return err
	}
	StopServer(env)
	if err := ResetState(env, d); err != nil {
		return err
	}
	if err := StartServer(env, self); err != nil {
		return err
	}
	for _, a := range d.Setup {
		if a.DeleteNotes != "" {
			if err := env.Desk.DeleteNotes(a.DeleteNotes); err != nil {
				return err
			}
		}
	}
	if err := env.Desk.OpenChromium(env.App, env.Base+d.Start); err != nil {
		return err
	}
	if err := WaitPage(ctx, env, d.Start, 60*time.Second); err != nil {
		return err
	}
	for _, a := range d.Setup {
		if a.OpenTextEdit != "" {
			if err := env.Desk.OpenTextEdit(env.Expand(a.OpenTextEdit)); err != nil {
				return err
			}
		}
	}
	return nil
}

// SeedPreferences returns the seed as a map (tests).
func SeedPreferences() (map[string]any, error) {
	var m map[string]any
	err := json.Unmarshal([]byte(seedPreferences), &m)
	return m, err
}
