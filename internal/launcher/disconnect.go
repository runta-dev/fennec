package launcher

import (
	"golang.org/x/term"
	"io"
	"os"
	"strings"
)

const DisconnectedMessage = "Disconnected. Run fen to reattach sessions."

func disconnectMessage(styled bool) string {
	if !styled {
		return DisconnectedMessage
	}
	return "Disconnected. Run \x1b[7m fen \x1b[27m to reattach sessions."
}

func terminalDisconnectMessage() string {
	_, noColor := os.LookupEnv("NO_COLOR")
	return disconnectMessage(term.IsTerminal(int(os.Stderr.Fd())) && os.Getenv("TERM") != "dumb" && !noColor)
}

// SSH diagnostics are separate from remote PTY output. Preserve other errors.
type sshDiagnostics struct {
	writer       io.Writer
	pending      string
	disconnected bool
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
	if strings.Contains(lower, "timeout, server ") || strings.Contains(lower, "broken pipe") || strings.Contains(lower, "connection reset by peer") || strings.Contains(lower, "connection to ssh.runta.com closed") || strings.Contains(lower, "connection closed by") {
		if !w.disconnected {
			io.WriteString(w.writer, "\n"+terminalDisconnectMessage()+"\n")
			w.disconnected = true
		}
		return
	}
	io.WriteString(w.writer, line)
}
func (w *sshDiagnostics) Flush() {
	if w.pending != "" {
		w.line(w.pending)
		w.pending = ""
	}
}
