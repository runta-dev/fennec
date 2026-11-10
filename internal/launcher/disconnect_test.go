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
	if strings.Count(strings.ReplaceAll(out.String(), "\r", ""), DisconnectedMessage) != 1 || strings.Contains(out.String(), "Timeout") {
		t.Fatal(out.String())
	}
}

func TestDisconnectInlineCode(t *testing.T) {
	styled := disconnectMessage(true)
	if !strings.Contains(styled, "\x1b[1;4mfen\x1b[22;24m") || strings.Contains(styled, "`") {
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

func TestNetworkFailuresUseSessionDetachNotice(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, diagnostic := range []string{"Timeout, server ssh.runta.com not responding.\n", "read: Connection reset by peer\n", "client_loop: send disconnect: Broken pipe\n", "ssh: connect to host ssh.runta.com port 22: Connection timed out\n", "Connection closed by remote host\n"} {
		var out strings.Builder
		w := sshDiagnostics{writer: &out, sessionID: "cone-leaf"}
		for _, part := range []string{diagnostic[:5], diagnostic[5:]} {
			w.Write([]byte(part))
		}
		w.Flush()
		want := "Session detached.\nRun fen attach cone-leaf to reattach, or fen to manage sessions.\n"
		if strings.ReplaceAll(out.String(), "\r", "") != want {
			t.Fatalf("%q => %q", diagnostic, out.String())
		}
	}
}
