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

// Bounds of one ProbeMac attempt over the existing master and over a fresh
// connection.
const (
	macProbeTimeout      = 4 * time.Second
	macFreshProbeTimeout = 10 * time.Second
)

// ProbeMac checks the actual user-facing alias, including ProxyCommand and Mac
// session creation. A nonce cannot be satisfied by the cloud relay's keepalive.
func ProbeMac(ctx context.Context, home string, h LinkHealth) error {
	return probeMac(ctx, home, h, func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "ssh", args...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		// Bound pipe draining even if a failed ProxyCommand retains stdout.
		cmd.WaitDelay = 200 * time.Millisecond
		return cmd.Output()
	})
}

func probeMac(ctx context.Context, home string, h LinkHealth, run func(context.Context, ...string) ([]byte, error)) error {
	if err := h.Validate(); err != nil {
		return err
	}
	alias := RegisteredAlias(home, h.Device)
	probe := func(fresh, rebuilding bool) error {
		var nonce [16]byte
		if _, e := rand.Read(nonce[:]); e != nil {
			return e
		}
		command := healthCommand + hex.EncodeToString(nonce[:])
		// A fresh connection starts a relay and authenticates to the Mac
		// twice (data and health streams): about 3.2 s at 170 ms RTT on an
		// idle link, so the old shared 4 s bound failed on any jitter.
		timeout := macProbeTimeout
		if fresh || rebuilding {
			timeout = macFreshProbeTimeout
		}
		step, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		args := []string{"-F", filepath.Join(home, ".ssh/config"), "-o", "BatchMode=yes", "-o", "ConnectTimeout=3", "-o", "ConnectionAttempts=1", "-o", "StrictHostKeyChecking=yes"}
		if fresh {
			args = append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
		}
		args = append(args, "--", alias, command)
		out, e := run(step, args...)
		if e != nil {
			return fmt.Errorf("Mac SSH probe failed: %w", e)
		}
		if string(out) != command+"\n" {
			return fmt.Errorf("Mac SSH probe response mismatch")
		}
		return nil
	}
	if err := probe(false, false); err != nil {
		if freshErr := probe(true, false); freshErr != nil {
			return freshErr
		}
		// A fresh path works but the old master does not. Stop accepting new clients
		// on that master, without killing its in-flight sessions.
		step, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, _ = run(step, "-F", filepath.Join(home, ".ssh/config"), "-o", "BatchMode=yes", "-O", "stop", "--", alias)
		// The stopped master cannot serve this attempt: it performs both Mac
		// SSH handshakes again. Applying the warm budget here falsely reports
		// an outage immediately after the fresh path proved the Mac reachable.
		return probe(false, true)
	}
	if h.Fresh {
		return probe(true, false)
	}
	return nil
}
