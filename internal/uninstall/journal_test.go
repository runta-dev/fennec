package uninstall

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUninstallIntentSurvivesLocalDataRemoval(t *testing.T) {
	home := t.TempDir()
	r := Record{Plan: Plan{ID: "confirmed"}, Stage: "deleting_cloud"}
	if err := WriteRecord(Marker(home), r); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(home, ".config/fennec"), 0700)
	os.RemoveAll(filepath.Join(home, ".config/fennec"))
	if !Pending(home) {
		t.Fatal("uninstall guard removed before application removal")
	}
	read, err := ReadRecord(Marker(home))
	if err != nil || read.Stage != r.Stage {
		t.Fatal("recovery intent lost", err)
	}
	info, _ := os.Stat(Marker(home))
	if info.Mode().Perm() != 0600 {
		t.Fatal("uninstall record not private")
	}
}
func TestUninstallPreviewExpiresAndBindsMode(t *testing.T) {
	now := time.Now()
	p := Plan{ID: "preview", Expires: now.Add(time.Minute), DeleteCloud: true, Cloud: &CloudTarget{ID: "runtime", Owner: "owner"}}
	valid := Request{ConfirmLocal: true, DeleteCloud: true, ConfirmRuntime: "runtime"}
	if err := ConfirmPlan(p, valid, now); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmPlan(p, valid, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired confirmation accepted")
	}
	valid.DeleteCloud = false
	if err := ConfirmPlan(p, valid, now); err == nil {
		t.Fatal("scope mismatch accepted")
	}
}
