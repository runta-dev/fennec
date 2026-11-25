package launcher

import (
	"crypto/ed25519"
	"crypto/rand"
	"fennec/internal/byoc"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestAttachReachesPairedSelfHostedComputer(t *testing.T) {
	t.Setenv("FENNEC_RUNTA_ENV", "production")
	home := t.TempDir()
	machine := "0b9f6c1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f"
	session := strings.Repeat("a", 32)

	runtaArgs, err := AttachArgs(machine, "t1", session, filepath.Join(home, ".config/fennec/cloud"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runtaArgs, " "), machine+"@ssh.runta.com") {
		t.Fatal("Runta attach no longer targets the gateway")
	}

	if _, err := AttachArgs(machine, "t1", session, byoc.Dir(home)); err == nil {
		t.Fatal("attached to an unpaired self-hosted directory")
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	if err := byoc.Save(home, byoc.Pairing{Version: 1, MachineID: machine, Host: "vps.example.com", Port: 2222, User: byoc.DefaultUser, HostKey: byoc.AuthorizedKey(signer)}); err != nil {
		t.Fatal(err)
	}
	args, err := AttachArgs(machine, "t1", session, byoc.Dir(home))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"-p 2222 fennec@vps.example.com", "UserKnownHostsFile=" + filepath.Join(byoc.Dir(home), "known_hosts"), "StrictHostKeyChecking=yes"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %q in %q", required, joined)
		}
	}
	if strings.Contains(joined, "runta") {
		t.Fatalf("self-hosted attach mentions Runta: %q", joined)
	}
}
