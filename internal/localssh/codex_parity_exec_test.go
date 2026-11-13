package localssh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fennec/internal/guardian"
	"fennec/internal/sandbox"

	crypto "golang.org/x/crypto/ssh"
)

// startConnector runs the real SSH connector with a paired client key and
// returns an exec helper.
func startConnector(t *testing.T, broker *Broker) func(command string) (int, string) {
	t.Helper()
	directory := t.TempDir()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := crypto.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "authorized_keys"), crypto.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter := io.Pipe()
	eventsReader, eventsWriter := io.Pipe()
	broker.output = eventsWriter
	broker.directory = directory
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); inputWriter.Close() })
	go RunWithApproval(ctx, directory, inputReader, eventsWriter, true, broker)
	address := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(eventsReader)
		for scanner.Scan() {
			var event Event
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "listening" {
				address <- event.Address
			}
		}
	}()
	var addr string
	select {
	case addr = <-address:
	case <-time.After(5 * time.Second):
		t.Fatal("connector did not start")
	}
	client, err := crypto.Dial("tcp", addr, &crypto.ClientConfig{User: "agent", Auth: []crypto.AuthMethod{crypto.PublicKeys(signer)}, HostKeyCallback: crypto.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return func(command string) (int, string) {
		session, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		var stderr bytes.Buffer
		session.Stderr = &stderr
		err = session.Run(command)
		var exit *crypto.ExitError
		if errors.As(err, &exit) {
			return exit.ExitStatus(), stderr.String()
		}
		if err != nil {
			t.Fatalf("exec %q: %v", command, err)
		}
		return 0, stderr.String()
	}
}

func countingReviewer(t *testing.T, outcome string, calls *atomic.Int32) *guardian.Reviewer {
	r := &guardian.Reviewer{Home: t.TempDir()}
	r.Respond = func(context.Context, string, string, string) (string, error) {
		calls.Add(1)
		return outcome, nil
	}
	return r
}

func outsideWorkspace(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("home unavailable")
	}
	var suffix [6]byte
	rand.Read(suffix[:])
	path := filepath.Join(home, ".fennec-parity-test-"+hex.EncodeToString(suffix[:]))
	t.Cleanup(func() { os.RemoveAll(path) })
	return path
}

func TestCodexParityExecFlow(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("Seatbelt unavailable")
	}
	workspace := t.TempDir()
	outside := outsideWorkspace(t)
	var calls atomic.Int32
	broker := NewBroker("", io.Discard)
	broker.Reviewer = countingReviewer(t, `{"outcome":"allow"}`, &calls)
	broker.Policy = func() sandbox.Policy { return sandbox.PolicyForPreset(sandbox.PresetAuto, []string{workspace}) }
	exec := startConnector(t, broker)

	// 1. Ordinary workspace work runs in the sandbox without any review.
	if code, stderr := exec("touch " + filepath.Join(workspace, "ok.txt")); code != 0 {
		t.Fatalf("workspace write failed: %d %s", code, stderr)
	}
	if calls.Load() != 0 {
		t.Fatal("ordinary sandboxed command was reviewed")
	}

	// 2. A sandbox denial is returned to the agent with the escalation form.
	code, stderr := exec("mkdir " + outside)
	if code == 0 || !strings.Contains(stderr, "fennec-escalate --reason") || calls.Load() != 0 {
		t.Fatalf("denial should fail with an escalation hint and no review: %d %q", code, stderr)
	}

	// 3. An escalation request goes to auto-review and, approved, runs outside the workspace.
	code, stderr = exec(`fennec-escalate --reason "create a scratch dir for the build" -- mkdir ` + outside)
	if code != 0 || calls.Load() != 1 {
		t.Fatalf("approved escalation failed: %d %q (reviews %d)", code, stderr, calls.Load())
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("escalated command did not run")
	}

	// 4. Approved escalation still cannot read denied credential paths. (Public
	// SSH material is readable after approval; the Keychain never is.)
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, "Library/Keychains")); err == nil {
		code, _ = exec(`fennec-escalate --reason "inspect keys" -- ls ` + filepath.Join(home, "Library/Keychains"))
		if code == 0 {
			t.Fatal("approved escalation read a protected credential path")
		}
	}

	// 5. A forced rm is reviewed even without escalation and still runs sandboxed.
	os.MkdirAll(filepath.Join(workspace, "build"), 0o755)
	before := calls.Load()
	if code, stderr := exec("rm -rf " + filepath.Join(workspace, "build")); code != 0 || calls.Load() != before+1 {
		t.Fatalf("dangerous command review: %d %q", code, stderr)
	}

	// 6. Protected metadata stays read-only in the workspace sandbox.
	os.MkdirAll(filepath.Join(workspace, ".git"), 0o755)
	if code, _ := exec("touch " + filepath.Join(workspace, ".git", "config")); code == 0 {
		t.Fatal(".git was writable inside the workspace sandbox")
	}
}

func TestCodexParityDeniedEscalationAndFullAccess(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("Seatbelt unavailable")
	}
	workspace := t.TempDir()
	outside := outsideWorkspace(t)
	var calls atomic.Int32
	broker := NewBroker("", io.Discard)
	broker.Reviewer = countingReviewer(t, `{"risk_level":"high","user_authorization":"unknown","outcome":"deny","rationale":"Creates files outside the project without authorization."}`, &calls)
	preset := sandbox.PresetAuto
	broker.Policy = func() sandbox.Policy { return sandbox.PolicyForPreset(preset, []string{workspace}) }
	exec := startConnector(t, broker)

	code, stderr := exec(`fennec-escalate --reason "scratch" -- mkdir ` + outside)
	if code != 126 || !strings.Contains(stderr, "This action was rejected due to unacceptable risk.") || !strings.Contains(stderr, "Creates files outside the project") {
		t.Fatalf("denied escalation: %d %q", code, stderr)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("denied escalation ran")
	}

	preset = sandbox.PresetFull
	if code, stderr := exec("mkdir " + outside); code != 0 {
		t.Fatalf("full access should run unsandboxed: %d %q", code, stderr)
	}
	if code, stderr := exec("rm -rf " + outside); code != 126 || !strings.Contains(stderr, "rejected by policy") {
		t.Fatalf("full access forbids forced rm like Codex: %d %q", code, stderr)
	}
	if calls.Load() != 1 {
		t.Fatalf("full access must not invoke review (reviews %d)", calls.Load())
	}
}
