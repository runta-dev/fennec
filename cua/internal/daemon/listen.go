// Package daemon runs fennec-cua's per-user daemon: it owns the desktop, the
// model keys and the safety controls, and serves MCP to every agent session
// on a unix socket, with a separate control socket for the user's CLI and
// Fennec's UI.
package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// MaxSocketPath is the longest socket path macOS binds (sun_path holds 104
// bytes; keep room for the temporary name used while binding).
const MaxSocketPath = 101

// Paths are the daemon's files.
type Paths struct {
	Dir     string // 0700
	MCP     string // agent sessions
	Control string // stop, resume, status: owner only
	Lock    string
	Driver  string // the bundled cua-driver's socket
	Log     string
}

// DefaultPaths puts everything under ~/Library/Caches/fennec/cua, or under
// FENNEC_CUA_DIR when set.
func DefaultPaths() (Paths, error) {
	dir := os.Getenv("FENNEC_CUA_DIR")
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return Paths{}, err
		}
		dir = filepath.Join(cache, "fennec", "cua")
	}
	return PathsIn(dir), nil
}

// PathsIn lays the files out in dir.
func PathsIn(dir string) Paths {
	return Paths{
		Dir:     dir,
		MCP:     filepath.Join(dir, "cua.sock"),
		Control: filepath.Join(dir, "control.sock"),
		Lock:    filepath.Join(dir, "daemon.lock"),
		Driver:  filepath.Join(dir, "driver.sock"),
		Log:     filepath.Join(dir, "daemon.log"),
	}
}

// Check validates socket path lengths.
func (p Paths) Check() error {
	for _, s := range []string{p.MCP, p.Control, p.Driver} {
		if len(s) > MaxSocketPath {
			return fmt.Errorf("socket path %s is %d bytes; macOS allows at most %d", s, len(s), MaxSocketPath)
		}
	}
	return nil
}

// ErrRunning means another daemon holds the lock.
var ErrRunning = errors.New("another fennec-cua daemon is already serving")

// lock takes an exclusive lock on path for the life of the process; the
// error of a loser names the endpoint the winner serves. The lock file is
// 0600 whatever the umask or an earlier mode.
func lock(path, endpoint string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w on %s", ErrRunning, endpoint)
		}
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// beforeVerify runs between binding and checking the bound file (tests
// plant a link there).
var beforeVerify func(tmp string)

// listen binds a unix socket at path atomically: it binds a temporary name,
// gives it to group gid (unless gid is negative), sets mode, checks the file
// is the socket it just bound (not a planted link) and renames it into
// place.
func listen(path string, mode os.FileMode, gid int) (*net.UnixListener, error) {
	tmp := path + ".t"
	_ = os.Remove(tmp)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, err
	}
	ln.SetUnlinkOnClose(false)
	fail := func(err error) (*net.UnixListener, error) {
		_ = ln.Close()
		_ = os.Remove(tmp) // a link itself, never what it points to
		return nil, err
	}
	if beforeVerify != nil {
		beforeVerify(tmp)
	}
	// Lstat first: chmod and chown follow links, so a link planted at tmp
	// is refused before anything acts on its target.
	fi, err := os.Lstat(tmp)
	if err != nil {
		return fail(err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fail(fmt.Errorf("%s is not the socket just bound", tmp))
	}
	if gid >= 0 {
		if err := os.Lchown(tmp, -1, gid); err != nil {
			return fail(fmt.Errorf("give %s to group %d: %w", tmp, gid, err))
		}
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fail(err)
	}
	fi, err = os.Lstat(tmp)
	if err != nil {
		return fail(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != mode {
		return fail(fmt.Errorf("%s is not the socket just bound", tmp))
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return ln, nil
}

// peerUID is the user id of the process on the other end of conn.
func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var cerr error
	err = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			cerr = err
			return
		}
		uid = cred.Uid
	})
	if err != nil {
		return 0, err
	}
	return uid, cerr
}
