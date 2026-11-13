package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VM states as tart list reports them.
const (
	StateRunning   = "running"
	StateStopped   = "stopped"
	StateSuspended = "suspended"
)

// State returns the VM's state: running, stopped or suspended, or "" when
// the VM does not exist.
func (v *VM) State(ctx context.Context) (string, error) {
	out, err := v.tart(ctx, "list", "--source", "local", "--format", "json")
	if err != nil {
		return "", err
	}
	var vms []struct {
		Name  string `json:"Name"`
		State string `json:"State"`
	}
	if err := json.Unmarshal([]byte(out), &vms); err != nil {
		return "", fmt.Errorf("tart list: %v", err)
	}
	for _, m := range vms {
		if m.Name == v.Name {
			return m.State, nil
		}
	}
	return "", nil
}

// Dir is the VM's directory in tart's local storage.
func (v *VM) Dir() string {
	home := os.Getenv("TART_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".tart")
	}
	return filepath.Join(home, "vms", v.Name)
}

// Identity returns the VM's MAC address and machine identifier (tart's
// "ecid", from which the guest's serial number and hardware UUID derive).
func (v *VM) Identity() (mac, machine string, err error) {
	data, err := os.ReadFile(filepath.Join(v.Dir(), "config.json"))
	if err != nil {
		return "", "", err
	}
	var cfg struct {
		MAC  string `json:"macAddress"`
		ECID string `json:"ecid"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", err
	}
	return cfg.MAC, cfg.ECID, nil
}

// RandomSerial gives the VM a new machine identifier, so a new serial
// number and hardware UUID in the guest. Like RandomMAC it only suits a
// stopped VM.
func (v *VM) RandomSerial(ctx context.Context) error {
	_, err := v.tart(ctx, "set", v.Name, "--random-serial")
	return err
}

// RandomMAC gives the VM a new random MAC address. A suspended VM no
// longer restores after that, so it only suits a stopped one.
func (v *VM) RandomMAC(ctx context.Context) error {
	_, err := v.tart(ctx, "set", v.Name, "--random-mac")
	return err
}

// StartSuspendable boots the VM like Start, with the devices tart allows
// for a VM that may be suspended later (no audio, clipboard or USB
// accessories).
func (v *VM) StartSuspendable(ctx context.Context) error {
	cmd := exec.Command(v.bin(), "run", "--no-graphics", "--suspendable", v.Name)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return v.waitAgent(ctx, 8*time.Minute)
}

// Suspend saves the running VM's memory and device state next to its disk
// and stops it; the next tart run resumes from there. The VM must have
// been started with StartSuspendable.
func (v *VM) Suspend(ctx context.Context) error {
	if _, err := v.tart(ctx, "suspend", v.Name); err != nil {
		return err
	}
	// tart suspend returns once the request is sent; the state file is
	// complete when the VM reports suspended.
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if st, err := v.State(ctx); err == nil && st == StateSuspended {
			return nil
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("the VM did not reach the suspended state within 3 minutes")
}

// ErrNotResumed means tart booted the VM afresh instead of restoring its
// saved state.
var ErrNotResumed = errors.New("tart did not restore the saved state")

// Resume starts a suspended VM from its saved state and waits until its
// guest agent answers. It fails, instead of waiting for a cold boot, when
// the saved state does not restore: tart then exits (a changed MAC address
// or device configuration) or discards the state and boots afresh (another
// running VM already has this VM's MAC address). On failure the VM may be
// left running and should be deleted.
func (v *VM) Resume(ctx context.Context, timeout time.Duration) error {
	if st, err := v.State(ctx); err != nil {
		return err
	} else if st != StateSuspended {
		return fmt.Errorf("the VM is %s, not suspended", st)
	}
	out := &lockedBuffer{}
	cmd := exec.Command(v.bin(), "run", "--no-graphics", v.Name)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	// tart run lives as long as the VM; this goroutine ends when the
	// trial stops it.
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(out.String(), "Resetting VM") {
			return fmt.Errorf("%w: %s", ErrNotResumed, strings.TrimSpace(out.String()))
		}
		if v.agentReady(ctx) {
			break
		}
		select {
		case err := <-exited:
			return fmt.Errorf("tart run exited (%v): %s", err, strings.TrimSpace(out.String()))
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the guest agent did not answer within %s of the resume: %s", timeout, strings.TrimSpace(out.String()))
		}
	}
	if !strings.Contains(out.String(), "resuming VM") {
		return fmt.Errorf("%w: %s", ErrNotResumed, strings.TrimSpace(out.String()))
	}
	return nil
}

func (v *VM) waitAgent(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if v.agentReady(ctx) {
			return nil
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("the VM's guest agent did not answer within %s", timeout)
}

// SyncClock sets the guest clock to the host's when they differ by a
// second or more, and returns the difference it found (guest minus host).
// A resumed VM starts with the clock of the moment it was suspended until
// the guest catches up.
func (s *Session) SyncClock(ctx context.Context) (time.Duration, error) {
	before := time.Now()
	out, err := s.Output(ctx, "/bin/date -u +%s")
	if err != nil {
		return 0, err
	}
	guest, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("guest date: %q", out)
	}
	host := before.Add(time.Since(before) / 2)
	skew := time.Unix(guest, 0).Sub(host.Truncate(time.Second))
	if skew > -time.Second && skew < time.Second {
		return skew, nil
	}
	now := time.Now().UTC()
	if _, err := s.Output(ctx, "sudo /bin/date -u "+now.Format("010215042006.05")+" >/dev/null"); err != nil {
		return skew, err
	}
	return skew, nil
}

// lockedBuffer collects a child's output while another goroutine reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
