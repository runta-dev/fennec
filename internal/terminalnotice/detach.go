package terminalnotice

import "fennec/internal/sessionid"

// Detached is shared by the remote attachment and local SSH/stream failures.
func Detached(id string, styled bool) string {
	command := func(text string) string {
		if styled {
			return "\x1b[1;4m" + text + "\x1b[22;24m"
		}
		return text
	}
	if !sessionid.Valid(id) {
		return "Session detached.\nRun " + command("fen") + " to manage sessions."
	}
	return "Session detached.\nRun " + command("fen attach "+id) + " to reattach, or " + command("fen") + " to manage sessions."
}
