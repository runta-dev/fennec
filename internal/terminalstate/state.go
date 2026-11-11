// Package terminalstate restores both terminal-emulator and kernel input state.
package terminalstate

import (
	"golang.org/x/term"
	"io"
	"os"
)

// Reset closes TUI-owned reporting modes before the shell regains input echo.
// Do not use RIS: it would reset unrelated terminal preferences and scrollback.
const Reset = "\x1b[?2026l\x1b[?9l\x1b[?1000l\x1b[?1001l\x1b[?1002l\x1b[?1003l\x1b[?1004l\x1b[?1005l\x1b[?1006l\x1b[?1015l\x1b[?1016l\x1b[?2004l\x1b[?1049l\x1b[0m\x1b[?25h"

// Restore runs after input/output workers have stopped. Queued terminal reports
// belong to the detached TUI, not to the shell or the next reconnect attempt.
func Restore(input *os.File, output io.Writer, state *term.State) error {
	_, outputErr := io.WriteString(output, Reset)
	flushErr := flushInput(int(input.Fd()))
	restoreErr := term.Restore(int(input.Fd()), state)
	if restoreErr != nil {
		return restoreErr
	}
	if outputErr != nil {
		return outputErr
	}
	return flushErr
}
