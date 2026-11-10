package launcher

import (
	"strings"
	"testing"
)

func TestSSHDisconnectMessage(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out strings.Builder
	w := &sshDiagnostics{writer: &out}
	w.Write([]byte("Timeout, server ssh.run"))
	w.Write([]byte("ta.com not responding.\nclient_loop: send disconnect: Broken pipe\n"))
	w.Flush()
	if strings.Count(out.String(), DisconnectedMessage) != 1 || strings.Contains(out.String(), "Timeout") {
		t.Fatal(out.String())
	}
}

func TestDisconnectInlineCode(t *testing.T) {
	styled := disconnectMessage(true)
	if !strings.Contains(styled, "\x1b[7m fen \x1b[27m") || strings.Contains(styled, "`") {
		t.Fatal(styled)
	}
	plain := disconnectMessage(false)
	if strings.ContainsAny(plain, "\x1b`") || plain != DisconnectedMessage {
		t.Fatal(plain)
	}
}
func TestSSHOtherErrorPreserved(t *testing.T) {
	var out strings.Builder
	w := &sshDiagnostics{writer: &out}
	w.Write([]byte("Permission denied (publickey)."))
	w.Flush()
	if out.String() != "Permission denied (publickey)." {
		t.Fatal(out.String())
	}
}
