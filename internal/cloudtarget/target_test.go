package cloudtarget

import (
	"crypto/ed25519"
	"crypto/rand"
	"fennec/internal/byoc"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/crypto/ssh"
)

const machine = "0b9f6c1e-3d2a-4c5b-9e8f-7a6b5c4d3e2f"

func pair(t *testing.T, home string, port int) byoc.Pairing {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	p := byoc.Pairing{Version: 1, MachineID: machine, Host: "vps.example.com", Port: port, User: byoc.DefaultUser, HostKey: byoc.AuthorizedKey(signer)}
	if err := byoc.Save(home, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestActiveIsRuntaUntilPaired(t *testing.T) {
	t.Setenv("FENNEC_RUNTA_ENV", "production")
	home := t.TempDir()
	target, err := Active(home)
	if err != nil {
		t.Fatal(err)
	}
	if target != (Target{Dir: filepath.Join(home, ".config/fennec/cloud"), Host: "ssh.runta.com", Port: 22}) {
		t.Fatalf("unexpected Runta target %+v", target)
	}
	runtime := "01a1b016-11bc-7e63-b3d6-c4955e17b73b"
	want := []string{"-o", "UserKnownHostsFile=" + filepath.Join(home, ".config/fennec/cloud/known_hosts"), "-i", filepath.Join(home, ".config/fennec/cloud/runtime_key"), runtime + "@ssh.runta.com"}
	if got := target.OpenSSHArgs(runtime); !reflect.DeepEqual(got, want) {
		t.Fatalf("Runta OpenSSH arguments changed: %q", got)
	}

	pair(t, home, 2222)
	target, err = Active(home)
	if err != nil {
		t.Fatal(err)
	}
	if target.Dir != byoc.Dir(home) || target.Host != "vps.example.com" || target.SSHPort() != 2222 || target.Login(machine) != "fennec" {
		t.Fatalf("unexpected self-hosted target %+v", target)
	}
	want = []string{"-o", "UserKnownHostsFile=" + filepath.Join(byoc.Dir(home), "known_hosts"), "-i", filepath.Join(byoc.Dir(home), "runtime_key"), "-p", "2222", "fennec@vps.example.com"}
	if got := target.OpenSSHArgs(machine); !reflect.DeepEqual(got, want) {
		t.Fatalf("self-hosted OpenSSH arguments: %q", got)
	}
}

func TestUnreadablePairingIsNotRunta(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(byoc.Dir(home), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(byoc.Dir(home), "pairing.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Active(home); err == nil {
		t.Fatal("a corrupt pairing silently fell back to Runta")
	}
	if _, err := ForDir(byoc.Dir(home)); err == nil {
		t.Fatal("a corrupt pairing resolved a target")
	}
}

func TestForDir(t *testing.T) {
	t.Setenv("FENNEC_RUNTA_ENV", "production")
	home := t.TempDir()
	runta, err := ForDir(filepath.Join(home, ".config/fennec/cloud"))
	if err != nil || runta.Host != "ssh.runta.com" || runta.Home() != home {
		t.Fatalf("unexpected Runta directory target %+v %v", runta, err)
	}
	if _, err := ForDir(byoc.Dir(home)); err == nil {
		t.Fatal("resolved an unpaired self-hosted directory")
	}
	pair(t, home, 22)
	self, err := ForDir(byoc.Dir(home))
	if err != nil || self.Host != "vps.example.com" || self.Home() != home {
		t.Fatalf("unexpected self-hosted directory target %+v %v", self, err)
	}
	if got := self.OpenSSHArgs(machine); got[len(got)-2] == "22" {
		t.Fatal("default port should not be passed explicitly")
	}
	other, err := ForDir("/validation")
	if err != nil || other.Dir != "/validation" || other.Home() != "" {
		t.Fatalf("unexpected validation target %+v %v", other, err)
	}
}

func TestAllowsDial(t *testing.T) {
	t.Setenv("FENNEC_RUNTA_ENV", "production")
	home := t.TempDir()
	if !AllowsDial(home, "ssh.runta.com", "22") {
		t.Fatal("Runta gateway refused")
	}
	if AllowsDial(home, "vps.example.com", "2222") {
		t.Fatal("unpaired host allowed")
	}
	pair(t, home, 2222)
	if !AllowsDial(home, "vps.example.com", "2222") {
		t.Fatal("paired computer refused")
	}
	for _, target := range [][2]string{{"vps.example.com", "22"}, {"evil.example.com", "2222"}, {"ssh.runta.com", "2222"}, {"", "22"}} {
		if AllowsDial(home, target[0], target[1]) {
			t.Fatalf("allowed %s:%s", target[0], target[1])
		}
	}
}
