package uninstall

import (
	"os"
	"path/filepath"
	"testing"
)

func fixtureApp(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, "Contents/MacOS")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "Contents/Info.plist"), []byte(`<?xml version="1.0"?><plist><dict><key>CFBundleIdentifier</key><string>`+id+`</string></dict></plist>`), 0600)
	binary := filepath.Join(dir, "fen")
	os.WriteFile(binary, []byte("fixture"), 0700)
	return binary
}
func TestDevelopmentUninstallKeepsRepositoryAndNativeCredentials(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(home, "repo")
	os.MkdirAll(repo, 0700)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module fennec\n"), 0600)
	binary := fixtureApp(t, filepath.Join(repo, "build/Fennec.app"), "com.runta.fennec")
	fixtureApp(t, filepath.Join(repo, "build/Fennec.before-update.app"), "com.runta.fennec")
	fixtureApp(t, filepath.Join(repo, "build/Fennec-unrelated.app"), "com.other.app")
	native := filepath.Join(home, ".codex/auth.json")
	os.MkdirAll(filepath.Dir(native), 0700)
	os.WriteFile(native, []byte("private-fixture"), 0600)
	plan, err := DiscoverLocal(home, binary, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Restrict the test plan to the disposable test tree, never host Applications.
	var apps []string
	for _, app := range plan.Apps {
		if filepath.Dir(app) == filepath.Join(repo, "build") {
			apps = append(apps, app)
		}
	}
	if len(apps) != 2 {
		t.Fatal("development archives not inventoried", apps)
	}
	for _, path := range plan.Data {
		os.MkdirAll(filepath.Dir(path), 0700)
	}
	owned := filepath.Join(home, ".config/fennec/settings.json")
	os.MkdirAll(filepath.Dir(owned), 0700)
	os.WriteFile(owned, []byte(`{"fixture":true}`), 0600)
	if err := plan.RemoveData(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("Fennec data was not removed")
	}
	if err := RemoveLocalApps(apps); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{native, filepath.Join(repo, "go.mod"), filepath.Join(repo, "build/Fennec-unrelated.app/Contents/Info.plist")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("unrelated data removed", path)
		}
	}
}
func TestUninstallRejectsUnreviewedDataPaths(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(home, "work")
	os.Mkdir(outside, 0700)
	p := LocalPlan{Home: home, Data: []string{outside}}
	if err := p.RemoveData(); err == nil {
		t.Fatal("unrelated path accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("unrelated path removed")
	}
}
