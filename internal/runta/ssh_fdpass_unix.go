//go:build darwin || linux

package runta

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

// DialAndPassSSHFD connects to host:port, applies TCP_NODELAY and TCP keepalive
// on the underlying kernel socket, and passes the file descriptor over outFD for
// OpenSSH's ProxyUseFdpass=yes protocol.
func DialAndPassSSHFD(ctx context.Context, host, port string, noDelay bool, outFD int) error {
	if host == "" || port != "22" {
		return fmt.Errorf("unsupported SSH gateway target %s:%s", host, port)
	}
	conn, err := (&net.Dialer{
		Timeout:   20 * time.Second,
		KeepAlive: 15 * time.Second,
	}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	defer conn.Close()
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("expected TCP connection")
	}
	if err := tcp.SetNoDelay(noDelay); err != nil {
		return err
	}
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(15 * time.Second)
	rawConn, err := tcp.SyscallConn()
	if err != nil {
		return err
	}
	var passErr error
	ctrlErr := rawConn.Control(func(fd uintptr) {
		rights := unix.UnixRights(int(fd))
		passErr = unix.Sendmsg(outFD, []byte{0}, rights, nil, 0)
	})
	if ctrlErr != nil {
		return ctrlErr
	}
	return passErr
}
