// Package localssh implements a user-owned SSH exec connector with one-shot approval.
package localssh

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	ssh "github.com/gliderlabs/ssh"
	crypto "golang.org/x/crypto/ssh"
)

type Event struct {
	Type        string `json:"type"`
	ID          string `json:"id,omitempty"`
	Command     string `json:"command,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Address     string `json:"address,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
}
type Decision struct {
	Action   string `json:"action,omitempty"`
	ID       string `json:"id"`
	Allow    bool   `json:"allow"`
	NeverAsk bool   `json:"never_ask"`
}
type Broker struct {
	mu        sync.Mutex
	pending   map[string]chan Decision
	writeMu   sync.Mutex
	output    io.Writer
	directory string
}

func (b *Broker) emit(e Event) {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	json.NewEncoder(b.output).Encode(e)
}
func (b *Broker) approve(ctx context.Context, command, fingerprint string) bool {
	if b.trusted(fingerprint) {
		return true
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return false
	}
	id := hex.EncodeToString(raw)
	ch := make(chan Decision, 1)
	b.mu.Lock()
	if len(b.pending) >= 16 {
		b.mu.Unlock()
		return false
	}
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, id); b.mu.Unlock(); b.emit(Event{Type: "dismiss", ID: id}) }()
	b.emit(Event{Type: "approval", ID: id, Command: command, Fingerprint: fingerprint})
	select {
	case decision := <-ch:
		if decision.Allow && decision.NeverAsk {
			b.remember(fingerprint)
		}
		return decision.Allow
	case <-ctx.Done():
		return false
	}
}
func (b *Broker) decide(d Decision) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch := b.pending[d.ID]; ch != nil {
		select {
		case ch <- d:
		default:
		}
	}
}

// Run uses parent-owned stdin/stdout as private approval IPC; EOF shuts down SSH.
// Paired keys execute as the current OS user by default. No password auth,
// PTY, shell or agent forwarding is enabled. SFTP and local TCP forwarding
// (-L/-D) use explicit approval and the same pairing/revocation policy.
func Run(ctx context.Context, directory string, input io.Reader, output io.Writer) error {
	return RunWithApproval(ctx, directory, input, output, true)
}

func RunWithApproval(ctx context.Context, directory string, input io.Reader, output io.Writer, requireApproval bool) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}
	keyPath := filepath.Join(directory, "host_key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		_, private, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		block, e := crypto.MarshalPrivateKey(private, "Fennec local connector")
		if e != nil {
			return e
		}
		key = pem.EncodeToMemory(block)
		if e = os.WriteFile(keyPath, key, 0600); e != nil {
			return e
		}
	} else if err != nil {
		return err
	}
	signer, err := crypto.ParsePrivateKey(key)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	broker := &Broker{pending: map[string]chan Decision{}, output: output, directory: directory}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	paired := func(key ssh.PublicKey) bool { return authorizedKey(filepath.Join(directory, "authorized_keys"), key) }
	server := &ssh.Server{PublicKeyHandler: func(_ ssh.Context, key ssh.PublicKey) bool { return paired(key) }, Handler: func(session ssh.Session) {
		operation, cancelOperation := context.WithCancel(session.Context())
		defer cancelOperation()
		watchPairing(operation, cancelOperation, func() bool { return paired(session.PublicKey()) }, time.Second)
		stop := context.AfterFunc(ctx, cancelOperation)
		defer stop()
		if !paired(session.PublicKey()) {
			session.Exit(126)
			return
		}
		command := session.RawCommand()
		if command == "" || len(command) > 8192 || session.Subsystem() != "" {
			io.WriteString(session.Stderr(), "Fennec supports approved exec requests only.\n")
			session.Exit(126)
			return
		}
		if requireApproval {
			approval, cancelApproval := context.WithTimeout(operation, 2*time.Minute)
			defer cancelApproval()
			io.WriteString(session.Stderr(), "Fennec: waiting for approval on your computer.\n")
			if !broker.approve(approval, command, crypto.FingerprintSHA256(session.PublicKey())) {
				io.WriteString(session.Stderr(), "Fennec: denied, disconnected or expired.\n")
				session.Exit(126)
				return
			}
		}
		// Approval never overrides pairing revoked while the prompt was open.
		if !paired(session.PublicKey()) {
			session.Exit(126)
			return
		}
		runCtx, cancelRun := context.WithTimeout(operation, 10*time.Minute)
		defer cancelRun()
		cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", command)
		cmd.Dir, _ = os.UserHomeDir()
		cmd.Stdin = session
		cmd.Stdout = session
		cmd.Stderr = session.Stderr()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return nil
			}
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		cmd.WaitDelay = time.Second
		signals := make(chan ssh.Signal, 1)
		session.Signals(signals)
		go func() {
			select {
			case <-signals:
				cancelRun()
			case <-runCtx.Done():
			}
		}()
		code := 0
		if e := cmd.Run(); e != nil {
			code = 1
			var exit *exec.ExitError
			if errors.As(e, &exit) {
				code = exit.ExitCode()
			}
			if code < 0 {
				code = 130
			}
		}
		session.Exit(code)
	}}
	server.ChannelHandlers = map[string]ssh.ChannelHandler{
		"session":      ssh.DefaultSessionHandler,
		"direct-tcpip": directForward(ctx, paired, broker, requireApproval),
	}
	server.SubsystemHandlers = map[string]ssh.SubsystemHandler{"sftp": fileTransfer(ctx, paired, broker, requireApproval)}
	server.AddHostKey(signer)
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), 16384)
		for scanner.Scan() {
			var d Decision
			if json.Unmarshal(scanner.Bytes(), &d) == nil {
				if d.Action == "clear_trust" {
					if err := broker.clearTrust(); err != nil {
						broker.emit(Event{Type: "trust_clear_failed"})
					} else {
						broker.emit(Event{Type: "trust_cleared"})
					}
					continue
				}
				broker.decide(d)
			}
		}
		cancel()
	}()
	go func() { <-ctx.Done(); server.Close() }()
	broker.emit(Event{Type: "listening", Address: listener.Addr().String(), Fingerprint: crypto.FingerprintSHA256(signer.PublicKey())})
	err = server.Serve(listener)
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("SSH service stopped: %w", err)
}

// Re-read pairing on every exec, including requests on an existing connection.
func authorizedKey(path string, key ssh.PublicKey) bool {
	if key == nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for len(data) > 0 {
		pub, _, options, rest, err := crypto.ParseAuthorizedKey(data)
		if err != nil {
			return false
		}
		if len(options) == 0 && ssh.KeysEqual(pub, key) {
			return true
		}
		data = rest
	}
	return false
}

func (b *Broker) trusted(fingerprint string) bool {
	if b.directory == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(b.directory, "trusted_devices.json"))
	if err != nil {
		return false
	}
	var trusted map[string]bool
	if json.Unmarshal(data, &trusted) != nil {
		return false
	}
	return trusted[fingerprint]
}
func (b *Broker) remember(fingerprint string) {
	if b.directory == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	path := filepath.Join(b.directory, "trusted_devices.json")
	trusted := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &trusted) != nil {
			return
		}
	}
	trusted[fingerprint] = true
	data, _ := json.Marshal(trusted)
	file, err := os.CreateTemp(b.directory, "trust-*")
	if err != nil {
		return
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return
	}
	if file.Close() != nil {
		return
	}
	os.Rename(file.Name(), path)
}

func (b *Broker) clearTrust() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.directory == "" {
		return fmt.Errorf("trust directory unavailable")
	}
	err := os.Remove(filepath.Join(b.directory, "trusted_devices.json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
