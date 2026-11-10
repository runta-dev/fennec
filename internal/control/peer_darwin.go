package control

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func sameUser(connection *net.UnixConn) bool {
	raw, err := connection.SyscallConn()
	if err != nil {
		return false
	}
	allowed := false
	err = raw.Control(func(fd uintptr) {
		credential, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		allowed = err == nil && credential.Uid == uint32(os.Getuid())
	})
	return err == nil && allowed
}
