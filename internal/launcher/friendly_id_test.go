package launcher

import (
	"path/filepath"
	"testing"
)

func TestFriendlySessionIdentity(t *testing.T) {
	intent := LaunchIntent{RequestID: "amber-cedar", Agent: "codex", Directory: "/project"}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := AttachArgs("12345678-1234-1234-1234-123456789abc", "t1", intent.RequestID, filepath.Join(t.TempDir(), "cloud")); err != nil {
		t.Fatal(err)
	}
	if CleanSessionTitle("Task | amber-cedar", intent.RequestID) != "Task" {
		t.Fatal("title suffix retained")
	}
}
