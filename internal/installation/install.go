// Package installation installs a verified app bundle without requiring a
// source checkout or registering services from a temporary disk image.
package installation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fennec/internal/uninstall"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrUnverifiedApplication = errors.New("application signature verification failed")

type Status struct {
	Application string `json:"application"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Required    bool   `json:"required"`
	ReadOnly    bool   `json:"read_only"`
	Existing    bool   `json:"existing"`
}
type Result struct {
	UIRestarted bool   `json:"ui_restarted,omitempty"`
	Application string `json:"application"`
	Backup      string `json:"backup,omitempty"`
}

func Inspect(executable, home string, installed ...string) (Status, error) {
	var result Status
	if filepath.Base(filepath.Dir(executable)) != "MacOS" {
		return result, fmt.Errorf("Fennec application bundle required")
	}
	result.Source = filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	result.Application = result.Source
	if id, err := uninstall.BundleID(result.Source); err != nil || id != "com.runta.fennec" {
		return result, fmt.Errorf("cannot verify Fennec application identity")
	}
	readonly, err := readOnlyVolume(result.Source)
	if err != nil {
		return result, err
	}
	result.ReadOnly = readonly
	result.Required = RequiresInstallation(result.Source, os.TempDir(), readonly)
	result.Destination = filepath.Join(home, "Applications/Fennec.app")
	if writableDirectory("/Applications") {
		candidate := "/Applications/Fennec.app"
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			result.Destination = candidate
		} else if id, err := uninstall.BundleID(candidate); err == nil && id == "com.runta.fennec" {
			result.Destination = candidate
		}
	}
	if _, err := os.Lstat(result.Destination); err == nil {
		result.Existing = true
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if result.Required {
		candidates := append(append([]string{}, installed...), "/Applications/Fennec.app", filepath.Join(home, "Applications/Fennec.app"))
		ops := SystemOperations()
		if ops.Verify != nil {
			for _, candidate := range candidates {
				if !filepath.IsAbs(candidate) {
					continue
				}
				candidate, err = canonicalPath(candidate)
				if err != nil {
					continue
				}
				readOnly, err := readOnlyVolume(candidate)
				if err != nil || RequiresInstallation(candidate, os.TempDir(), readOnly) {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				match := MatchingInstalledApplication(ctx, result.Source, candidate, ops.Verify)
				cancel()
				if match {
					result.Required = false
					result.Application = candidate
					break
				}
			}
		}
	}
	return result, nil
}
func RequiresInstallation(app, temp string, readonly bool) bool {
	if readonly {
		return true
	}
	canonical := func(p string) string {
		if resolved, err := canonicalPath(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	app = canonical(app)
	for _, root := range []string{temp, "/tmp", "/private/tmp"} {
		if root == "" {
			continue
		}
		relative, err := filepath.Rel(canonical(root), app)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Select an explicitly known, stable installation by signed bundle identity;
// never infer an original path from Gatekeeper's implementation details.
func MatchingInstalledApplication(ctx context.Context, source, candidate string, verify func(context.Context, string) error) bool {
	if id, err := uninstall.BundleID(candidate); err != nil || id != "com.runta.fennec" {
		return false
	}
	digest := func(path string) ([32]byte, error) {
		var result [32]byte
		f, err := os.Open(filepath.Join(path, "Contents/MacOS/Fennec"))
		if err != nil {
			return result, err
		}
		defer f.Close()
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			return result, err
		}
		copy(result[:], h.Sum(nil))
		return result, nil
	}
	a, err := digest(source)
	if err != nil {
		return false
	}
	b, err := digest(candidate)
	return err == nil && a == b && verify(ctx, candidate) == nil
}

type Operations struct {
	StopUI  func(context.Context, string, string) (bool, error)
	OpenUI  func(context.Context, string) error
	Verify  func(context.Context, string) error
	Copy    func(context.Context, string, string) error
	Publish func(string, string) error
}

func Install(ctx context.Context, source, destination string, replace bool, ops Operations) (result Result, err error) {
	if ops.Verify == nil || ops.Copy == nil || ops.Publish == nil {
		return result, fmt.Errorf("installation operations required")
	}
	if (ops.StopUI == nil) != (ops.OpenUI == nil) {
		return result, fmt.Errorf("paired UI lifecycle operations required")
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || !strings.EqualFold(filepath.Ext(destination), ".app") {
		return result, fmt.Errorf("absolute application paths required")
	}
	source = filepath.Clean(source)
	destination = filepath.Clean(destination)
	if id, err := uninstall.BundleID(source); err != nil || id != "com.runta.fennec" {
		return result, fmt.Errorf("source is not Fennec")
	}
	if source == destination {
		return Result{Application: destination}, nil
	}
	if inside(source, destination) || inside(destination, source) {
		return result, fmt.Errorf("application paths must not overlap")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := ops.Verify(ctx, source); err != nil {
		return result, ErrUnverifiedApplication
	}
	parent := filepath.Dir(destination)
	resolved, err := canonicalPath(parent)
	if err != nil {
		return result, err
	}
	destination = filepath.Join(resolved, filepath.Base(destination))
	parent = resolved
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return result, err
	}
	if destination == canonicalSource {
		return Result{Application: destination}, nil
	}
	if inside(canonicalSource, destination) || inside(destination, canonicalSource) {
		return result, fmt.Errorf("application paths must not overlap")
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return result, err
	}
	prior, err := os.Lstat(destination)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return result, err
	}
	if exists {
		if !replace {
			return result, fmt.Errorf("Fennec is already installed; confirm replacement")
		}
		if id, err := uninstall.BundleID(destination); err != nil || id != "com.runta.fennec" {
			return result, fmt.Errorf("another application occupies this location")
		}
	}
	work, err := os.MkdirTemp(parent, ".fennec-update-")
	if err != nil {
		return result, err
	}
	retain := false
	defer func() {
		if !retain {
			_ = os.RemoveAll(work)
		}
	}()
	incoming := filepath.Join(work, "incoming.app")
	if err = ops.Copy(ctx, source, incoming); err != nil {
		return result, fmt.Errorf("application copy failed: %w", err)
	}
	if id, err := uninstall.BundleID(incoming); err != nil || id != "com.runta.fennec" {
		return result, fmt.Errorf("copied application identity mismatch")
	}
	if err = ops.Verify(ctx, incoming); err != nil {
		return result, ErrUnverifiedApplication
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	backup := filepath.Join(work, "Fennec.app")
	reopen := false
	defer func() {
		if !reopen {
			return
		}
		restart, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		id, identityErr := uninstall.BundleID(destination)
		if identityErr != nil || id != "com.runta.fennec" || ops.Verify(restart, destination) != nil {
			err = errors.Join(err, fmt.Errorf("cannot reopen verified application"))
			return
		}
		openErr := ops.OpenUI(restart, destination)
		if openErr == nil {
			result.UIRestarted = true
		}
		err = errors.Join(err, openErr)
	}()
	if exists {
		current, err := os.Lstat(destination)
		if err != nil || !os.SameFile(prior, current) {
			return result, fmt.Errorf("installed application changed; retry installation")
		}
		if ops.StopUI != nil {
			reopen, err = ops.StopUI(ctx, incoming, destination)
			if err != nil {
				return result, err
			}
			if err = ctx.Err(); err != nil {
				return result, err
			}
			current, err = os.Lstat(destination)
			if err != nil || !os.SameFile(prior, current) {
				return result, fmt.Errorf("installed application changed during UI shutdown")
			}
		}
		if err = ops.Publish(destination, backup); err != nil {
			return result, err
		}
		moved, err := os.Lstat(backup)
		id, idErr := uninstall.BundleID(backup)
		if err != nil || !os.SameFile(prior, moved) || idErr != nil || id != "com.runta.fennec" {
			if rollback := ops.Publish(backup, destination); rollback != nil {
				retain = true
				return Result{Backup: backup}, fmt.Errorf("destination changed; previous contents preserved at %s", backup)
			}
			return result, fmt.Errorf("destination changed during installation")
		}
	}
	if err = ops.Publish(incoming, destination); err != nil {
		if exists {
			if rollback := ops.Publish(backup, destination); rollback != nil {
				retain = true
				return Result{Backup: backup}, fmt.Errorf("installation interrupted; previous app preserved at %s", backup)
			}
		}
		return result, err
	}
	result.Application = destination
	if exists {
		retain = true
		result.Backup = backup
	} // Keep mapped binaries of existing CLI clients intact.
	return result, nil
}
func inside(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func canonicalPath(path string) (string, error) {
	current := filepath.Clean(path)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
