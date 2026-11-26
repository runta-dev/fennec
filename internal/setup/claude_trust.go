package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var claudeTrustMu sync.Mutex

// ClaudeProfile is the global config file Claude Code reads for configDir
// (LocalAgentHome's answer): ~/.claude.json for the default
// ~/.claude, otherwise $CLAUDE_CONFIG_DIR/.claude.json.
func ClaudeProfile(home, configDir string) string {
	if configDir == "" || filepath.Clean(configDir) == filepath.Join(home, ".claude") {
		return filepath.Join(home, ".claude.json")
	}
	return filepath.Join(configDir, ".claude.json")
}

// TrustClaudeFolders records in profile that Claude Code may work in dirs
// without its "Quick safety check" trust prompt, which an unattended queue
// session has nobody to answer. Each folder is recorded as written and with
// its links followed, since Claude Code keys projects by its real working
// directory. Every other key and value in the file is kept byte for byte;
// a file that can't be parsed (Claude Code may be mid-write) is left alone.
func TrustClaudeFolders(profile string, dirs ...string) error {
	return updateClaudeProfile(profile, false, dirs)
}

// PrepareClaudeLaunch trusts dirs like TrustClaudeFolders and also marks
// Claude Code's first-run onboarding (theme picker and sign-in intro) as
// done, so a Fennec session opens straight into the TUI. Use it only when
// the session is already signed in another way (Magpie's gateway token):
// finished onboarding also skips Claude Code's own login step.
func PrepareClaudeLaunch(profile string, dirs ...string) error {
	return updateClaudeProfile(profile, true, dirs)
}

func updateClaudeProfile(profile string, onboarded bool, dirs []string) error {
	claudeTrustMu.Lock()
	defer claudeTrustMu.Unlock()
	top := map[string]json.RawMessage{}
	b, err := os.ReadFile(profile)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &top); err != nil || top == nil {
			return fmt.Errorf("%s can't be read: %v", profile, err)
		}
	}
	projects := map[string]json.RawMessage{}
	if raw, ok := top["projects"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return fmt.Errorf("%s has projects that aren't an object", profile)
		}
	}
	changed := false
	if onboarded {
		var done bool
		if json.Unmarshal(top["hasCompletedOnboarding"], &done) != nil || !done {
			top["hasCompletedOnboarding"] = json.RawMessage("true")
			changed = true
		}
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		keys := []string{filepath.Clean(dir)}
		if real, err := filepath.EvalSymlinks(dir); err == nil && filepath.Clean(real) != keys[0] {
			keys = append(keys, filepath.Clean(real))
		}
		for _, key := range keys {
			entry := map[string]json.RawMessage{}
			if raw, ok := projects[key]; ok {
				if json.Unmarshal(raw, &entry) != nil || entry == nil {
					entry = map[string]json.RawMessage{}
				}
			}
			var trusted bool
			if json.Unmarshal(entry["hasTrustDialogAccepted"], &trusted) == nil && trusted {
				continue
			}
			entry["hasTrustDialogAccepted"] = json.RawMessage("true")
			raw, err := json.Marshal(entry)
			if err != nil {
				return err
			}
			projects[key] = raw
			changed = true
		}
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	top["projects"] = raw
	out, err := json.Marshal(top)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		return err
	}
	pretty.WriteByte('\n')
	return writeClaudeProfile(profile, pretty.Bytes())
}

func writeClaudeProfile(path string, data []byte) error {
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".claude.json-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
