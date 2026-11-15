// Package installation installs a verified app bundle without requiring a
// source checkout or registering services from a temporary disk image.
package installation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fennec/internal/launchprofile"
	"fennec/internal/uninstall"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if hasTrashSegment(app) {
		return true
	}
	canonical := func(p string) string {
		if resolved, err := canonicalPath(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	app = canonical(app)
	if hasTrashSegment(app) {
		return true
	}
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

func hasTrashSegment(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == ".Trash" || part == ".Trashes" {
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
	// Warm may execute only side-effect-free startup after signature verification.
	Warm    func(context.Context, string) error
	StopUI  func(context.Context, string, string) (bool, error)
	OpenUI  func(context.Context, string) error
	Verify  func(context.Context, string) error
	Copy    func(context.Context, string, string) error
	Publish func(string, string) error
}

// Prepared owns a private, verified candidate on the destination volume. Its
// in-memory seal is never restored from disk or trusted across daemon restarts.
type Prepared struct {
	mu                                    sync.Mutex
	destination, work, incoming           string
	prior, verified                       os.FileInfo
	exists, noop, retain, applied, closed bool
	ops                                   Operations
	seal                                  treeSeal
}

func (p *Prepared) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if !p.retain && p.work != "" {
		_ = os.RemoveAll(p.work)
	}
}

func Install(ctx context.Context, source, destination string, replace bool, ops Operations) (Result, error) {
	prepared, err := Prepare(ctx, source, destination, replace, ops)
	if err != nil {
		return Result{}, err
	}
	defer prepared.Close()
	return prepared.Apply(ctx)
}

func Prepare(ctx context.Context, source, destination string, replace bool, ops Operations) (prepared *Prepared, err error) {
	defer launchprofile.Begin("update.prepare")()
	if ops.Verify == nil || ops.Copy == nil || ops.Publish == nil {
		return nil, fmt.Errorf("installation operations required")
	}
	if (ops.StopUI == nil) != (ops.OpenUI == nil) {
		return nil, fmt.Errorf("paired UI lifecycle operations required")
	}
	ops = profiledOperations(ops)
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || !strings.EqualFold(filepath.Ext(destination), ".app") {
		return nil, fmt.Errorf("absolute application paths required")
	}
	source = filepath.Clean(source)
	destination = filepath.Clean(destination)
	if id, err := uninstall.BundleID(source); err != nil || id != "com.runta.fennec" {
		return nil, fmt.Errorf("source is not Fennec")
	}
	if source == destination {
		return &Prepared{destination: destination, noop: true}, nil
	}
	if inside(source, destination) || inside(destination, source) {
		return nil, fmt.Errorf("application paths must not overlap")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Validate the private destination copy before executing anything or stopping
	// the running UI. Verifying the source as well repeats the same full scan and
	// cannot protect against source changes while it is being copied.
	parent := filepath.Dir(destination)
	resolved, err := canonicalPath(parent)
	if err != nil {
		return nil, err
	}
	destination = filepath.Join(resolved, filepath.Base(destination))
	parent = resolved
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, err
	}
	if destination == canonicalSource {
		return &Prepared{destination: destination, noop: true}, nil
	}
	if inside(canonicalSource, destination) || inside(destination, canonicalSource) {
		return nil, fmt.Errorf("application paths must not overlap")
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return nil, err
	}
	prior, err := os.Lstat(destination)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if exists {
		if !replace {
			return nil, fmt.Errorf("Fennec is already installed; confirm replacement")
		}
		if id, err := uninstall.BundleID(destination); err != nil || id != "com.runta.fennec" {
			return nil, fmt.Errorf("another application occupies this location")
		}
	}
	work, err := os.MkdirTemp(parent, ".fennec-update-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if prepared == nil {
			_ = os.RemoveAll(work)
		}
	}()
	incoming := filepath.Join(work, "incoming.app")
	if err = ops.Copy(ctx, source, incoming); err != nil {
		return nil, fmt.Errorf("application copy failed: %w", err)
	}
	if id, err := uninstall.BundleID(incoming); err != nil || id != "com.runta.fennec" {
		return nil, fmt.Errorf("copied application identity mismatch")
	}
	seal, err := verifyTree(ctx, incoming, ops.Verify)
	if err != nil {
		return nil, err
	}
	if ops.Warm != nil {
		if err := ops.Warm(ctx, incoming); err != nil {
			return nil, err
		}
		if !seal.matches(incoming) {
			seal, err = verifyTree(ctx, incoming, ops.Verify)
			if err != nil {
				return nil, err
			}
		}
	}
	verifiedIncoming, err := os.Lstat(incoming)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &Prepared{destination: destination, work: work, incoming: incoming, prior: prior, exists: exists, verified: verifiedIncoming, ops: ops, seal: seal}, nil
}
func (p *Prepared) Apply(ctx context.Context) (result Result, err error) {
	defer launchprofile.Begin("update.install")()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.applied {
		return result, fmt.Errorf("prepared installation is no longer available")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if p.noop {
		p.applied = true
		return Result{Application: p.destination}, nil
	}
	if !p.seal.matches(p.incoming) {
		return result, ErrUnverifiedApplication
	}
	destination, work, incoming, prior, exists, ops := p.destination, p.work, p.incoming, p.prior, p.exists, p.ops
	backup := filepath.Join(work, "Fennec.app")
	reopen := false
	published := false
	defer func() {
		if !reopen {
			return
		}
		restart, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		id, identityErr := uninstall.BundleID(destination)
		current, statErr := os.Lstat(destination)
		verified := published && statErr == nil && os.SameFile(p.verified, current)
		// A successful atomic rename publishes the already verified private copy.
		// Rollback reopens a different bundle and must verify that bundle separately.
		if identityErr != nil || id != "com.runta.fennec" || (!verified && ops.Verify(restart, destination) != nil) {
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
		if !p.seal.matches(incoming) {
			return result, ErrUnverifiedApplication
		}
		if err = ops.Publish(destination, backup); err != nil {
			return result, err
		}
		moved, err := os.Lstat(backup)
		id, idErr := uninstall.BundleID(backup)
		if err != nil || !os.SameFile(prior, moved) || idErr != nil || id != "com.runta.fennec" {
			if rollback := ops.Publish(backup, destination); rollback != nil {
				p.retain = true
				return Result{Backup: backup}, fmt.Errorf("destination changed; previous contents preserved at %s", backup)
			}
			return result, fmt.Errorf("destination changed during installation")
		}
	}
	if err = ops.Publish(incoming, destination); err != nil {
		if exists {
			if rollback := ops.Publish(backup, destination); rollback != nil {
				p.retain = true
				return Result{Backup: backup}, fmt.Errorf("installation interrupted; previous app preserved at %s", backup)
			}
		}
		return result, err
	}
	result.Application = destination
	published = true
	p.applied = true
	if exists {
		p.retain = true
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
