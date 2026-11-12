package launcher

import (
	"fennec/internal/terminalnotice"
	"golang.org/x/term"
	"io"
	"os"
	"os/exec"
	"strings"
)

const DisconnectedMessage = "Session detached.\nRun fen to manage sessions."

func disconnectMessage(styled bool) string {
	return terminalnotice.Detached("", styled)
}

func terminalDisconnectMessage(id string) string {
	_, noColor := os.LookupEnv("NO_COLOR")
	return terminalnotice.Detached(id, term.IsTerminal(int(os.Stderr.Fd())) && os.Getenv("TERM") != "dumb" && !noColor)
}

// SSH diagnostics are separate from remote PTY output. Preserve other errors.
type sshDiagnostics struct {
	sessionID     string
	writer        io.Writer
	pending       string
	disconnected  bool
	hadDiagnostic bool
}

func (w *sshDiagnostics) Write(data []byte) (int, error) {
	w.pending += string(data)
	for {
		line, rest, ok := strings.Cut(w.pending, "\n")
		if !ok {
			break
		}
		w.pending = rest
		w.line(line + "\n")
	}
	if len(w.pending) > 8192 {
		io.WriteString(w.writer, w.pending)
		w.pending = ""
	}
	return len(data), nil
}
func (w *sshDiagnostics) line(line string) {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "timeout, server ") || strings.Contains(lower, "broken pipe") || strings.Contains(lower, "connection reset by peer") || (strings.Contains(lower, "connection to ssh.") && strings.Contains(lower, " closed")) || strings.Contains(lower, "connection closed by") || strings.Contains(lower, "connection timed out") || strings.Contains(lower, "operation timed out") || strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route to host") {
		if !w.disconnected {
			io.WriteString(w.writer, "\r"+strings.ReplaceAll(terminalDisconnectMessage(w.sessionID), "\n", "\r\n")+"\r\n")
			w.disconnected = true
		}
		return
	}
	w.hadDiagnostic = true
	io.WriteString(w.writer, line)
}
func (w *sshDiagnostics) Finish(err error) {
	w.Flush()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 255 && !w.disconnected && !w.hadDiagnostic {
		w.line("Connection closed by remote host\n")
	}
}
func (w *sshDiagnostics) Flush() {
	if w.pending != "" {
		w.line(w.pending)
		w.pending = ""
	}
}
