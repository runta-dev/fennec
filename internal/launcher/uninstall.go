package launcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UninstallEntry restores an intact wrapper. If another package manager has
// already replaced it, its new executable is kept and only our sidecar removed.
func UninstallEntry(path string) error {
	err := withEntryLock(path, func() error {
		raw, err := os.ReadFile(path + ".fennec-original.json")
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var record entryRecord
		if json.Unmarshal(raw, &record) != nil || record.Link == "" || !strings.HasPrefix(record.Wrapper, "#!/bin/sh\n# Fennec managed entry v1\n") {
			return fmt.Errorf("invalid Fennec entry backup; executable preserved")
		}
		current, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if bytes.Equal(current, []byte(record.Wrapper)) || record.PendingWrapper != "" && bytes.Equal(current, []byte(record.PendingWrapper)) {
			return restoreEntry(path)
		}
		if bytes.Contains(current, []byte("# Fennec managed entry v1\n")) {
			return fmt.Errorf("modified Fennec wrapper requires review; executable preserved")
		}
		return os.Remove(path + ".fennec-original.json")
	})
	if err != nil {
		return err
	}
	if err = os.Remove(path + ".fennec-lock"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveShellBlock preserves every byte outside the uniquely delimited block.
func RemoveShellBlock(data []byte) ([]byte, error) {
	start, end := "# >>> Fennec CLI integration >>>", "# <<< Fennec CLI integration <<<"
	text := string(data)
	if !strings.Contains(text, start) && !strings.Contains(text, end) {
		return data, nil
	}
	if strings.Count(text, start) != 1 || strings.Count(text, end) != 1 {
		return nil, fmt.Errorf("ambiguous Fennec shell integration block")
	}
	a, b := strings.Index(text, start), strings.Index(text, end)
	if a > b || a > 0 && text[a-1] != '\n' || b > 0 && text[b-1] != '\n' {
		return nil, fmt.Errorf("invalid Fennec shell integration block")
	}
	finish := b + len(end)
	if finish < len(text) && text[finish] != '\n' && text[finish] != '\r' {
		return nil, fmt.Errorf("invalid Fennec shell integration delimiter")
	}
	if finish < len(text) && text[finish] == '\r' {
		finish++
	}
	if finish < len(text) && text[finish] == '\n' {
		finish++
	}
	if a > 0 {
		a--
	}
	return []byte(text[:a] + text[finish:]), nil
}
func UninstallShellFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		return UninstallShellFile(target)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("shell configuration is not a regular file; preserved")
	}
	if info.Size() > 4<<20 {
		return fmt.Errorf("shell configuration is too large; preserved")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if filepath.Base(path) == "fennec.fish" {
		if !bytes.HasPrefix(data, []byte("# Fennec CLI integration\n")) {
			return fmt.Errorf("unmanaged fish configuration preserved")
		}
		return os.Remove(path)
	}
	updated, err := RemoveShellBlock(data)
	if err != nil {
		return err
	}
	if bytes.Equal(updated, data) {
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".fennec-shell-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(info.Mode().Perm()); err == nil {
		_, err = file.Write(updated)
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	latest, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(latest, data) {
		return fmt.Errorf("shell configuration changed during uninstall; preserved")
	}
	return os.Rename(file.Name(), path)
}

// ValidateUninstallShellFile is read-only, so known conflicts are discovered
// before accepting any cloud deletion or stopping the user's local services.
func ValidateUninstallShellFile(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return fmt.Errorf("shell configuration cannot be safely updated")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if filepath.Base(path) == "fennec.fish" {
		if !bytes.HasPrefix(data, []byte("# Fennec CLI integration\n")) {
			return fmt.Errorf("unmanaged fish configuration preserved")
		}
		return nil
	}
	_, err = RemoveShellBlock(data)
	return err
}
func ValidateUninstallEntry(path string) error {
	raw, err := os.ReadFile(path + ".fennec-original.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var record entryRecord
	if json.Unmarshal(raw, &record) != nil || record.Link == "" || !strings.HasPrefix(record.Wrapper, "#!/bin/sh\n# Fennec managed entry v1\n") {
		return fmt.Errorf("invalid Fennec entry backup; executable preserved")
	}
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(current, []byte("# Fennec managed entry v1\n")) && !bytes.Equal(current, []byte(record.Wrapper)) && (record.PendingWrapper == "" || !bytes.Equal(current, []byte(record.PendingWrapper))) {
		return fmt.Errorf("modified Fennec wrapper requires review; executable preserved")
	}
	return nil
}
