package launcher

import (
	"fennec/internal/launchprofile"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Only fixed stage names enter the profiler. Raw SSH diagnostics may contain
// user configuration (including ProxyCommand arguments), so never persist them.
type sshPhaseWriter struct {
	started time.Time
	pending []byte
	discard bool
	seen    map[string]bool
	record  func(string, time.Time, time.Duration)
}

func (w *sshPhaseWriter) Write(data []byte) (int, error) {
	size := len(data)
	for _, b := range data {
		if b == '\n' {
			if !w.discard {
				w.line(string(w.pending))
			}
			w.pending = nil
			w.discard = false
			continue
		}
		if w.discard {
			continue
		}
		if len(w.pending) >= 16384 {
			w.pending = nil
			w.discard = true
			continue
		}
		w.pending = append(w.pending, b)
	}
	return size, nil
}
func (w *sshPhaseWriter) line(line string) {
	stages := []struct{ text, stage string }{
		{"debug1: Connecting to ", "ssh.connect_begin"},
		{"debug1: Connection established.", "ssh.tcp_connected"},
		{"debug1: Remote protocol version ", "ssh.banner_received"},
		{"debug1: SSH2_MSG_KEXINIT sent", "ssh.kex_sent"},
		{"debug1: SSH2_MSG_NEWKEYS received", "ssh.keys_ready"},
		{"debug1: SSH2_MSG_SERVICE_ACCEPT received", "ssh.auth_service_ready"},
		{"debug1: Offering public key:", "ssh.key_offered"},
		{"debug1: Server accepts key:", "ssh.key_accepted"},
		{"Authenticated to ", "ssh.authenticated"},
	}
	for _, item := range stages {
		if strings.HasPrefix(line, item.text) {
			if w.seen == nil {
				w.seen = map[string]bool{}
			}
			if !w.seen[item.stage] {
				w.seen[item.stage] = true
				w.record(item.stage, w.started, time.Since(w.started))
			}
			return
		}
	}
}
func attachSSHProfile(command *exec.Cmd, home string, started time.Time) (func(), error) {
	if _, err := os.Stat(filepath.Join(home, ".config/fennec/launch-profile.enabled")); err != nil {
		return func() {}, nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	command.Args = append(command.Args[:1], append([]string{"-v"}, command.Args[1:]...)...)
	// Giving exec an actual descriptor avoids Wait waiting on a background SSH
	// master's inherited stderr. We own and close the draining goroutine instead.
	command.Stderr = writer
	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(&sshPhaseWriter{started: started, record: launchprofile.Record}, reader)
	}()
	var once sync.Once
	return func() { once.Do(func() { writer.Close(); reader.Close(); <-done }) }, nil
}
