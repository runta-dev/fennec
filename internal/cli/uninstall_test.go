package cli

import (
	"fennec/internal/uninstall"
	"testing"
)

func TestUninstallProcessSelectionUsesExactPathsAndPreservesCaller(t *testing.T) {
	allowed := map[string]bool{"/Applications/Fennec.app/Contents/MacOS/fen": true, "/Users/test/My Apps/Fennec.app/Contents/MacOS/Fennec": true}
	output := "10 /Applications/Fennec.app/Contents/MacOS/fen\n11 /Users/test/My Apps/Fennec.app/Contents/MacOS/Fennec\n12 /Applications/Fennec.app/Contents/MacOS/fen\n13 /Applications/Fennec.app/Contents/MacOS/fen-other\n14 /bin/node\n15 /tmp/fennec-uninstall/worker\n"
	got := ownedUninstallProcesses(output, allowed, 12, 15)
	if len(got) != 2 || got[10] == "" || got[11] == "" {
		t.Fatal("incorrect process scope", got)
	}
}
func TestUninstallRetryCannotSubstituteCloudTarget(t *testing.T) {
	target := &uninstall.CloudTarget{ID: "confirmed", Owner: "owner", Revision: 7}
	previous := uninstall.Record{Plan: uninstall.Plan{ID: "original", DeleteCloud: true, Cloud: target}}
	current := uninstall.Plan{ID: "new-preview", ResumeID: "original", DeleteCloud: true, Cloud: target}
	if !matchesPendingUninstall(current, previous) {
		t.Fatal("valid retry rejected")
	}
	current.Cloud = &uninstall.CloudTarget{ID: "replacement", Owner: "owner", Revision: 7}
	if matchesPendingUninstall(current, previous) {
		t.Fatal("replacement runtime accepted")
	}
	current.Cloud = target
	current.DeleteCloud = false
	if matchesPendingUninstall(current, previous) {
		t.Fatal("pending cloud deletion silently abandoned")
	}
}
