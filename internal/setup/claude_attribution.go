package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// HideClaudeAttribution makes Claude Code in configDir leave its own
// attribution (the Co-Authored-By trailer and the "Generated with Claude
// Code" line) out of commits and pull requests: Fennec sessions commit as
// the user. It sets attribution.commit and attribution.pr to "" in
// configDir/settings.json, which Claude Code reads as "hide attribution",
// and keeps every other key byte for byte. A settings file that can't be
// parsed (Claude Code or a sync may be mid-write) is left alone, as a
// missing setting must not keep a session from starting.
func HideClaudeAttribution(configDir string) error {
	claudeTrustMu.Lock()
	defer claudeTrustMu.Unlock()
	path := filepath.Join(configDir, "settings.json")
	top := map[string]json.RawMessage{}
	b, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		if json.Unmarshal(b, &top) != nil || top == nil {
			return nil
		}
	}
	attribution := map[string]json.RawMessage{}
	if raw, ok := top["attribution"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if json.Unmarshal(raw, &attribution) != nil || attribution == nil {
			attribution = map[string]json.RawMessage{}
		}
	}
	changed := false
	for _, key := range []string{"commit", "pr"} {
		var text *string
		if json.Unmarshal(attribution[key], &text) == nil && text != nil && *text == "" {
			continue
		}
		attribution[key] = json.RawMessage(`""`)
		changed = true
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(attribution)
	if err != nil {
		return err
	}
	top["attribution"] = raw
	out, err := json.Marshal(top)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		return err
	}
	pretty.WriteByte('\n')
	return writeClaudeProfile(path, pretty.Bytes())
}
