package launcher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The stable dispatcher lives outside the application bundle. Shims, shell
// functions and entry wrappers call only this path, so moving or updating the
// application never requires rewriting the user's files, and a bundle moved
// to the Trash still leaves a dispatcher that runs agents locally and the
// cleanup that restores the user's environment.

// StableDir holds the dispatcher and its record of the installed application.
func StableDir(home string) string {
	return filepath.Join(home, "Library/Application Support/com.runta.fennec/bin")
}

// StableExecutable is the dispatcher every launcher calls.
func StableExecutable(home string) string { return filepath.Join(StableDir(home), "fen") }

// RelayEnv marks a command the cloud computer runs on this Mac through the
// relay. Such a command always runs a selected agent locally: dispatching it
// would send the cloud computer's own local task back to the cloud.
const RelayEnv = "FENNEC_MAC_RELAY"

// DispatchedEnv marks a process already dispatched once.
const DispatchedEnv = "FENNEC_DISPATCHED"

// RunLocally reports whether this process must run agents on this Mac.
func RunLocally() bool { return os.Getenv(RelayEnv) != "" || os.Getenv(DispatchedEnv) != "" }

type stableRecord struct {
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
}

func stableRecordPath(home string) string { return filepath.Join(StableDir(home), "application.json") }

// RegisteredExecutable is the installed application's fen recorded by the last
// refresh, or "" when none was recorded.
func RegisteredExecutable(home string) string {
	data, err := os.ReadFile(stableRecordPath(home))
	if err != nil || len(data) > 65536 {
		return ""
	}
	var record stableRecord
	if json.Unmarshal(data, &record) != nil || !filepath.IsAbs(record.Executable) {
		return ""
	}
	return record.Executable
}

// ApplicationPresent reports whether the recorded application's fen exists
// outside any Trash.
func ApplicationPresent(home string) bool {
	executable := RegisteredExecutable(home)
	if executable == "" {
		return false
	}
	for _, part := range strings.Split(executable, string(filepath.Separator)) {
		if part == ".Trash" || part == ".Trashes" {
			return false
		}
	}
	info, err := os.Stat(executable)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0
}

// RefreshStable copies executable to the stable path when its bytes differ,
// atomically, and records it as the installed application. It returns the
// stable path.
func RefreshStable(home, executable string) (string, error) {
	if !filepath.IsAbs(home) || !filepath.IsAbs(executable) {
		return "", fmt.Errorf("absolute home and executable required")
	}
	source, err := os.Open(executable)
	if err != nil {
		return "", err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("executable unavailable")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, source); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	dir := StableDir(home)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	target := StableExecutable(home)
	current, _ := os.ReadFile(stableRecordPath(home))
	var record stableRecord
	_ = json.Unmarshal(current, &record)
	if record.SHA256 != sum || !sameFileHash(target, sum) {
		if _, err = source.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		temp, err := os.CreateTemp(dir, ".fen-")
		if err != nil {
			return "", err
		}
		defer os.Remove(temp.Name())
		_, err = io.Copy(temp, source)
		if err == nil {
			err = temp.Chmod(0755)
		}
		if err == nil {
			err = temp.Sync()
		}
		if closeErr := temp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return "", err
		}
		if err = os.Rename(temp.Name(), target); err != nil {
			return "", err
		}
	}
	data, _ := json.Marshal(stableRecord{Executable: executable, SHA256: sum})
	if !bytes.Equal(current, data) {
		if err = writeAtomic(stableRecordPath(home), data, 0600); err != nil {
			return "", err
		}
	}
	return target, nil
}

func sameFileHash(path, sum string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == sum
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".fennec-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	if err == nil {
		err = temp.Chmod(mode)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
