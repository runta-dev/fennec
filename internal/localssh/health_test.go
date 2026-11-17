package localssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestHealthReplyIsBoundedAndNotShell(t *testing.T) {
	for _, command := range []string{"true", healthCommand + "a", healthCommand + strings.Repeat("a", 32) + "; touch x", healthCommand + strings.Repeat("x", 32)} {
		var out bytes.Buffer
		if HealthReply(command, &out) || out.Len() != 0 {
			t.Fatalf("accepted %q", command)
		}
	}
	var out bytes.Buffer
	command := healthCommand + strings.Repeat("a", 32)
	if !HealthReply(command, &out) || out.String() != command+"\n" {
		t.Fatal("nonce failed")
	}
}

func TestLinkHealthDoesNotPublishUnverifiedOrLateSuccess(t *testing.T) {
	for _, late := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var published, notified atomic.Int32
		config := LinkConfig{Health: func(context.Context, LinkHealth) error {
			if late {
				cancel()
				return nil
			}
			return fmt.Errorf("relay down")
		}, Notify: func(context.Context, LinkHealth) { notified.Add(1) }}
		err := monitorLink(ctx, config, "aabbccdd", "Mac.local", func(string, string) { published.Add(1) })
		cancel()
		if err == nil || published.Load() != 0 || notified.Load() != 0 {
			t.Fatal("unverified online", err)
		}
	}
}

// Real OpenSSH alias -> ProxyCommand -> Go relay -> reverse bridge -> authenticated
// Mac connector. The fixture kills only its own relay, then reuses the existing
// ControlMaster after reconciliation. No real user session or keys are touched.
func TestGoRelayEndToEndRecoveryAndHealthAuthorization(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cloud relay uses Linux process identity")
	}
	home, err := os.MkdirTemp("/tmp", "fg-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	device := "aabbccdd"
	binary := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	os.MkdirAll(filepath.Dir(binary), 0700)
	if out, e := exec.Command("go", "build", "-o", binary, "../../cmd/fennec-terminal").CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, out)
	}
	macDir := filepath.Join(home, "mac")
	os.MkdirAll(macDir, 0700)
	_, identityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ssh.NewSignerFromKey(identityPrivate)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(macDir, "authorized_keys"), ssh.MarshalAuthorizedKey(identity.PublicKey()), 0600)
	in, writer := io.Pipe()
	defer writer.Close()
	events, output := io.Pipe()
	defer events.Close()
	serverDone := make(chan error, 1)
	go func() { serverDone <- RunWithApproval(ctx, macDir, in, output, true); output.Close() }()
	decoder := json.NewDecoder(events)
	var listening Event
	if e := decoder.Decode(&listening); e != nil || listening.Type != "listening" {
		t.Fatal("connector start", e, listening)
	}
	dir := filepath.Join(home, ".ssh/fennec")
	os.MkdirAll(dir, 0700)
	private, _ := ssh.MarshalPrivateKey(identityPrivate, "fixture")
	os.WriteFile(filepath.Join(dir, device), pem.EncodeToMemory(private), 0600)
	macPrivate, _ := os.ReadFile(filepath.Join(macDir, "host_key"))
	macKey, _ := ssh.ParsePrivateKey(macPrivate)
	var owned []int
	defer func() {
		for _, pid := range owned {
			if pid > 1 {
				syscall.Kill(pid, syscall.SIGTERM)
			}
		}
	}()
	setup := func() LinkBootstrapResult {
		state, e := EnsureRelay(ctx, home, device)
		if e != nil {
			logRaw, _ := os.ReadFile(filepath.Join(home, ".local/fennec/relay-"+device, "log"))
			stateRaw, _ := os.ReadFile(filepath.Join(home, ".local/fennec/relay-"+device, "state.json"))
			t.Fatalf("ensure: %v log=%s state=%s", e, logRaw, stateRaw)
		}
		owned = append(owned, state.PID)
		_, e = RegisterAlias(home, AliasRegistration{Alias: "Mac.local", Address: fmt.Sprintf("127.0.0.1:%d", state.AgentPort), Device: device, HostKey: string(ssh.MarshalAuthorizedKey(macKey.PublicKey())), Default: true})
		if e != nil {
			t.Fatal(e)
		}
		// OpenSSH expands ~ using passwd, not HOME. Keep fixture config isolated.
		for _, path := range []string{filepath.Join(home, ".ssh/config"), filepath.Join(dir, device+".conf")} {
			raw, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			os.WriteFile(path, bytes.ReplaceAll(raw, []byte("~/"), []byte(home+"/")), 0600)
		}
		return state
	}
	state := setup()
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	defer stopBridge()
	go serveResidentRelay(bridgeCtx, func(c context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "tcp", fmt.Sprintf("127.0.0.1:%d", state.BridgePort))
	}, listening.Address)
	if e := ProbeMac(ctx, home, LinkHealth{Device: device, Fresh: true}); e != nil {
		t.Fatal("initial alias", e)
	}
	// An ordinary command must still ask; the probe never grants shell access.
	cmd := exec.CommandContext(ctx, "ssh", "-F", filepath.Join(home, ".ssh/config"), "Mac.local", "printf forbidden")
	cmd.Env = append(os.Environ(), "HOME="+home)
	result := make(chan error, 1)
	go func() { result <- cmd.Run() }()
	var approval Event
	if e := decoder.Decode(&approval); e != nil || approval.Type != "approval" {
		t.Fatal("ordinary command bypassed approval", e, approval)
	}
	go io.Copy(io.Discard, events)
	json.NewEncoder(writer).Encode(Decision{ID: approval.ID, Allow: false})
	if e := <-result; e == nil {
		t.Fatal("denied command executed")
	}

	stopBridge()
	syscall.Kill(state.PID, syscall.SIGTERM)
	// Wait for the old listener to close before asking the reconciler to reuse it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", state.BridgePort), 50*time.Millisecond)
		if e != nil {
			break
		}
		c.Close()
		time.Sleep(10 * time.Millisecond)
	}
	recovered := setup()
	if recovered.PID == state.PID {
		t.Fatal("dead service reused")
	}
	go serveResidentRelay(ctx, func(c context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "tcp", fmt.Sprintf("127.0.0.1:%d", recovered.BridgePort))
	}, listening.Address)
	if e := ProbeMac(ctx, home, LinkHealth{Device: device, Fresh: true}); e != nil {
		t.Fatal("recovered alias", e)
	}
	stop := exec.Command("ssh", "-F", filepath.Join(home, ".ssh/config"), "-O", "exit", "Mac.local")
	_ = stop.Run()
	cancel()
	writer.Close()
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("connector leaked")
	}
}

func TestLinkAutomaticallyRequestsRepairBeforePublishingConnected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	directory := t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.MarshalPrivateKey(private, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "host_key"), pem.EncodeToMemory(key), 0600); err != nil {
		t.Fatal(err)
	}
	identity := newSigner(t)
	var preparations, checks int
	var connected bool
	config := LinkConfig{Directory: directory, RuntimeID: "owned", VerifiedOwner: "owner", Name: "Mac.local", LocalAddress: "127.0.0.1:1"}
	config.SharedSSH = func(context.Context) (*ssh.Client, error) { return &ssh.Client{}, nil }
	config.Bootstrap = func(_ context.Context, r LinkBootstrap) (LinkBootstrapResult, error) {
		preparations++
		if r.Repair != (preparations > 1) {
			t.Errorf("repair=%v on attempt %d", r.Repair, preparations)
		}
		return LinkBootstrapResult{Ready: true, BridgePort: 1234, AgentPort: 1235, PublicKey: string(ssh.MarshalAuthorizedKey(identity.PublicKey()))}, nil
	}
	config.RelayDial = func(c context.Context, _ string, _ int) (net.Conn, error) {
		a, b := net.Pipe()
		context.AfterFunc(c, func() { b.Close() })
		return a, nil
	}
	config.Health = func(context.Context, LinkHealth) error {
		checks++
		if checks == 1 {
			return fmt.Errorf("dead relay")
		}
		return nil
	}
	if err = Link(ctx, nil, config, func(stage, _ string) {
		if stage == "connected" {
			connected = true
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !connected || preparations != 2 || checks != 2 {
		t.Fatalf("connected=%v prepares=%d probes=%d", connected, preparations, checks)
	}
}
