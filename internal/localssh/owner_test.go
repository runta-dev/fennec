package localssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerChangeRevokesDeviceTrust(t *testing.T) {
	dir := t.TempDir()
	if err := bindLocalOwner(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"authorized_keys", "trusted_devices.json"} {
		os.WriteFile(filepath.Join(dir, n), []byte("trust"), 0600)
	}
	if err := bindLocalOwner(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "authorized_keys")); err != nil {
		t.Fatal("same owner lost pairing")
	}
	if err := bindLocalOwner(dir, "bob"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"authorized_keys", "trusted_devices.json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Fatal("account switch retained trust", n)
		}
	}
}
