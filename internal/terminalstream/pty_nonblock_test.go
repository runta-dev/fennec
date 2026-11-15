package terminalstream

import (
	"context"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func TestPTYIOAndResizePreserveNonblockingMode(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	fd := int(master.Fd())
	if err = unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session := &serverSession{ctx: ctx, file: master, fd: fd, rows: 40, columns: 120}
	if _, err = slave.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	if _, err = session.read(buffer); err != nil {
		t.Fatal(err)
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.O_NONBLOCK == 0 {
		t.Fatal("PTY read restored blocking mode; stale poll readiness can freeze input and protocol heartbeats")
	}
	if err = session.write([]byte("input")); err != nil {
		t.Fatal(err)
	}
	if err = session.resizePTY(); err != nil {
		t.Fatal(err)
	}
	flags, err = unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK == 0 {
		t.Fatal("input or resize restored blocking mode")
	}
	size, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil || size.Row != 40 || size.Col != 120 {
		t.Fatal("terminal resize was not applied")
	}
}
