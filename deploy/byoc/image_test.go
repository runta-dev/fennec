// Package byocimage verifies the published self-hosted cloud image with
// Fennec's real client code. It needs Docker and runs only when
// FENNEC_BYOC_IMAGE names an image, for example:
//
//	docker build -f deploy/byoc/Dockerfile -t fennec-cloud:test .
//	FENNEC_BYOC_IMAGE=fennec-cloud:test go test ./deploy/byoc
//
// FENNEC_BYOC_BOOTSTRAP=1 additionally runs Fennec's first-connection
// bootstrap (Node.js, Herdr, the terminal service and one agent), which
// downloads from the internet inside the container.
package byocimage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fennec/internal/byoc"
	"fennec/internal/setup"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	var stderr bytes.Buffer
	command := exec.Command("docker", args...)
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(output))
}

type container struct {
	name, volume string
}

func randomName(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// start runs the image on a loopback port with the given volume and waits
// until sshd accepts connections.
func start(t *testing.T, image, volume, authorizedKey string) (container, byoc.Endpoint) {
	t.Helper()
	c := container{name: randomName("fennec-byoc-test-"), volume: volume}
	args := []string{"run", "-d", "--name", c.name, "-p", "127.0.0.1::22", "-v", volume + ":/home/fennec"}
	if authorizedKey != "" {
		args = append(args, "-e", "FENNEC_AUTHORIZED_KEY="+authorizedKey)
	}
	docker(t, append(args, image)...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", c.name).Run() })
	address := docker(t, "port", c.name, "22/tcp")
	host, port, err := net.SplitHostPort(strings.Split(address, "\n")[0])
	if err != nil {
		t.Fatalf("unexpected published port %q", address)
	}
	number, _ := strconv.Atoi(port)
	deadline := time.Now().Add(60 * time.Second)
	for {
		logs, _ := exec.Command("docker", "logs", c.name).CombinedOutput()
		if bytes.Contains(logs, []byte("Server listening")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sshd did not start:\n%s", logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return c, byoc.Endpoint{Host: host, Port: number, User: byoc.DefaultUser}
}

func run(t *testing.T, client *ssh.Client, command string) (string, error) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	output, err := session.CombinedOutput(command)
	return strings.TrimSpace(string(output)), err
}

func TestImage(t *testing.T) {
	image := os.Getenv("FENNEC_BYOC_IMAGE")
	if image == "" {
		t.Skip("set FENNEC_BYOC_IMAGE to test the self-hosted cloud image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	home := t.TempDir()
	device, err := byoc.DeviceKey(home)
	if err != nil {
		t.Fatal(err)
	}
	volume := randomName("fennec-byoc-test-")
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() })

	first, endpoint := start(t, image, volume, byoc.AuthorizedKey(device))
	fingerprint := docker(t, "exec", first.name, "fennec-cloud", "fingerprint")
	machine := docker(t, "exec", first.name, "fennec-cloud", "identity")
	logs, _ := exec.Command("docker", "logs", first.name).CombinedOutput()
	if !bytes.Contains(logs, []byte(fingerprint)) || !bytes.Contains(logs, []byte(machine)) {
		t.Fatalf("start-up output lacks pairing details:\n%s", logs)
	}

	// Pairing accepts only the fingerprint the container printed.
	if _, _, err := byoc.Discover(ctx, home, endpoint, "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("paired with a wrong fingerprint")
	}
	pairing, client, err := byoc.Discover(ctx, home, endpoint, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	if pairing.MachineID != machine {
		t.Fatalf("paired machine %s, container reports %s", pairing.MachineID, machine)
	}
	client, err = byoc.Connect(ctx, home, pairing)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	t.Run("unprivileged user and protected state", func(t *testing.T) {
		if out, err := run(t, client, "id -un; id -u"); err != nil || out != "fennec\n1000" {
			t.Fatalf("unexpected user %q (%v)", out, err)
		}
		for _, command := range []string{
			"cat ~/.fennec-host/ssh_host_ed25519_key",
			"echo forged > ~/.config/fennec-cloud/identity.json",
			"echo forged >> /etc/ssh/fennec_authorized_keys",
			"command -v sudo",
		} {
			if out, err := run(t, client, command); err == nil {
				t.Fatalf("%q succeeded: %q", command, out)
			}
		}
		if _, err := run(t, client, "touch ~/writable && rm ~/writable"); err != nil {
			t.Fatal("home is not writable")
		}
	})

	t.Run("bootstrap tools", func(t *testing.T) {
		for _, tool := range []string{"sh", "bash", "curl", "tar", "gzip", "sha256sum", "base64", "flock", "setsid", "timeout", "mktemp", "git", "ps", "uname"} {
			if _, err := run(t, client, "command -v "+tool); err != nil {
				t.Errorf("missing %s", tool)
			}
		}
		if out, _ := run(t, client, `printf '%s' "$LANG"`); out != "C.UTF-8" {
			t.Errorf("LANG is %q", out)
		}
	})

	t.Run("many channels on one connection", func(t *testing.T) {
		var wg sync.WaitGroup
		failures := make(chan error, 40)
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := run(t, client, "sleep 1"); err != nil {
					failures <- err
				}
			}()
		}
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Fatalf("concurrent session failed: %v", err)
		}
	})

	t.Run("remote forwarding for the Mac connector", func(t *testing.T) {
		listener, err := client.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			conn, err := listener.Accept()
			if err == nil {
				conn.Write([]byte("relay-ok"))
				conn.Close()
			}
		}()
		port := listener.Addr().(*net.TCPAddr).Port
		if out, err := run(t, client, fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d && cat <&3", port)); err != nil || out != "relay-ok" {
			t.Fatalf("forwarded connection returned %q (%v)", out, err)
		}
	})

	if os.Getenv("FENNEC_BYOC_BOOTSTRAP") == "1" {
		t.Run("first-connection bootstrap", func(t *testing.T) { bootstrap(ctx, t, client) })
	}

	// A replacement container on the same volume is the same paired machine,
	// and the device stays authorized without the variable.
	client.Close()
	docker(t, "rm", "-f", first.name)
	second, moved := start(t, image, volume, "")
	if got := docker(t, "exec", second.name, "fennec-cloud", "fingerprint"); got != fingerprint {
		t.Fatalf("host key changed across containers: %s -> %s", fingerprint, got)
	}
	pairing.Port = moved.Port
	again, err := byoc.Connect(ctx, home, pairing)
	if err != nil {
		t.Fatalf("replacement container: %v", err)
	}
	if os.Getenv("FENNEC_BYOC_BOOTSTRAP") == "1" {
		if out, err := run(t, again, `"$HOME/.local/fennec/bin/fennec-terminal" protocol-version`); err != nil || out == "" {
			t.Fatalf("bootstrapped terminal service did not survive replacement: %q (%v)", out, err)
		}
	}
	again.Close()

	// Revoking the device ends access.
	docker(t, "exec", second.name, "fennec-cloud", "revoke", ssh.FingerprintSHA256(device.PublicKey()))
	if _, err := byoc.Connect(ctx, home, pairing); err == nil {
		t.Fatal("revoked device still connects")
	}
	// A volume from another computer is a different machine.
	other := randomName("fennec-byoc-test-")
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", other).Run() })
	_, elsewhere := start(t, image, other, byoc.AuthorizedKey(device))
	pairing.Port = elsewhere.Port
	if _, err := byoc.Connect(ctx, home, pairing); err == nil {
		t.Fatal("connected to a different machine with the old pin")
	}
}

// bootstrap runs Fennec's real first-connection preparation scripts.
func bootstrap(ctx context.Context, t *testing.T, client *ssh.Client) {
	for name, script := range map[string]string{"node": setup.NodeSetupScript, "herdr": setup.HerdrSetupScript} {
		if err := setup.Exec(ctx, client, script); err != nil {
			t.Fatalf("%s setup: %v", name, err)
		}
	}
	arch, err := run(t, client, "uname -m")
	if err != nil {
		t.Fatal(err)
	}
	goarch := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[arch]
	if goarch == "" {
		t.Fatalf("unsupported architecture %q", arch)
	}
	artifacts := t.TempDir()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(artifacts, "fennec-terminal-linux-"+goarch), "./cmd/fennec-terminal")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build terminal service: %v\n%s", err, output)
	}
	if err := setup.InstallTerminalBinary(ctx, client, artifacts); err != nil {
		t.Fatalf("install terminal service: %v", err)
	}
	// Preparation requests "latest": an existing binary is reused, a missing one installed.
	script, err := setup.EnsureAgentScript(setup.Agent{ID: "codex", Version: "latest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Exec(ctx, client, script); err != nil {
		t.Fatalf("install codex: %v", err)
	}
	for _, check := range []string{
		`"$HOME/.local/fennec/bin/fennec-terminal" protocol-version`,
		`"$HOME/.local/bin/herdr-core" --version`,
		`PATH="$HOME/.local/fennec/node/bin:$HOME/.local/bin:$PATH" codex --version`,
	} {
		if out, err := run(t, client, check); err != nil {
			t.Fatalf("%s: %q (%v)", check, out, err)
		}
	}
}
