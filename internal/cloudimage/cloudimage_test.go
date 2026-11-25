package cloudimage

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testLayout(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	return Layout{Home: filepath.Join(root, "home", "fennec"), AuthorizedKeys: filepath.Join(root, "etc", "ssh", "fennec_authorized_keys")}
}

func deviceKey(t *testing.T, comment string) (ssh.PublicKey, string) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " " + comment
}

func TestInitIsStableAcrossRestartsAndContainers(t *testing.T) {
	l := testLayout(t)
	key, line := deviceKey(t, "Fennec device")
	first, err := Init(l, line)
	if err != nil {
		t.Fatal(err)
	}
	if first.Devices != 1 || !strings.HasPrefix(first.Fingerprint, "SHA256:") || !uuidPattern.MatchString(first.MachineID) {
		t.Fatalf("unexpected report %+v", first)
	}
	// A restart, and a new container on the same volume (fresh /etc), keep
	// the host key and identity and do not duplicate the device.
	if err := os.Remove(l.AuthorizedKeys); err != nil {
		t.Fatal(err)
	}
	second, err := Init(l, line)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("restart changed the computer: %+v vs %+v", second, first)
	}
	published, err := os.ReadFile(l.AuthorizedKeys)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(published)
	if err != nil || ssh.FingerprintSHA256(parsed) != ssh.FingerprintSHA256(key) || strings.Count(string(published), "\n") != 1 {
		t.Fatalf("unexpected sshd key file %q", published)
	}
}

func TestPublishedFilesAndPermissions(t *testing.T) {
	l := testLayout(t)
	_, line := deviceKey(t, "")
	report, err := Init(l, line)
	if err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{
		l.State():            0700,
		l.HostKey():          0600,
		l.identity():         0600,
		l.devices():          0600,
		l.publicDir():        0755,
		l.publicPath():       0644,
		l.AuthorizedKeys:     0644,
		l.HostKey() + ".pub": 0644,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode %v, want %v (%v)", path, info.Mode().Perm(), mode, err)
		}
	}
	// The identity Fennec reads over SSH is exactly the machine ID document.
	data, err := os.ReadFile(filepath.Join(l.Home, ".config/fennec-cloud/identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		MachineID string `json:"machine_id"`
	}
	if json.Unmarshal(data, &identity) != nil || identity.MachineID != report.MachineID {
		t.Fatalf("unexpected identity %q", data)
	}
	if fingerprint, err := Fingerprint(l); err != nil || fingerprint != report.Fingerprint {
		t.Fatal("fingerprint differs from the report")
	}
	if machine, err := MachineID(l); err != nil || machine != report.MachineID {
		t.Fatal("machine ID differs from the report")
	}
}

func TestInitRefusesToReplaceCorruptIdentity(t *testing.T) {
	l := testLayout(t)
	if _, err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.identity(), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(l, ""); err == nil {
		t.Fatal("replaced a corrupt identity with a new one")
	}
}

func TestAuthorizeAndRevoke(t *testing.T) {
	l := testLayout(t)
	if _, err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	if published, _ := os.ReadFile(l.AuthorizedKeys); len(published) != 0 {
		t.Fatal("a fresh computer authorized a device")
	}
	keyA, lineA := deviceKey(t, "a")
	_, lineB := deviceKey(t, "b")
	for _, bad := range []string{"", "not a key", `command="sh" ` + lineA, lineA + "\n" + lineB} {
		if _, err := Authorize(l, bad); err == nil {
			t.Fatalf("authorized %q", bad)
		}
	}
	if added, err := Authorize(l, lineA); err != nil || !added {
		t.Fatal(err)
	}
	if added, err := Authorize(l, lineA); err != nil || added {
		t.Fatal("duplicate authorization")
	}
	if _, err := Authorize(l, lineB); err != nil {
		t.Fatal(err)
	}
	devices, _ := Devices(l)
	if len(devices) != 2 {
		t.Fatalf("devices %q", devices)
	}
	if removed, err := Revoke(l, ssh.FingerprintSHA256(keyA)); err != nil || !removed {
		t.Fatal("revoke by fingerprint failed")
	}
	if removed, err := Revoke(l, lineB); err != nil || !removed {
		t.Fatal("revoke by key failed")
	}
	if removed, _ := Revoke(l, lineB); removed {
		t.Fatal("revoked twice")
	}
	if published, _ := os.ReadFile(l.AuthorizedKeys); len(published) != 0 {
		t.Fatalf("revoked keys still published: %q", published)
	}
}

func TestNewUUIDIsVersion4(t *testing.T) {
	for i := 0; i < 100; i++ {
		id, err := newUUID()
		if err != nil || !uuidPattern.MatchString(id) || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("invalid UUID %q", id)
		}
	}
}
