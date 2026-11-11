package installation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func bundle(t *testing.T, path, id, version string) {
	t.Helper()
	os.MkdirAll(filepath.Join(path, "Contents/MacOS"), 0700)
	os.WriteFile(filepath.Join(path, "Contents/Info.plist"), []byte(`<plist><dict><key>CFBundleIdentifier</key><string>`+id+`</string></dict></plist>`), 0600)
	os.WriteFile(filepath.Join(path, "Contents/MacOS/fen"), []byte(version), 0700)
}
func operations() Operations {
	return Operations{Verify: func(context.Context, string) error { return nil }, Copy: func(_ context.Context, source, destination string) error {
		return os.CopyFS(destination, os.DirFS(source))
	}, Publish: SystemOperations().Publish}
}
func contents(t *testing.T, app string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(app, "Contents/MacOS/fen"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestInstallNewBundleAndPreserveSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "disk image/Fennec.app")
	dest := filepath.Join(root, "My Applications/Fennec.app")
	bundle(t, source, "com.runta.fennec", "new")
	result, err := Install(context.Background(), source, dest, false, operations())
	if err != nil {
		t.Fatal(err)
	}
	if result.Backup != "" || contents(t, result.Application) != "new" || contents(t, source) != "new" {
		t.Fatal("copy did not preserve source")
	}
}
func TestReplaceRetainsMappedOldBundle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "image/Fennec.app")
	dest := filepath.Join(root, "Applications/Fennec.app")
	bundle(t, source, "com.runta.fennec", "new")
	bundle(t, dest, "com.runta.fennec", "old")
	if _, err := Install(context.Background(), source, dest, false, operations()); err == nil {
		t.Fatal("replacement was not explicit")
	}
	result, err := Install(context.Background(), source, dest, true, operations())
	if err != nil {
		t.Fatal(err)
	}
	if contents(t, result.Application) != "new" || contents(t, result.Backup) != "old" {
		t.Fatal("old executable was overwritten")
	}
}
func TestCopyVerificationFailureLeavesInstalledAppUntouched(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "image/Fennec.app")
	dest := filepath.Join(root, "Applications/Fennec.app")
	bundle(t, source, "com.runta.fennec", "new")
	bundle(t, dest, "com.runta.fennec", "old")
	ops := operations()
	ops.Verify = func(_ context.Context, path string) error {
		if filepath.Base(path) == "incoming.app" {
			return errors.New("signature rejected")
		}
		return nil
	}
	if _, err := Install(context.Background(), source, dest, true, ops); err == nil {
		t.Fatal("bad copy published")
	}
	if contents(t, dest) != "old" {
		t.Fatal("existing installation changed")
	}
}
func TestInstallDoesNotReplaceUnrelatedApp(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "image/Fennec.app")
	dest := filepath.Join(root, "Applications/Fennec.app")
	bundle(t, source, "com.runta.fennec", "new")
	bundle(t, dest, "other.app", "personal")
	if _, err := Install(context.Background(), source, dest, true, operations()); err == nil {
		t.Fatal("unrelated application replaced")
	}
	if contents(t, dest) != "personal" {
		t.Fatal("unrelated application changed")
	}
}
func TestFailedPublicationRestoresPreviousBundle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "image/Fennec.app")
	dest := filepath.Join(root, "Applications/Fennec.app")
	bundle(t, source, "com.runta.fennec", "new")
	bundle(t, dest, "com.runta.fennec", "old")
	ops := operations()
	publish := ops.Publish
	ops.Publish = func(from, to string) error {
		if filepath.Base(from) == "incoming.app" {
			return errors.New("publication failed")
		}
		return publish(from, to)
	}
	if _, err := Install(context.Background(), source, dest, true, ops); err == nil {
		t.Fatal("failure ignored")
	}
	if contents(t, dest) != "old" {
		t.Fatal("previous installation not restored")
	}
}
func TestRollbackNeverOverwritesConcurrentDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "image/Fennec.app")
	dest := filepath.Join(root, "Applications/Fennec.app")
	bundle(t, source, "com.runta.fennec", "new")
	bundle(t, dest, "com.runta.fennec", "old")
	ops := operations()
	publish := ops.Publish
	ops.Publish = func(from, to string) error {
		if filepath.Base(from) == "incoming.app" {
			bundle(t, to, "other.app", "concurrent")
			return errors.New("destination appeared")
		}
		return publish(from, to)
	}
	result, err := Install(context.Background(), source, dest, true, ops)
	if err == nil || result.Backup == "" {
		t.Fatal("rollback conflict lost recovery")
	}
	if contents(t, dest) != "concurrent" || contents(t, result.Backup) != "old" {
		t.Fatal("concurrent or previous data lost")
	}
}
func TestInstallerRejectsRecursiveDestinationThroughSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "Fennec.app")
	bundle(t, source, "com.runta.fennec", "new")
	alias := filepath.Join(root, "alias")
	os.Symlink(source, alias)
	_, err := Install(context.Background(), source, filepath.Join(alias, "nested/Fennec.app"), false, operations())
	if err == nil {
		t.Fatal("recursive copy accepted")
	}
	if _, err := os.Stat(filepath.Join(source, "nested")); !os.IsNotExist(err) {
		t.Fatal("source directory modified")
	}
}
func TestTemporaryAndReadOnlyBundlesRequireInstallation(t *testing.T) {
	for _, row := range []struct {
		path           string
		readonly, want bool
	}{{"/Applications/Fennec.app", false, false}, {"/Volumes/Fennec/Fennec.app", true, true}, {"/Users/person/AppTranslocation/Fennec.app", false, false}, {"/tmp/stage/Fennec.app", false, true}, {"/Users/person/Tools/AppTranslocationNotes/Fennec.app", false, false}} {
		if got := RequiresInstallation(row.path, "/private/var/example/temp", row.readonly); got != row.want {
			t.Fatalf("%s: %v", row.path, got)
		}
	}
}

func TestInstalledCandidateRequiresExactSignedApplication(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.app")
	candidate := filepath.Join(root, "candidate.app")
	bundle(t, source, "com.runta.fennec", "go")
	bundle(t, candidate, "com.runta.fennec", "go")
	for _, app := range []string{source, candidate} {
		os.WriteFile(filepath.Join(app, "Contents/MacOS/Fennec"), []byte("signed-native-identity"), 0755)
	}
	calls := 0
	verify := func(context.Context, string) error { calls++; return nil }
	if !MatchingInstalledApplication(context.Background(), source, candidate, verify) || calls != 1 {
		t.Fatal("verified matching copy not recognized")
	}
	if MatchingInstalledApplication(context.Background(), source, candidate, func(context.Context, string) error { return errors.New("invalid seal") }) {
		t.Fatal("invalid signed copy accepted")
	}
	os.WriteFile(filepath.Join(candidate, "Contents/MacOS/Fennec"), []byte("different-build"), 0755)
	if MatchingInstalledApplication(context.Background(), source, candidate, verify) {
		t.Fatal("different application used for daemon startup")
	}
}
