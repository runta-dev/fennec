//go:build !darwin

package extensions

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID is the process on the other end of conn.
func peerPID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if cred != nil {
			pid = int(cred.Pid)
		}
	}); err != nil {
		return 0, err
	}
	return pid, cerr
}
