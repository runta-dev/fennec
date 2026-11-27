package tailnet

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const statusFixture = `{
  "BackendState": "Running",
  "AuthURL": "",
  "Health": [],
  "CurrentTailnet": {"Name": "dev@example.com"},
  "Self": {"ID": "nSelf", "HostName": "fennec-cloud", "DNSName": "fennec-cloud.tail1234.ts.net.", "OS": "linux", "TailscaleIPs": ["100.101.1.1", "fd7a:115c:a1e0::1"], "Online": true, "UserID": 4242},
  "User": {"4242": {"LoginName": "dev@example.com", "DisplayName": "Dev"}},
  "Peer": {
    "nodekey:a": {"ID": "nMac", "HostName": "Dev’s MacBook Pro", "DNSName": "devs-macbook-pro.tail1234.ts.net.", "OS": "macOS", "TailscaleIPs": ["100.101.1.2"], "Online": true, "LastSeen": "0001-01-01T00:00:00Z"},
    "nodekey:b": {"ID": "nBox", "HostName": "box", "DNSName": "box.tail1234.ts.net.", "OS": "linux", "TailscaleIPs": ["100.101.1.3"], "Online": false, "LastSeen": "2026-11-20T08:00:00Z"},
    "nodekey:c": {"ID": "nAir", "HostName": "air", "DNSName": "air.tail1234.ts.net.", "OS": "macOS", "TailscaleIPs": ["100.101.1.4"], "Online": true}
  }
}`

func TestApplyBackendRunning(t *testing.T) {
	var b backend
	if err := json.Unmarshal([]byte(statusFixture), &b); err != nil {
		t.Fatal(err)
	}
	var s Status
	applyBackend(&s, b)
	if s.State != "running" || s.User != "dev@example.com" || s.Tailnet != "dev@example.com" {
		t.Fatalf("status = %+v", s)
	}
	if s.Self == nil || s.Self.Name != "fennec-cloud" || s.Self.IPs[0] != "100.101.1.1" {
		t.Fatalf("self = %+v", s.Self)
	}
	var names []string
	for _, d := range s.Devices {
		names = append(names, d.Name)
	}
	// Online first, then by name; MagicDNS short names replace host names.
	if got := strings.Join(names, ","); got != "air,devs-macbook-pro,box" {
		t.Fatalf("devices = %s", got)
	}
	if s.Devices[1].LastSeen != "" || s.Devices[2].LastSeen != "2026-11-20T08:00:00Z" {
		t.Fatalf("last seen = %q %q", s.Devices[1].LastSeen, s.Devices[2].LastSeen)
	}
}

func TestApplyBackendNeedsLoginBlocked(t *testing.T) {
	// What tailscaled reports behind the Runta egress gateway (FEN-104).
	b := backend{BackendState: "NeedsLogin", Health: []string{`You are logged out. The last login error was: register request: Post "https://controlplane.tailscale.com/machine/register": all connection attempts failed (HTTP: TLS forced: no port 80 dialed, HTTPS: unexpected HTTP response: 400 Bad Request)`}}
	var s Status
	applyBackend(&s, b)
	if s.State != "needs_login" || s.Error != ErrNetworkBlocked || s.Self != nil {
		t.Fatalf("status = %+v", s)
	}
	if got := classifyLoginError("You are logged out. The last login error was: invalid key"); got != ErrLoginFailed {
		t.Fatalf("classify = %q", got)
	}
	if got := classifyLoginError("Some unrelated warning"); got != "" {
		t.Fatalf("classify unrelated = %q", got)
	}
}

func TestParseMetrics(t *testing.T) {
	m := parseMetrics([]byte("os_name=macOS\nos_version=26.1\nname=Dev’s Mac\ncpu_count=14\ncpu_percent=6.8\nmemory_total_bytes=25769803776\nmemory_used_bytes=18999541760\ndisk_total_bytes=994610155520\ndisk_used_bytes=500000000000\n"))
	if m == nil || m.OSName != "macOS" || m.CPUCount != 14 || m.CPUPercent != 6.8 || m.MemTotal != 25769803776 || m.DiskUsed != 500000000000 || m.Name != "Dev’s Mac" {
		t.Fatalf("metrics = %+v", m)
	}
	if parseMetrics([]byte("'sh' is not recognized as an internal or external command\n")) != nil {
		t.Fatal("non-POSIX output must give no metrics")
	}
}

func TestProbeScriptOnThisComputer(t *testing.T) {
	out, err := exec.Command("sh", "-c", probeScript).Output()
	if err != nil {
		t.Fatal(err)
	}
	m := parseMetrics(out)
	if m == nil || m.CPUCount < 1 || m.MemTotal == 0 || m.DiskTotal == 0 {
		t.Fatalf("metrics = %+v from %s", m, out)
	}
}

func TestAuthorizeCommandIsIdempotent(t *testing.T) {
	home := t.TempDir()
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2r0BSzk7ru9MVrZQ6jZnfdNnL7bpBuu8ilmgEqvPCw fennec-cloud-tailnet"
	command := AuthorizeCommands(key)["posix"]
	for i := 0; i < 2; i++ {
		run := exec.Command("sh", "-c", command)
		run.Env = append(os.Environ(), "HOME="+home)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("run %d: %v: %s", i, err, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh/authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	want := `from="100.64.0.0/10,fd7a:115c:a1e0::/48" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2r0BSzk7ru9MVrZQ6jZnfdNnL7bpBuu8ilmgEqvPCw fennec-cloud-tailnet` + "\n"
	if string(data) != want {
		t.Fatalf("authorized_keys = %q", data)
	}
	info, _ := os.Stat(filepath.Join(home, ".ssh/authorized_keys"))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if !strings.Contains(AuthorizeCommands(key)["windows"], "administrators_authorized_keys") {
		t.Fatal("windows command must handle administrator accounts")
	}
}

func TestRequestValidate(t *testing.T) {
	for _, bad := range []Request{{}, {ID: "n 1", User: "a"}, {ID: "n1", User: "-oProxyCommand=x"}, {ID: "n1", User: "a b"}, {ID: "n1"}} {
		if bad.Validate(true) == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if (Request{ID: "nKx7", User: "dev.user"}).Validate(true) != nil || (Request{ID: "nKx7"}).Validate(false) != nil {
		t.Fatal("valid request rejected")
	}
}

func TestComputersAndSSHConfig(t *testing.T) {
	home := t.TempDir()
	p := paths{home}
	if err := os.MkdirAll(p.sshDir(), 0700); err != nil {
		t.Fatal(err)
	}
	// The Mac's device alias already takes this name.
	os.WriteFile(filepath.Join(p.sshDir(), "5d4282fd.conf"), []byte("Host devs-mac fennec-device-5d4282fd machine\n  HostName 127.0.0.1\n"), 0600)
	list := []Computer{{ID: "nMac", Name: "devs-mac", Host: "100.101.1.2", User: "dev", AddedAt: 2}}
	list[0].Alias = alias(p, "devs-mac", nil, "nMac")
	box := Computer{ID: "nBox", Name: "Build Box", Host: "100.101.1.3", User: "root", AddedAt: 1}
	box.Alias = alias(p, box.Name, list, box.ID)
	if list[0].Alias != "devs-mac-2" || box.Alias != "build-box" {
		t.Fatalf("aliases = %q %q", list[0].Alias, box.Alias)
	}
	if err := saveComputers(p, append(list, box)); err != nil {
		t.Fatal(err)
	}
	if got := loadComputers(p); len(got) != 2 || got[0].ID != "nBox" {
		t.Fatalf("saved = %+v", got)
	}
	config, _ := os.ReadFile(p.sshConfig())
	for _, want := range []string{"Host build-box\n  HostName 100.101.1.3\n  User root\n", "Host devs-mac-2\n", "  ProxyCommand \"" + p.tailscale() + "\" --socket=", " nc %h %p\n", "  IdentitiesOnly yes\n"} {
		if !strings.Contains(string(config), want) {
			t.Fatalf("ssh config missing %q:\n%s", want, config)
		}
	}
	// ssh must accept the generated file.
	if out, err := exec.Command("ssh", "-G", "-F", p.sshConfig(), "build-box").CombinedOutput(); err != nil || !strings.Contains(string(out), "hostname 100.101.1.3") {
		t.Fatalf("ssh -G: %v: %s", err, out)
	}
}

func TestEnsureKey(t *testing.T) {
	p := paths{t.TempDir()}
	first, err := ensureKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "ssh-ed25519 ") || !strings.HasSuffix(first, " "+KeyComment) {
		t.Fatalf("key = %q", first)
	}
	again, _ := ensureKey(p)
	if again != first {
		t.Fatal("key changed between calls")
	}
	info, _ := os.Stat(p.key())
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private key mode = %v", info.Mode())
	}
}
