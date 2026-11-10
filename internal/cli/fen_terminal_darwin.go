package cli

import "golang.org/x/sys/unix"

func fenTerminalState(descriptor int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(descriptor, unix.TIOCGETA)
}

func fenSetTerminalState(descriptor int, state *unix.Termios) error {
	return unix.IoctlSetTermios(descriptor, unix.TIOCSETA, state)
}
