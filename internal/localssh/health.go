package localssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const healthCommand = "fennec-health-v1 "

// HealthReply is deliberately not a shell command. Only an authenticated paired
// SSH session can reach it, and its sole effect is to echo a bounded nonce.
func HealthReply(command string, output io.Writer) bool {
	if !strings.HasPrefix(command, healthCommand) {
		return false
	}
	nonce := strings.TrimPrefix(command, healthCommand)
	raw, err := hex.DecodeString(nonce)
	if err != nil || len(raw) != 16 || len(nonce) != 32 {
		return false
	}
	_, _ = io.WriteString(output, "fennec-health-v1 "+nonce+"\n")
	return true
}

type LinkHealth struct {
	Device     string `json:"device"`
	Fresh      bool   `json:"fresh,omitempty"`
	Generation string `json:"generation,omitempty"`
	Since      int64  `json:"since,omitempty"`
}

func (h LinkHealth) Validate() error {
	if !deviceIDPattern.MatchString(h.Device) {
		return fmt.Errorf("invalid health device")
	}
	if h.Generation != "" {
		raw, e := hex.DecodeString(h.Generation)
		if e != nil || len(raw) != 16 {
			return fmt.Errorf("invalid health generation")
		}
	}
	return nil
}

// ProbeMac checks the actual user-facing alias, including ProxyCommand and Mac
// session creation. A nonce cannot be satisfied by the cloud relay's keepalive.
func ProbeMac(ctx context.Context, home string, h LinkHealth) error {
	if err := h.Validate(); err != nil {
		return err
	}
	alias := RegisteredAlias(home, h.Device)
	probe := func(fresh bool) error {
		var nonce [16]byte
		if _, e := rand.Read(nonce[:]); e != nil {
			return e
		}
		command := healthCommand + hex.EncodeToString(nonce[:])
		step, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		args := []string{"-F", filepath.Join(home, ".ssh/config"), "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "-o", "ConnectionAttempts=1", "-o", "StrictHostKeyChecking=yes"}
		if fresh {
			args = append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
		}
		args = append(args, "--", alias, command)
		cmd := exec.CommandContext(step, "ssh", args...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		// Bound pipe draining even if a failed ProxyCommand retains stdout.
		cmd.WaitDelay = 200 * time.Millisecond
		out, e := cmd.Output()
		if e != nil {
			return fmt.Errorf("Mac SSH probe failed: %w", e)
		}
		if string(out) != command+"\n" {
			return fmt.Errorf("Mac SSH probe response mismatch")
		}
		return nil
	}
	if err := probe(false); err != nil {
		if freshErr := probe(true); freshErr != nil {
			return freshErr
		}
		// A fresh path works but the old master does not. Stop accepting new clients
		// on that master, without killing its in-flight sessions.
		step, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		cmd := exec.CommandContext(step, "ssh", "-F", filepath.Join(home, ".ssh/config"), "-o", "BatchMode=yes", "-O", "stop", "--", alias)
		cmd.WaitDelay = 200 * time.Millisecond
		_ = cmd.Run()
		return probe(false)
	}
	if h.Fresh {
		return probe(true)
	}
	return nil
}
