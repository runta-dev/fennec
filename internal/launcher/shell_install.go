package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InstallShellIntegration appends one managed source block, preserving user rc
// content. Existing shells are not rewritten or injected into.
func InstallShellIntegration(home, shell string) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("absolute home required")
	}
	name := filepath.Base(shell)
	rc := ""
	switch name {
	case "zsh":
		rc = ".zshrc"
	case "bash":
		rc = ".bashrc"
	case "fish":
		return installFishIntegration(home)
	default:
		return fmt.Errorf("unsupported login shell")
	}
	directory := filepath.Join(home, ".local/share/fennec")
	script, err := ShellInit(name, filepath.Join(directory, "shims"))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	initPath := filepath.Join(directory, "shell-init.sh")
	if err = os.WriteFile(initPath, []byte(script), 0600); err != nil {
		return err
	}
	rcDirectory := home
	if name == "zsh" && os.Getenv("ZDOTDIR") != "" {
		rcDirectory = os.Getenv("ZDOTDIR")
		if !filepath.IsAbs(rcDirectory) {
			return fmt.Errorf("absolute ZDOTDIR required")
		}
	}
	paths := []string{filepath.Join(rcDirectory, rc)}
	if name == "bash" {
		profile := filepath.Join(home, ".bash_profile")
		for _, candidate := range []string{".bash_profile", ".bash_login", ".profile"} {
			path := filepath.Join(home, candidate)
			if _, err := os.Stat(path); err == nil {
				profile = path
				break
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		paths = append(paths, profile)
	}
	for _, path := range paths {
		if err := appendShellSource(path, initPath); err != nil {
			return err
		}
	}
	_ = recordShellFiles(home, paths)
	return nil
}

func appendShellSource(path, initPath string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	start, end := "# >>> Fennec CLI integration >>>", "# <<< Fennec CLI integration <<<"
	if strings.Contains(string(data), start) || strings.Contains(string(data), end) {
		if strings.Count(string(data), start) != 1 || strings.Count(string(data), end) != 1 || strings.Index(string(data), start) > strings.Index(string(data), end) {
			return fmt.Errorf("malformed Fennec shell integration block")
		}
		return nil
	}
	quote := "'" + strings.ReplaceAll(initPath, "'", "'\"'\"'") + "'"
	block := "\n" + start + "\n[ ! -r " + quote + " ] || . " + quote + "\n" + end + "\n"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(block)
	return err
}

func installFishIntegration(home string) error {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(config) {
		return fmt.Errorf("absolute XDG_CONFIG_HOME required")
	}
	dir := filepath.Join(config, "fish", "conf.d")
	path := filepath.Join(dir, "fennec.fish")
	script, err := ShellInit("fish", filepath.Join(home, ".local/share/fennec/shims"))
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(path); err == nil {
		if !strings.HasPrefix(string(data), "# Fennec CLI integration\n") {
			return fmt.Errorf("refusing to replace unmanaged fish configuration")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".fennec-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.WriteString(script)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	_ = recordShellFiles(home, []string{path})
	return nil
}

func shellFilesManifestPath(home string) string {
	return filepath.Join(home, ".local/share/fennec/shell-files.json")
}

// RecordedShellFiles returns previously modified shell startup files so cleanup
// still restores custom ZDOTDIR or XDG_CONFIG_HOME paths when env vars differ.
func RecordedShellFiles(home string) []string {
	if !filepath.IsAbs(home) {
		return nil
	}
	data, err := os.ReadFile(shellFilesManifestPath(home))
	if err != nil || len(data) > 65536 {
		return nil
	}
	var recorded []string
	if json.Unmarshal(data, &recorded) != nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(recorded))
	for _, item := range recorded {
		cleaned := filepath.Clean(item)
		if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) || strings.ContainsAny(cleaned, "\x00\r\n") || seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	sort.Strings(out)
	return out
}

func recordShellFiles(home string, added []string) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("absolute home required")
	}
	merged := map[string]bool{}
	for _, existing := range RecordedShellFiles(home) {
		merged[existing] = true
	}
	for _, item := range added {
		cleaned := filepath.Clean(item)
		if filepath.IsAbs(cleaned) && cleaned != string(filepath.Separator) && !strings.ContainsAny(cleaned, "\x00\r\n") {
			merged[cleaned] = true
		}
	}
	list := make([]string, 0, len(merged))
	for item := range merged {
		list = append(list, item)
	}
	sort.Strings(list)
	manifest := shellFilesManifestPath(home)
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(manifest), ".shell-files-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), manifest)
}
