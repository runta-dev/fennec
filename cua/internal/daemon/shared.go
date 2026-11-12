package daemon

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Endpoint modes.
const (
	PrivateDirMode    os.FileMode = 0o700
	SharedDirMode     os.FileMode = 0o755
	PrivateSocketMode os.FileMode = 0o600
	SharedSocketMode  os.FileMode = 0o660
)

// ResolveGroup turns a socket_group setting, a group name or a numeric
// gid, into a gid. An empty, malformed or unknown group is an error naming
// it.
func ResolveGroup(group string) (int, error) {
	g := strings.TrimSpace(group)
	if g == "" {
		return 0, fmt.Errorf("socket group %q is empty", group)
	}
	if g[0] >= '0' && g[0] <= '9' {
		n, err := strconv.ParseUint(g, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("socket group %q is not a group name or number", group)
		}
		return int(n), nil
	}
	if strings.ContainsAny(g, ":/\x00 \t\n") || g[0] == '-' {
		return 0, fmt.Errorf("socket group %q is not a group name or number", group)
	}
	grp, err := user.LookupGroup(g)
	if err != nil {
		return 0, fmt.Errorf("socket group %q: no such group", group)
	}
	n, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return 0, fmt.Errorf("socket group %q has gid %q", group, grp.Gid)
	}
	return n, nil
}

// PathEntry is what the shared-socket checks read of one path component.
type PathEntry struct {
	UID  uint32
	Mode os.FileMode // permission and sticky bits
	Link bool
}

// lstatEntry reads a path component without following a link.
func lstatEntry(p string) (PathEntry, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return PathEntry{}, err
	}
	e := PathEntry{Mode: fi.Mode().Perm(), Link: fi.Mode()&os.ModeSymlink != 0}
	if fi.Mode()&os.ModeSticky != 0 {
		e.Mode |= os.ModeSticky
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		e.UID = st.Uid
	}
	return e, nil
}

// CheckSharedDir refuses a directory for a group-shared socket that someone
// else could swap the socket in: the path must be absolute with no link
// anywhere on it, the directory owned by own and writable by no one else,
// and every directory above owned by root or own and not writable by group
// or others unless sticky. The error names the directory, its owner and
// its mode.
func CheckSharedDir(dir string, own uint32) error {
	return checkSharedDir(dir, own, lstatEntry)
}

func checkSharedDir(dir string, own uint32, stat func(string) (PathEntry, error)) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("socket_group needs an absolute endpoint; %s is relative", dir)
	}
	d := filepath.Clean(dir)
	for i := 0; ; i++ {
		e, err := stat(d)
		if err != nil {
			return fmt.Errorf("socket_group: %w", err)
		}
		if e.Link {
			return fmt.Errorf("socket_group needs a path without links; %s is a link", d)
		}
		writable := e.Mode.Perm()&0o022 != 0
		if i == 0 && (e.UID != own || writable) {
			return fmt.Errorf("socket_group needs %s owned by this user (uid %d) and writable by no one else; it is uid %d mode %04o", d, own, e.UID, modeBits(e.Mode))
		}
		if i > 0 && ((e.UID != 0 && e.UID != own) || (writable && e.Mode&os.ModeSticky == 0)) {
			return fmt.Errorf("socket_group needs every directory above %s owned by root or this user and writable by no one else unless sticky; %s is uid %d mode %04o", dir, d, e.UID, modeBits(e.Mode))
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil
		}
		d = parent
	}
}

func modeBits(m os.FileMode) uint32 {
	b := uint32(m.Perm())
	if m&os.ModeSticky != 0 {
		b |= 0o1000
	}
	return b
}

// ownUID is the daemon's user (tests replace it).
var ownUID = func() uint32 { return uint32(os.Getuid()) }

// OwnerOnly decides whether a safety control (stop, resume, recording) may
// be answered for a peer running as uid peer: only the daemon's own user
// may, never a member of a shared socket group.
func OwnerOnly(peer, own uint32) error {
	if peer == own {
		return nil
	}
	return fmt.Errorf("stop, resume and recording are answered only for the daemon's own user (uid %d); this connection runs as uid %d", own, peer)
}

// DetachedAttr is how a client starts the daemon: in a session and process
// group of its own, so Ctrl-C or a hangup in the agent's terminal never
// reaches it.
func DetachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// endpointDir creates dir with mode whatever the umask, and sets that mode
// on a directory that already exists.
func endpointDir(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}
