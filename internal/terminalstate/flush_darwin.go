package terminalstate

import "golang.org/x/sys/unix"

func flushInput(fd int) error {
	// Darwin TIOCFLUSH takes an int pointer; FREAD (1) discards input only.
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 1)
}
