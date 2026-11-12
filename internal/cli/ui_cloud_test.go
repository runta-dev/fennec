package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionUIHidesInternalTeardownControls(t *testing.T) {
	if _, err := os.Stat("../../native/macos/UI/cloud-computer.js"); !os.IsNotExist(err) {
		t.Fatalf("internal cloud deletion UI script must not ship in production UI: %v", err)
	}
	files := []string{
		"../../native/macos/UI/index.html",
		"../../native/macos/UI/panel.js",
		"../../native/macos/Fennec.swift",
	}
	forbidden := []string{
		"cloud-computer.js",
		"cloud-delete",
		"cloud.delete",
		"ui.cloud_delete_title",
		"ui.cloud_manage",
		"uninstall-dialog",
		"uninstall.execute",
	}
	for _, path := range files {
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, token := range forbidden {
			if strings.Contains(text, token) {
				t.Fatalf("%s exposes internal teardown token %q in production UI", path, token)
			}
		}
	}
}
