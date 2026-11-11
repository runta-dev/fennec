package launcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type entryRecord struct {
	Link           string `json:"link"`
	Target         string `json:"target"`
	Wrapper        string `json:"wrapper"`
	PendingWrapper string `json:"pending_wrapper,omitempty"`
}

var errManagedLauncher = errors.New("managed launcher is not an original agent executable")

// InstallEntry replaces only an executable symlink. Its target stays in place.
// The sidecar is written exclusively before replacement, enabling recovery.
func InstallEntry(path, daemon, agent string) error {
	return withEntryLock(path, func() error { return installEntry(path, daemon, agent) })
}
func installEntry(path, daemon, agent string) error {
	if !Supported[agent] || !filepath.IsAbs(path) || !filepath.IsAbs(daemon) {
		return fmt.Errorf("invalid entry installation")
	}
	if data, err := os.ReadFile(path + ".fennec-original.json"); err == nil {
		var old entryRecord
		if json.Unmarshal(data, &old) != nil {
			return fmt.Errorf("invalid entry backup")
		}
		current, _ := os.ReadFile(path)
		if old.PendingWrapper != "" && bytes.Equal(current, []byte(old.PendingWrapper)) {
			old.Wrapper = old.PendingWrapper
			old.PendingWrapper = ""
			if err := writeEntryRecord(path, old); err != nil {
				return err
			}
		}
		if bytes.Equal(current, []byte(old.Wrapper)) {
			if old.Wrapper != entryScript(daemon, agent, old.Target) {
				return fmt.Errorf("entry points to a different daemon; restore before reinstalling")
			}
			return nil
		}
		link, linkErr := os.Readlink(path)
		target, targetErr := filepath.EvalSymlinks(path)
		if linkErr == nil && targetErr == nil && link == old.Link && target == old.Target {
			return publishEntry(path, old.Link, old.Wrapper)
		}
		return fmt.Errorf("entry changed since installation; reconcile before replacing")
	} else if !os.IsNotExist(err) {
		return err
	}
	link, err := os.Readlink(path)
	if err != nil {
		return fmt.Errorf("entry must be a symlink: %w", err)
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for _, p := range []string{target, daemon} {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return fmt.Errorf("entry executable unavailable")
		}
	}
	script := entryScript(daemon, agent, target)
	record, _ := json.Marshal(entryRecord{Link: link, Target: target, Wrapper: script})
	backup, err := os.OpenFile(path+".fennec-original.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = backup.Write(record)
	if err == nil {
		err = backup.Sync()
	}
	closeErr := backup.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return publishEntry(path, link, script)
}

func publishEntry(path, link, script string) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".fennec-entry-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.WriteString(script)
	if err == nil {
		err = temp.Chmod(0755)
	}
	temp.Close()
	if err != nil {
		return err
	}
	current, err := os.Readlink(path)
	if err != nil || current != link {
		return fmt.Errorf("entry changed during installation")
	}
	return os.Rename(temp.Name(), path)
}

func RestoreEntry(path string) error {
	return withEntryLock(path, func() error { return restoreEntry(path) })
}
func restoreEntry(path string) error {
	data, err := os.ReadFile(path + ".fennec-original.json")
	if err != nil {
		return err
	}
	var record entryRecord
	if json.Unmarshal(data, &record) != nil || record.Link == "" {
		return fmt.Errorf("invalid entry backup")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, []byte(record.Wrapper)) && (record.PendingWrapper == "" || !bytes.Equal(current, []byte(record.PendingWrapper))) {
		return fmt.Errorf("entry was changed by another installer; preserved")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".fennec-restore-")
	if err != nil {
		return err
	}
	name := temp.Name()
	temp.Close()
	os.Remove(name)
	defer os.Remove(name)
	if err = os.Symlink(record.Link, name); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return os.Remove(path + ".fennec-original.json")
}

// OriginalEntry resolves only an intact managed wrapper, without executing it.
func OriginalEntry(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	header := make([]byte, 128)
	n, _ := file.Read(header)
	file.Close()
	if bytes.Contains(header[:n], []byte("# Fennec managed launcher v1\n")) {
		return "", errManagedLauncher
	}
	if !bytes.Contains(header[:n], []byte("# Fennec managed entry v1\n")) {
		return path, nil
	}
	data, err := os.ReadFile(path + ".fennec-original.json")
	if err != nil {
		return "", fmt.Errorf("managed entry backup unavailable")
	}
	var record entryRecord
	if len(data) > 65536 || json.Unmarshal(data, &record) != nil || !filepath.IsAbs(record.Target) {
		return "", fmt.Errorf("invalid managed entry backup")
	}
	current, err := os.ReadFile(path)
	if err != nil || (!bytes.Equal(current, []byte(record.Wrapper)) && (record.PendingWrapper == "" || !bytes.Equal(current, []byte(record.PendingWrapper)))) {
		return "", fmt.Errorf("managed entry changed")
	}
	target, err := filepath.EvalSymlinks(record.Target)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("original executable unavailable")
	}
	if same, err := os.Stat(path); err == nil && os.SameFile(same, info) {
		return "", fmt.Errorf("recursive entry")
	}
	f, err := os.Open(target)
	if err != nil {
		return "", err
	}
	n, _ = f.Read(header)
	f.Close()
	if bytes.Contains(header[:n], []byte("# Fennec managed")) {
		return "", fmt.Errorf("nested managed entry")
	}
	return target, nil
}

func entryScript(daemon, agent, target string) string {
	quote := func(s string) string {
		return "'" + string(bytes.ReplaceAll([]byte(s), []byte("'"), []byte("'\"'\"'"))) + "'"
	}
	return "#!/bin/sh\n# Fennec managed entry v1\n" + removedApplicationFallback(daemon, target) + "exec " + quote(daemon) + " dispatch-entry " + quote(agent) + " " + quote(target) + " \"$@\"\n"
}

// A missing application is different from a failed cloud launch. Only when the
// installed executable no longer exists may a stale wrapper run the original
// agent. Announce the location change rather than silently doing cloud work locally.
func removedApplicationFallback(daemon, target string) string {
	quote := func(value string) string {
		return "'" + string(bytes.ReplaceAll([]byte(value), []byte("'"), []byte("'\"'\"'"))) + "'"
	}
	script := "if [ ! -e " + quote(daemon) + " ]; then\n"
	if target != "" {
		script += " if [ -x " + quote(target) + " ]; then\n  printf '%s\\n' 'Fennec is no longer installed here. Running locally.' >&2\n  unset FENNEC_LAUNCH_MODE\n  exec " + quote(target) + " \"$@\"\n fi\n"
	}
	return script + " printf '%s\\n' 'Fennec is no longer installed here. Reinstall Fennec or the original agent.' >&2\n exit 127\nfi\n"
}

func withEntryLock(path string, operation func() error) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute entry required")
	}
	lock, err := os.OpenFile(path+".fennec-lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}
func writeEntryRecord(path string, record entryRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".fennec-backup-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path+".fennec-original.json")
}

// ReconcileEntry upgrades only an intact Fennec-owned wrapper. An external
// installer or user edit is preserved. The pending record makes interrupted
// upgrades readable and restorable on either side of the atomic rename.
func ReconcileEntry(path, daemon, agent string) error {
	return withEntryLock(path, func() error {
		if err := installEntry(path, daemon, agent); err == nil {
			return nil
		}
		if !Supported[agent] || !filepath.IsAbs(daemon) {
			return fmt.Errorf("invalid entry reconciliation")
		}
		if info, err := os.Stat(daemon); err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return fmt.Errorf("current executable unavailable")
		}
		if _, err := OriginalEntry(path); err != nil {
			return err
		}
		data, err := os.ReadFile(path + ".fennec-original.json")
		if err != nil {
			return err
		}
		var record entryRecord
		if len(data) > 65536 || json.Unmarshal(data, &record) != nil || record.Link == "" {
			return fmt.Errorf("invalid managed backup")
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, []byte(record.Wrapper)) && (record.PendingWrapper == "" || !bytes.Equal(current, []byte(record.PendingWrapper))) {
			return fmt.Errorf("external entry modification preserved")
		}
		desired := entryScript(daemon, agent, record.Target)
		if bytes.Equal(current, []byte(desired)) {
			record.Wrapper = desired
			record.PendingWrapper = ""
			return writeEntryRecord(path, record)
		}
		record.PendingWrapper = desired
		if err := writeEntryRecord(path, record); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(path), ".fennec-entry-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if _, err = file.WriteString(desired); err != nil {
			file.Close()
			return err
		}
		if err = file.Chmod(0755); err != nil {
			file.Close()
			return err
		}
		if err = file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err = file.Close(); err != nil {
			return err
		}
		latest, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(latest, current) {
			return fmt.Errorf("entry changed during repair; preserved")
		}
		if err = os.Rename(file.Name(), path); err != nil {
			return err
		}
		record.Wrapper = desired
		record.PendingWrapper = ""
		return writeEntryRecord(path, record)
	})
}
