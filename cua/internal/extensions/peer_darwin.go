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
		pid, cerr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, err
	}
	return pid, cerr
}
