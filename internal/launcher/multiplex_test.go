package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultiplexScopedToRuntimeAndSession(t *testing.T) {
	directory := t.TempDir()
	runtime := "01a1b016-11bc-7e63-b3d6-c4955e17b73b"
	session := strings.Repeat("a", 32)
	launch, err := LaunchSSHArgs(directory, runtime, session)
	if err != nil {
		t.Fatal(err)
	}
	attach, err := AttachArgs(runtime, "t1", session, directory)
	if err != nil {
		t.Fatal(err)
	}
	control, _ := SessionControlPath(directory, runtime, session)
	if !strings.Contains(strings.Join(launch, "\n"), control) || !strings.Contains(strings.Join(attach, "\n"), control) {
		t.Fatal("launch and attach do not share control socket")
	}
	other, _ := SessionControlPath(directory, runtime, strings.Repeat("b", 32))
	if other == control {
		t.Fatal("different cold launch shared a connection")
	}
	if err := os.Chmod(filepath.Dir(control), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LaunchSSHArgs(directory, runtime, session); err == nil {
		t.Fatal("public control socket directory accepted")
	}
}
