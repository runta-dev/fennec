package cli

import "golang.org/x/sys/unix"

func fenTerminalState(descriptor int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(descriptor, unix.TCGETS)
}

func fenSetTerminalState(descriptor int, state *unix.Termios) error {
	return unix.IoctlSetTermios(descriptor, unix.TCSETS, state)
}
