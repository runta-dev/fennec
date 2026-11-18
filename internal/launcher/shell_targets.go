package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ShellTargets names the user's real shell configuration: the login shell
// from the account record and ZDOTDIR and XDG_CONFIG_HOME as that login shell
// sets them, never the daemon's own environment.
type ShellTargets struct {
	Home       string
	Shell      string
	ZDotDir    string
	XDGConfig  string
	Dispatcher string
}

// ShellReport is the result of one installation: files written, and files
// left alone with the reason. A skipped file is a coverage gap to report, not
// an error: the cloud handoff never depends on it.
type ShellReport struct {
	Shell   string            `json:"shell"`
	Written []string          `json:"written,omitempty"`
	Skipped map[string]string `json:"skipped,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// InstallShellTargets writes Fennec's initializers and one guarded source
// line in each startup file the shell reads. zsh also gets a PATH-only line in
// .zshenv, which every zsh reads, including the non-interactive shells cloud
// agents use for local commands.
func InstallShellTargets(t ShellTargets) ShellReport {
	report := ShellReport{Shell: filepath.Base(t.Shell), Skipped: map[string]string{}}
	if !filepath.IsAbs(t.Home) {
		report.Error = "absolute home required"
		return report
	}
	name := filepath.Base(t.Shell)
	directory := filepath.Join(t.Home, ".local/share/fennec")
	shims := filepath.Join(directory, "shims")
	if name == "fish" {
		if err := installFishTargets(t, shims); err != nil {
			report.Error = err.Error()
		} else {
			report.Written = append(report.Written, fishConfigPath(t))
		}
		return report
	}
	if name != "zsh" && name != "bash" {
		report.Error = "unsupported login shell " + name
		return report
	}
	script, err := ShellIntegration(name, shims, t.Dispatcher)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		report.Error = err.Error()
		return report
	}
	initPath := filepath.Join(directory, "shell-init.sh")
	if err = writeAtomic(initPath, []byte(script), 0600); err != nil {
		report.Error = err.Error()
		return report
	}
	type target struct{ file, source string }
	targets := []target{}
	switch name {
	case "zsh":
		dir := t.Home
		if filepath.IsAbs(t.ZDotDir) {
			dir = t.ZDotDir
		}
		envScript, err := ShellInit("sh", shims)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		envPath := filepath.Join(directory, "env-init.sh")
		if err = writeAtomic(envPath, []byte(envScript), 0600); err != nil {
			report.Error = err.Error()
			return report
		}
		// ZDOTDIR is often set in ~/.zshenv itself; that file is read first.
		targets = append(targets, target{filepath.Join(t.Home, ".zshenv"), envPath})
		if dir != t.Home {
			targets = append(targets, target{filepath.Join(dir, ".zshenv"), envPath})
		}
		targets = append(targets, target{filepath.Join(dir, ".zshrc"), initPath})
	case "bash":
		targets = append(targets, target{filepath.Join(t.Home, ".bashrc"), initPath})
		profile := filepath.Join(t.Home, ".bash_profile")
		for _, candidate := range []string{".bash_profile", ".bash_login", ".profile"} {
			if _, err := os.Stat(filepath.Join(t.Home, candidate)); err == nil {
				profile = filepath.Join(t.Home, candidate)
				break
			}
		}
		targets = append(targets, target{profile, initPath})
	}
	created := []string{}
	for _, item := range targets {
		if reason := ShellFileUnwritable(item.file); reason != "" {
			report.Skipped[item.file] = reason
			continue
		}
		if _, err := os.Lstat(item.file); os.IsNotExist(err) {
			created = append(created, item.file)
		}
		if err := appendShellSource(item.file, item.source); err != nil {
			report.Skipped[item.file] = err.Error()
			continue
		}
		report.Written = append(report.Written, item.file)
	}
	_ = recordShellFiles(t.Home, report.Written)
	_ = recordCreatedShellFiles(t.Home, created)
	return report
}

func createdShellFilesPath(home string) string {
	return filepath.Join(home, ".local/share/fennec/shell-created.json")
}

func createdShellFiles(home string) []string {
	data, err := os.ReadFile(createdShellFilesPath(home))
	if err != nil || len(data) > 65536 {
		return nil
	}
	var list []string
	_ = json.Unmarshal(data, &list)
	return list
}

func recordCreatedShellFiles(home string, added []string) error {
	if len(added) == 0 {
		return nil
	}
	seen := map[string]bool{}
	list := []string{}
	for _, item := range append(createdShellFiles(home), added...) {
		if filepath.IsAbs(item) && !seen[item] {
			seen[item] = true
			list = append(list, item)
		}
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeAtomic(createdShellFilesPath(home), data, 0600)
}

// RemoveCreatedEmptyShellFiles removes the startup files Fennec created for
// its block once removing the block left them empty: the user had none.
func RemoveCreatedEmptyShellFiles(home string) {
	for _, path := range createdShellFiles(home) {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() == 0 {
			_ = os.Remove(path)
		}
	}
	_ = os.Remove(createdShellFilesPath(home))
}

// ShellFileUnwritable is why Fennec must not edit path, or "" when it may: a
// file another tool owns (a link into the Nix store), one of another user,
// one that is read-only, or a directory that can't be written.
func ShellFileUnwritable(path string) string {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if syscall.Access(filepath.Dir(path), 2) != nil {
			return "directory_read_only"
		}
		return ""
	}
	if err != nil {
		return "unreadable"
	}
	resolved := path
	if info.Mode()&os.ModeSymlink != 0 {
		if resolved, err = filepath.EvalSymlinks(path); err != nil {
			return "broken_link"
		}
		if strings.HasPrefix(resolved, "/nix/store/") {
			return "managed_by_nix"
		}
		if info, err = os.Stat(resolved); err != nil {
			return "unreadable"
		}
	}
	if !info.Mode().IsRegular() {
		return "not_a_regular_file"
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return "owned_by_another_user"
	}
	if syscall.Access(resolved, 2) != nil {
		return "read_only"
	}
	return ""
}

func fishConfigPath(t ShellTargets) string {
	config := t.XDGConfig
	if !filepath.IsAbs(config) {
		config = filepath.Join(t.Home, ".config")
	}
	return filepath.Join(config, "fish", "conf.d", "fennec.fish")
}

func installFishTargets(t ShellTargets, shims string) error {
	path := fishConfigPath(t)
	if reason := ShellFileUnwritable(path); reason != "" && reason != "directory_read_only" {
		return fmt.Errorf("%s", reason)
	}
	script, err := ShellIntegration("fish", shims, t.Dispatcher)
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(data), "# Fennec CLI integration\n") {
		return fmt.Errorf("refusing to replace unmanaged fish configuration")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = writeAtomic(path, []byte(script), 0600); err != nil {
		return err
	}
	return recordShellFiles(t.Home, []string{path})
}
