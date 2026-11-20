package launcher

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"regexp"
	"sync"
	"time"

	"fennec/internal/sessionid"
)

// AttachedLabel is the herdr-core label a local terminal carries while it
// shows a cloud session (a shim taking over `claude`, or `fen attach`). Fennec
// Desktop lists such a session once, as the local terminal, rather than also as
// a cloud row. It differs from the desktop's own `fennec.cloud-view` label,
// which marks attach views the desktop may kill and reuse.
const AttachedLabel = "fennec.attached"

var localTerminalAddress = regexp.MustCompile(`^t[0-9a-z]+$`)

// markAttached labels the herdr-core terminal this process runs in, if any,
// with the cloud session it shows, without delaying the launch. The returned
// function removes the label; a label left by a crash is harmless because the
// desktop only honors it while the terminal still runs an agent.
func markAttached(sessionID string) func() {
	terminal, socket := os.Getenv("HC_TERMINAL_ID"), os.Getenv("HC_TERMINAL_SOCKET")
	if !sessionid.Valid(sessionID) || !localTerminalAddress.MatchString(terminal) || socket == "" {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		labelTerminal(socket, terminal, sessionID)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			labelTerminal(socket, terminal, "")
		})
	}
}

// labelTerminal sets (or, with an empty value, removes) AttachedLabel through
// one plain-mode herdr-core call. Failures are ignored: the label only tidies
// the desktop sidebar.
func labelTerminal(socket, terminal, value string) {
	connection, err := net.DialTimeout("unix", socket, 500*time.Millisecond)
	if err != nil {
		return
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	request, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "terminal.label",
		"params": map[string]any{"terminal": terminal, "labels": map[string]string{AttachedLabel: value}},
	})
	if _, err := connection.Write(append(request, '\n')); err != nil {
		return
	}
	_, _ = bufio.NewReader(connection).ReadString('\n')
}
