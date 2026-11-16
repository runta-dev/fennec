package terminalstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func testPollablePTY(t *testing.T) (*serverSession, *os.File) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil {
		t.Fatal(err)
	}
	file, fd, err := pollablePTY(master)
	if err != nil {
		t.Fatal(err)
	}
	master.Close()
	ctx, cancel := context.WithCancel(context.Background())
	session := &serverSession{ctx: ctx, file: file, fd: fd, rows: 40, columns: 120}
	t.Cleanup(func() { cancel(); session.closePTY() })
	return session, slave
}

func TestPTYIOAndResizePreserveNonblockingMode(t *testing.T) {
	session, slave := testPollablePTY(t)
	fd := session.fd
	var err error
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

func TestPTYCloseInterruptsBlockedIO(t *testing.T) {
	for _, direction := range []string{"read", "write"} {
		t.Run(direction, func(t *testing.T) {
			session, _ := testPollablePTY(t)
			done := make(chan error, 1)
			go func() {
				if direction == "read" {
					_, err := session.read(make([]byte, 32))
					done <- err
				} else {
					// No slave reader: exceed the input queue to block Write.
					done <- session.write(bytes.Repeat([]byte("x"), 1<<20))
				}
			}()
			select {
			case err := <-done:
				t.Fatalf("%s did not wait for readiness: %v", direction, err)
			case <-time.After(20 * time.Millisecond):
			}
			// This is the same wakeup used by watchAttachment on cancellation.
			session.closePTY()
			select {
			case err := <-done:
				if !errors.Is(err, os.ErrClosed) {
					t.Fatalf("%s close: %v", direction, err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("close did not interrupt blocked %s", direction)
			}
		})
	}
}

func TestPTYReadWakesOnOutputAndSlaveExit(t *testing.T) {
	session, slave := testPollablePTY(t)
	type result struct {
		data string
		err  error
	}
	done := make(chan result, 1)
	read := func() {
		buf := make([]byte, 128)
		n, err := session.read(buf)
		done <- result{string(buf[:n]), err}
	}
	go read()
	select {
	case r := <-done:
		t.Fatalf("read returned before output: %+v", r)
	case <-time.After(20 * time.Millisecond):
	}
	const output = "\x1b[2J\x1b[Hready\r\n"
	if _, err := slave.Write([]byte(output)); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.data != output {
			t.Fatalf("terminal bytes changed: %+v", r)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("output did not wake the reader")
	}
	go read()
	slave.Close()
	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("slave exit did not end the read")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slave exit did not wake the reader")
	}
}

func TestPTYConcurrentIOAndResizePreserveBytes(t *testing.T) {
	session, slave := testPollablePTY(t)
	payload := bytes.Repeat([]byte("\x1b[32mterminal\x1b[0m\r\n"), 16384)
	done := make(chan error, 4)
	go func() { done <- session.write(payload) }()
	go func() { _, err := slave.Write(payload); done <- err }()
	go func() {
		data := make([]byte, len(payload))
		_, err := io.ReadFull(slave, data)
		if err == nil && !bytes.Equal(data, payload) {
			err = errors.New("input bytes changed")
		}
		done <- err
	}()
	go func() {
		var data []byte
		buf := make([]byte, 4096)
		for len(data) < len(payload) {
			n, err := session.read(buf)
			data = append(data, buf[:n]...)
			if err != nil {
				done <- err
				return
			}
		}
		if !bytes.Equal(data, payload) {
			done <- errors.New("output bytes changed")
		} else {
			done <- nil
		}
	}()
	for i := range 32 {
		session.mu.Lock()
		session.rows, session.columns = uint16(40+i), uint16(120+i)
		err := session.resizePTY()
		session.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent terminal IO stalled")
		}
	}
}
