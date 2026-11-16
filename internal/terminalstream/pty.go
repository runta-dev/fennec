package terminalstream

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// PTY setup calls File.Fd, which disables the original File's runtime poller.
// Give a nonblocking duplicate its own poller registration instead of parking
// an OS thread in poll for each read/write. The caller still owns the original.
func pollablePTY(original *os.File) (*os.File, int, error) {
	fd, err := unix.FcntlInt(original.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, -1, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, -1, err
	}
	file := os.NewFile(uintptr(fd), original.Name())
	// Reject an unsupported poller registration rather than silently falling
	// back to IO that cannot be interrupted when an attachment is canceled.
	if err := file.SetDeadline(time.Time{}); err != nil {
		file.Close()
		return nil, -1, err
	}
	// Keep this fd for ioctl only. Calling File.Fd during IO can disable
	// polling; File.Close owns cancellation and descriptor reuse protection.
	return file, fd, nil
}
