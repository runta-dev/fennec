package launcher

import (
	"os"
	"strings"
	"testing"
)

// A Fennec started by a dispatched agent or through the relay inherits these
// markers. The desktop window's shell tabs must not: with either set, every
// agent shim in them runs locally (RunLocally) instead of taking over.
func TestDesktopWindowDropsDispatchMarkers(t *testing.T) {
	data, err := os.ReadFile("../../native/macos/DesktopWindow.swift")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	launch := strings.Index(source, "process.environment = environment")
	if launch < 0 {
		t.Fatal("DesktopWindow no longer passes an environment to fennec-desktop")
	}
	for _, name := range []string{DispatchedEnv, RelayEnv} {
		clear := strings.Index(source, `environment["`+name+`"] = nil`)
		if clear < 0 || clear > launch {
			t.Fatalf("DesktopWindow must drop %s before starting fennec-desktop", name)
		}
	}
}
