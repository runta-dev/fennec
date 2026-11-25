package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// The starter queue's copy comes from the UI catalog; a renamed key must not
// leave a new board with an empty queue name.
func TestStarterQueueCopyComesFromCatalog(t *testing.T) {
	copyFor := func(language string) (string, string) {
		home := t.TempDir()
		dir := filepath.Join(home, ".config/fennec")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "desktop-settings.json"), []byte(`{"language":"`+language+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return starterQueueCopy(home)
	}
	name, description := copyFor("en-US")
	if name != "My tasks" || description == "" {
		t.Fatalf("en-US starter copy = %q, %q", name, description)
	}
	zhName, zhDescription := copyFor("zh-CN")
	if zhName == "" || zhName == name || zhDescription == "" || zhDescription == description {
		t.Fatalf("zh-CN starter copy = %q, %q", zhName, zhDescription)
	}
}
