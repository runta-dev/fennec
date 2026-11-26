package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellHome is a temporary home with a fake dispatcher that prints its
// arguments and a fake codex on a directory a version manager would own.
func shellHome(t *testing.T, shell string) (home, dispatcher, manager string) {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s not installed", shell)
	}
	home = t.TempDir()
	dispatcher = filepath.Join(home, "Library/Application Support/com.runta.fennec/bin/fen")
	if err := os.MkdirAll(filepath.Dir(dispatcher), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dispatcher, []byte("#!/bin/sh\necho DISPATCHED \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	manager = filepath.Join(home, ".local/share/mise/shims")
	if err := os.MkdirAll(manager, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager, "codex"), []byte("#!/bin/sh\necho MISE \"$@\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return home, dispatcher, manager
}

func runShell(t *testing.T, home, shell string, args ...string) string {
	t.Helper()
	cmd := exec.Command(shell, args...)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=dumb", "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("%s %v: %v\n%s", shell, args, err, out)
	}
	return string(out)
}

// A per-prompt version manager (mise activate, direnv) puts its own codex in
// front of the shims and the user's settings are strict (set -u): the
// interactive shell still reaches Fennec through the function.
func TestShellFunctionsWinOverVersionManagers(t *testing.T) {
	for _, shell := range []string{"zsh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			home, dispatcher, manager := shellHome(t, shell)
			rc := ".zshrc"
			if shell == "bash" {
				rc = ".bashrc"
			}
			// as mise activate does, before Fennec's block
			if err := os.WriteFile(filepath.Join(home, rc), []byte("set -u\nexport PATH=\""+manager+":$PATH\"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			path, _ := exec.LookPath(shell)
			report := InstallShellTargets(ShellTargets{Home: home, Shell: path, Dispatcher: dispatcher})
			if report.Error != "" || len(report.Skipped) != 0 {
				t.Fatalf("report %+v", report)
			}
			if out := runShell(t, home, path, "-i", "-c", "codex hello"); !strings.Contains(out, "DISPATCHED dispatch-stable codex hello") {
				t.Fatalf("interactive codex: %q", out)
			}
			if out := runShell(t, home, path, "-i", "-c", "agent hello"); !strings.Contains(out, "DISPATCHED dispatch-stable agent hello") {
				t.Fatal("agent alias bypassed the dispatcher:", out)
			}
			// the prompt hook moves the shims back in front of the manager
			out := runShell(t, home, path, "-i", "-c", "export PATH=\""+manager+":$PATH\"; fennec_path_front; echo \"P=$PATH\"")
			if !strings.Contains(out, "P="+filepath.Join(home, ".local/share/fennec/shims")+":") {
				t.Fatalf("path after hook: %q", out)
			}
		})
	}
}

// An alias the user chose is theirs: Fennec does not define a function over
// it (fen doctor agents reports the agent as shadowed).
func TestShellFunctionsLeaveUserAliases(t *testing.T) {
	home, dispatcher, _ := shellHome(t, "zsh")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("alias codex='echo ALIAS'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	path, _ := exec.LookPath("zsh")
	InstallShellTargets(ShellTargets{Home: home, Shell: path, Dispatcher: dispatcher})
	if out := runShell(t, home, path, "-i", "-c", "codex x"); !strings.Contains(out, "ALIAS x") || strings.Contains(out, "DISPATCHED") {
		t.Fatalf("aliased codex: %q", out)
	}
	// pi has no alias and is Fennec's
	if out := runShell(t, home, path, "-i", "-c", "pi y"); !strings.Contains(out, "DISPATCHED dispatch-stable pi y") {
		t.Fatalf("pi: %q", out)
	}
}

// Every zsh reads .zshenv: a non-interactive shell, as a cloud agent's local
// command runs, resolves the agent through the shims.
func TestZshenvCoversNonInteractiveShells(t *testing.T) {
	home, dispatcher, _ := shellHome(t, "zsh")
	path, _ := exec.LookPath("zsh")
	shims := filepath.Join(home, ".local/share/fennec/shims")
	if err := os.MkdirAll(shims, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shims, "codex"), []byte("#!/bin/sh\necho SHIM\n"), 0755); err != nil {
		t.Fatal(err)
	}
	report := InstallShellTargets(ShellTargets{Home: home, Shell: path, Dispatcher: dispatcher})
	if len(report.Written) != 2 {
		t.Fatalf("written %v", report.Written)
	}
	if out := strings.TrimSpace(runShell(t, home, path, "-c", "command -v codex")); out != filepath.Join(shims, "codex") {
		t.Fatalf("non-interactive codex: %q", out)
	}
}

// Fish gets the same functions from its conf.d file.
func TestFishFunctionsDispatch(t *testing.T) {
	home, dispatcher, _ := shellHome(t, "fish")
	path, _ := exec.LookPath("fish")
	report := InstallShellTargets(ShellTargets{Home: home, Shell: path, Dispatcher: dispatcher, XDGConfig: filepath.Join(home, ".config")})
	if report.Error != "" {
		t.Fatalf("report %+v", report)
	}
	if out := runShell(t, home, path, "-i", "-c", "codex hello"); !strings.Contains(out, "DISPATCHED dispatch-stable codex hello") {
		t.Fatalf("fish codex: %q", out)
	}
}

// Removal leaves every startup file as the user had it, byte for byte.
func TestShellTargetsRemoveToTheOriginalBytes(t *testing.T) {
	home, dispatcher, _ := shellHome(t, "zsh")
	path, _ := exec.LookPath("zsh")
	original := "# mine\nexport EDITOR=vim\n"
	for _, name := range []string{".zshrc", ".zshenv"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(original), 0640); err != nil {
			t.Fatal(err)
		}
		// Set the fixture mode explicitly instead of inheriting the runner's umask.
		if err := os.Chmod(filepath.Join(home, name), 0640); err != nil {
			t.Fatal(err)
		}
	}
	InstallShellTargets(ShellTargets{Home: home, Shell: path, Dispatcher: dispatcher})
	for _, file := range RecordedShellFiles(home) {
		if err := UninstallShellFile(file); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".zshrc", ".zshenv"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil || string(data) != original {
			t.Fatalf("%s after removal: %q %v", name, data, err)
		}
		info, _ := os.Stat(filepath.Join(home, name))
		if info.Mode().Perm() != 0640 {
			t.Fatalf("%s mode %v", name, info.Mode())
		}
	}
}

// A file another tool owns is reported, never written.
func TestShellFileUnwritableReasons(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, ".zshrc")
	if err := os.Symlink("/nix/store/does-not-exist-zshrc", link); err != nil {
		t.Fatal(err)
	}
	if got := ShellFileUnwritable(link); got != "broken_link" {
		t.Fatalf("broken link: %q", got)
	}
	if got := ShellFileUnwritable(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("missing file in writable dir: %q", got)
	}
	if err := os.Mkdir(filepath.Join(dir, "d"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := ShellFileUnwritable(filepath.Join(dir, "d")); got != "not_a_regular_file" {
		t.Fatalf("directory: %q", got)
	}
}
