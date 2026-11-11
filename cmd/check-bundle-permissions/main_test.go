package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundlePermissionsRejectOwnerOnlyPublishedFiles(t *testing.T) {
	for _, mode := range []os.FileMode{0700, 0600, 0666, 0644, 0755} {
		t.Run(mode.String(), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "Fennec.app")
			binary := filepath.Join(root, "Contents/MacOS/fen")
			os.MkdirAll(filepath.Dir(binary), 0755)
			os.WriteFile(binary, []byte("fixture"), mode)
			os.Chmod(binary, mode)
			if err := check(root); (err == nil) != (mode == 0755) {
				t.Fatalf("mode %o: %v", mode, err)
			}
		})
	}
}
func TestBundlePermissionCheckDoesNotFollowExternalSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Fennec.app")
	os.Mkdir(root, 0755)
	secret := filepath.Join(t.TempDir(), "private")
	os.WriteFile(secret, []byte("fixture"), 0600)
	os.Symlink(secret, filepath.Join(root, "link"))
	if err := check(root); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(secret)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private file mode changed")
	}
}
