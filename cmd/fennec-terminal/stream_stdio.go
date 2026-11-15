package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// Inherited SSH stdio is blocking and os.NewFile does not register blocking
// inherited descriptors with Go's poller. Closing those files cannot interrupt
// an outstanding read/write. Duplicate them in nonblocking mode before handing
// them to the protocol server so cancellation can retire a half-open channel.
func streamPipe(file *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}
