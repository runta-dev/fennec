//go:build darwin || linux

package runta

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// DialAndPassSSHFD connects to host:port, applies TCP_NODELAY and TCP keepalive
// on the underlying kernel socket, and passes the file descriptor over outFD for
// OpenSSH's ProxyUseFdpass=yes protocol. It fails closed: allow must accept the
// destination (the Runta gateway or the paired self-hosted computer).
func DialAndPassSSHFD(ctx context.Context, host, port string, allow func(host, port string) bool, noDelay bool, outFD int) error {
	if number, err := strconv.Atoi(port); host == "" || err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port || allow == nil || !allow(host, port) {
		return fmt.Errorf("refusing unexpected SSH target %s:%s", host, port)
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
