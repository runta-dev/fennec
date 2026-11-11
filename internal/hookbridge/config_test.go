package hookbridge

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDuplicateHookSelectorsRejected(t *testing.T) {
	d := Definition{Name: "one", Agent: "codex", Event: "PreToolUse", Command: "same", Location: "sandbox"}
	second := d
	second.Name = "two"
	raw, _ := json.Marshal(Config{Version: 1, Hooks: []Definition{d, second}})
	if _, err := ParseConfig(raw); err == nil {
		t.Fatal("ambiguous selector accepted")
	}
}
func TestUnavailableUnrelatedHookDoesNotBlockCatalog(t *testing.T) {
	home := t.TempDir()
	d := Definition{Name: "missing", Agent: "codex", Event: "PreToolUse", Command: "missing", Location: "local", Executable: filepath.Join(home, "absent"), Directory: home}
	if err := SaveConfig(home, Config{Version: 1, Hooks: []Definition{d}}); err != nil {
		t.Fatal(err)
	}
	entries, err := ConfigExecutors(home)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}
