package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func command(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	process := exec.CommandContext(ctx, name, args...)
	process.Env = append(os.Environ(), "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
	out, err := process.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, out)
	}
	return out, nil
}
func run(stage string) (result map[string]any, err error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("macOS required")
	}

	home, _ := os.UserHomeDir()
	plist := filepath.Join(home, "Library/LaunchAgents/com.runta.fennec.daemon.plist")
	before, _ := os.ReadFile(plist)
	root, err := os.MkdirTemp("/tmp", "fennec-install-probe-")
	if err != nil {
		return nil, err
	}
	mounted := false
	mount := filepath.Join(root, "mount")
	defer func() {
		if mounted {
			if _, cleanup := command("/usr/bin/hdiutil", "detach", mount); cleanup != nil {
				if err == nil {
					err = cleanup
				}
				return
			}
		}
		if cleanup := os.RemoveAll(root); err == nil {
			err = cleanup
		}
	}()

	imageRoot := filepath.Join(root, "image")
	if err = os.MkdirAll(imageRoot, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(imageRoot, 0755); err != nil {
		return nil, err
	}
	if _, err = command("/usr/bin/ditto", stage, filepath.Join(imageRoot, "Fennec.app")); err != nil {
		return nil, err
	}
	image := filepath.Join(root, "fixture.dmg")
	if _, err = command("/usr/bin/hdiutil", "create", "-volname", "Fennec Install Probe", "-srcfolder", imageRoot, "-format", "UDZO", image); err != nil {
		return nil, err
	}
	if err = os.Mkdir(mount, 0700); err != nil {
		return nil, err
	}
	if _, err = command("/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-mountpoint", mount, image); err != nil {
		return nil, err
	}
	mounted = true
	if err = publicModes(mount); err != nil {
		return nil, err
	}
	binary := filepath.Join(mount, "Fennec.app/Contents/MacOS/fen")
	raw, err := command(binary, "install", "--check")
	if err != nil {
		return nil, err
	}
	var status struct {
		Required    bool   `json:"required"`
		ReadOnly    bool   `json:"read_only"`
		Source      string `json:"source"`
		Application string `json:"application"`
	}
	if json.Unmarshal(raw, &status) != nil || !status.ReadOnly || !status.Required && (status.Application == "" || status.Application == status.Source) {
		return nil, fmt.Errorf("mounted image was not classified as requiring installation")
	}
	if status.Required {
		if _, sameErr := command(binary, "install", "--destination", status.Source); sameErr == nil {
			return nil, fmt.Errorf("read-only source was incorrectly accepted as its own installation destination")
		}
	}

	target := filepath.Join(root, "isolated Applications/Fennec.app")
	raw, err = command(binary, "install", "--destination", target)
	if err != nil {
		return nil, err
	}
	var first struct {
		Application string `json:"application"`
	}
	if json.Unmarshal(raw, &first) != nil || first.Application == "" {
		return nil, fmt.Errorf("missing installed app result")
	}
	if _, err = command("/usr/bin/codesign", "--verify", "--deep", "--strict", first.Application); err != nil {
		return nil, err
	}
	raw, err = command(binary, "install", "--destination", target, "--replace")
	if err != nil {
		return nil, err
	}
	var second struct {
		Application string `json:"application"`
		Backup      string `json:"backup"`
	}
	if json.Unmarshal(raw, &second) != nil || second.Backup == "" {
		return nil, fmt.Errorf("upgrade did not preserve previous bundle")
	}
	if _, err = command("/usr/bin/codesign", "--verify", "--deep", "--strict", second.Backup); err != nil {
		return nil, err
	}
	if err = publicModes(first.Application); err != nil {
		return nil, err
	}
	after, _ := os.ReadFile(plist)
	if !bytes.Equal(before, after) {
		return nil, fmt.Errorf("existing daemon registration changed during copy-only smoke test")
	}
	return map[string]any{"system_path_only": true, "existing_installation_reused": !status.Required, "bundle_public_modes_verified": true, "readonly_dmg_detected": true, "signed_copy_verified": true, "upgrade_backup_verified": true, "existing_registration_unchanged": true, "scope": "isolated filesystem copy; no app UI, login or daemon started"}, nil
}
func main() {
	app := flag.String("app", "", "Signed Fennec.app to inspect")
	flag.Parse()
	if *app == "" {
		fmt.Fprintln(os.Stderr, "--app is required")
		os.Exit(2)
	}
	result, err := run(*app)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(result)
}

func publicModes(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		required := os.FileMode(0444)
		if entry.IsDir() || info.Mode()&0111 != 0 {
			required = 0555
		}
		if info.Mode().Perm()&required != required {
			return fmt.Errorf("copied bundle permissions restrict other users: %s", path)
		}
		return nil
	})
}
