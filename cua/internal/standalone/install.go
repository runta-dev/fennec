package standalone

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// QuarantineAttr is the attribute Gatekeeper checks on downloaded files.
const QuarantineAttr = "com.apple.quarantine"

// Installer installs and uninstalls the app for one user. Nothing it does
// needs administrator rights or touches another user's files.
type Installer struct {
	Layout Layout
	// Copy copies an app bundle (ditto: keeps signatures, links and
	// attributes); Verify checks a bundle's code signature. Tests replace
	// them.
	Copy   func(src, dst string) error
	Verify func(app string) error
	// StopDaemon stops a running daemon before its files change; nil
	// means none runs.
	StopDaemon func() error
	// Log receives one line per step.
	Log func(format string, args ...any)
}

// Ditto copies a bundle with /usr/bin/ditto.
func Ditto(src, dst string) error {
	if out, err := exec.Command("/usr/bin/ditto", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("ditto: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Codesign verifies a bundle's signature (the bundle and the code it seals,
// without walking every nested helper).
func Codesign(app string) error {
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("the signature of %s does not verify: %v: %s", app, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (in *Installer) logf(format string, args ...any) {
	if in.Log != nil {
		in.Log(format, args...)
	}
}

// InstallResult says what an install did.
type InstallResult struct {
	App  string
	Link string
	// Copied is false when the source already was the installed app.
	Copied bool
	// Unquarantined counts files whose quarantine attribute was removed.
	Unquarantined int
	// OnPath is false when the link's directory is not on PATH.
	OnPath bool
}

// Install copies the app at src into the layout, clears its quarantine
// attribute, and links the fennec-cua command. An app already installed
// there is replaced only after the new copy verifies, and only once the
// daemon has stopped.
func (in *Installer) Install(src string, path string) (InstallResult, error) {
	res := InstallResult{App: in.Layout.App(), Link: in.Layout.Link()}
	if !IsApp(src) {
		return res, fmt.Errorf("%s is not the %s app; run install from the extracted archive's %s", src, DisplayName, AppName)
	}
	same, err := samePath(src, res.App)
	if err != nil {
		return res, err
	}
	if !same {
		if err := os.MkdirAll(in.Layout.AppDir, 0o755); err != nil {
			return res, err
		}
		staging := res.App + ".installing"
		if err := os.RemoveAll(staging); err != nil {
			return res, err
		}
		in.logf("copying %s to %s", src, res.App)
		if err := in.Copy(src, staging); err != nil {
			_ = os.RemoveAll(staging)
			return res, err
		}
		n, err := Unquarantine(staging)
		if err != nil {
			_ = os.RemoveAll(staging)
			return res, err
		}
		res.Unquarantined = n
		if err := in.Verify(staging); err != nil {
			_ = os.RemoveAll(staging)
			return res, err
		}
		if in.StopDaemon != nil {
			if err := in.StopDaemon(); err != nil {
				_ = os.RemoveAll(staging)
				return res, fmt.Errorf("stop the running daemon first: %w", err)
			}
		}
		old := res.App + ".previous"
		_ = os.RemoveAll(old)
		if _, err := os.Lstat(res.App); err == nil {
			if err := os.Rename(res.App, old); err != nil {
				_ = os.RemoveAll(staging)
				return res, err
			}
		}
		if err := os.Rename(staging, res.App); err != nil {
			_ = os.Rename(old, res.App)
			return res, err
		}
		_ = os.RemoveAll(old)
		res.Copied = true
	} else {
		n, err := Unquarantine(res.App)
		if err != nil {
			return res, err
		}
		res.Unquarantined = n
	}
	if err := in.link(res.Link, in.Layout.Exe()); err != nil {
		return res, err
	}
	res.OnPath = OnPath(in.Layout.BinDir, path)
	return res, nil
}

// link points the command link at exe, replacing an earlier link but never
// a file someone else put there.
func (in *Installer) link(link, exe string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a link; move it away or install with --bin-dir DIR", link)
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	in.logf("linking %s -> %s", link, exe)
	return os.Symlink(exe, link)
}

// UninstallResult says what an uninstall removed.
type UninstallResult struct {
	Removed []string
	Kept    []string
}

// Uninstall stops the daemon and removes the app and the command link
// (the link only when it points into the app). data lists the per-user
// data paths to remove too (empty keeps them).
func (in *Installer) Uninstall(data []string) (UninstallResult, error) {
	var res UninstallResult
	if in.StopDaemon != nil {
		if err := in.StopDaemon(); err != nil {
			return res, fmt.Errorf("stop the running daemon first: %w", err)
		}
	}
	app := in.Layout.App()
	link := in.Layout.Link()
	if target, err := os.Readlink(link); err == nil {
		if strings.HasPrefix(filepath.Clean(target), filepath.Clean(app)+string(filepath.Separator)) {
			if err := os.Remove(link); err != nil {
				return res, err
			}
			res.Removed = append(res.Removed, link)
		} else {
			res.Kept = append(res.Kept, link+" (points elsewhere: "+target+")")
		}
	}
	if IsApp(app) {
		if err := os.RemoveAll(app); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, app)
	} else if _, err := os.Lstat(app); err == nil {
		res.Kept = append(res.Kept, app+" (not the "+DisplayName+" app)")
	}
	for _, p := range data {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, p)
	}
	return res, nil
}

// Unquarantine removes the quarantine attribute from every file and
// directory under root (links are left alone) and counts the ones it
// removed it from.
func Unquarantine(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		switch err := unix.Removexattr(p, QuarantineAttr); {
		case err == nil:
			n++
		case errors.Is(err, unix.ENOATTR):
		default:
			return fmt.Errorf("remove the quarantine attribute from %s: %w", p, err)
		}
		return nil
	})
	return n, err
}

// Quarantined reports whether path carries the quarantine attribute.
func Quarantined(path string) bool {
	_, err := unix.Getxattr(path, QuarantineAttr, nil)
	return err == nil
}

// OnPath reports whether dir is one of path's directories.
func OnPath(dir, path string) bool {
	clean := filepath.Clean(dir)
	for _, p := range filepath.SplitList(path) {
		if p != "" && filepath.Clean(p) == clean {
			return true
		}
	}
	return false
}

func samePath(a, b string) (bool, error) {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false, err
	}
	rb, err := filepath.EvalSymlinks(b)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return filepath.Clean(ra) == filepath.Clean(rb), nil
}

// CopyTree copies the regular files and directories under src to dst,
// replacing dst. It is meant for small trees such as the skill.
func CopyTree(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o644)
		}
		return nil
	})
}
