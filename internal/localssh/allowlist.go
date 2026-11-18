package localssh

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// User overrides of auto-review denials, like turning a denied app back on in
// macOS Privacy settings: the user allows a command from the Activity list and
// the same command from the same paired device then runs without review.
// Grants are keyed by a hash of device and command, so the store never holds
// the raw command, and are cleared together with device trust.

const allowListFile = "allowed_commands.json"

var allowListMu sync.Mutex

// GrantKey identifies one command from one paired device.
func GrantKey(fingerprint, command string) string {
	if fingerprint == "" || command == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fingerprint + "\n" + command))
	return hex.EncodeToString(sum[:])
}

func readAllowList(directory string) (map[string]bool, error) {
	allowed := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(directory, allowListFile))
	if errors.Is(err, os.ErrNotExist) {
		return allowed, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(data, &allowed) != nil {
		return nil, fmt.Errorf("invalid allow list")
	}
	return allowed, nil
}

// Granted reports whether the user allowed this grant key.
func Granted(directory, key string) bool {
	if directory == "" || key == "" {
		return false
	}
	allowListMu.Lock()
	defer allowListMu.Unlock()
	allowed, err := readAllowList(directory)
	return err == nil && allowed[key]
}

// AllowGrant records the user's decision to allow a previously refused command.
func AllowGrant(directory, key string) error {
	if directory == "" || len(key) != sha256.Size*2 {
		return fmt.Errorf("invalid grant")
	}
	allowListMu.Lock()
	defer allowListMu.Unlock()
	allowed, err := readAllowList(directory)
	if err != nil {
		return err
	}
	allowed[key] = true
	data, _ := json.Marshal(allowed)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "allow-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, allowListFile))
}

func clearAllowList(directory string) error {
	allowListMu.Lock()
	defer allowListMu.Unlock()
	err := os.Remove(filepath.Join(directory, allowListFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
