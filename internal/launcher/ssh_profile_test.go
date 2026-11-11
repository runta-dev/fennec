package launcher

import (
	"strings"
	"testing"
	"time"
)

func TestSSHProfilePersistsOnlyFixedPhases(t *testing.T) {
	stages := []string{}
	writer := sshPhaseWriter{started: time.Now(), record: func(stage string, _ time.Time, _ time.Duration) { stages = append(stages, stage) }}
	raw := "debug1: Connecting to private-host [192.0.2.1] port 22.\ndebug1: Executing proxy command: proxy --token secret\ndebug1: Connection established.\nAuthenticated to private-host using publickey.\nAuthenticated to private-host using publickey.\n"
	for _, b := range []byte(raw) {
		writer.Write([]byte{b})
	}
	if strings.Join(stages, ",") != "ssh.connect_begin,ssh.tcp_connected,ssh.authenticated" {
		t.Fatal(stages)
	}
	writer.Write([]byte(strings.Repeat("x", 20000) + "Authenticated to secret\n"))
	if len(stages) != 3 {
		t.Fatal("oversized diagnostics treated as phase")
	}
}
