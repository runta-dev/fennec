package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/byoc"
	"fennec/internal/control"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// byocControl pairs and unpairs a self-hosted cloud computer. The pairing
// record on disk decides which computer every SSH path targets, so it and the
// account principal always change together, under one lock.
type byocControl struct {
	home    string
	account *account.Manager
	mu      sync.Mutex
	// restore returns to the stored Runta account after unpairing.
	restore func()
}

// byocStatus is public: it carries no secret, only the pinned host key's
// fingerprint and this Mac's device public key.
type byocStatus struct {
	Paired             bool      `json:"paired"`
	Active             bool      `json:"active"`
	Unreadable         bool      `json:"unreadable,omitempty"`
	MachineID          string    `json:"machine_id,omitempty"`
	Host               string    `json:"host,omitempty"`
	Port               int       `json:"port,omitempty"`
	User               string    `json:"user,omitempty"`
	HostKeyFingerprint string    `json:"host_key_fingerprint,omitempty"`
	PairedAt           time.Time `json:"paired_at,omitempty"`
	DeviceKey          string    `json:"device_key,omitempty"`
}

func newBYOCControl(home string, manager *account.Manager) *byocControl {
	c := &byocControl{home: home, account: manager}
	c.restore = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		_ = manager.Restore(ctx)
	}
	return c
}

func (c *byocControl) handlers() map[string]control.Handler {
	return map[string]control.Handler{
		"byoc.status": c.status,
		"byoc.key":    c.key,
		"byoc.pair":   c.pair,
		"byoc.unpair": c.unpair,
	}
}

func (c *byocControl) snapshot() byocStatus {
	var status byocStatus
	if _, err := os.Stat(filepath.Join(byoc.Dir(c.home), "runtime_key")); err == nil {
		if signer, err := byoc.DeviceKey(c.home); err == nil {
			status.DeviceKey = byoc.AuthorizedKey(signer)
		}
	}
	p, err := byoc.Load(c.home)
	if errors.Is(err, byoc.ErrNotPaired) {
		return status
	}
	if err != nil {
		status.Unreadable = true
		return status
	}
	status.Paired = true
	status.MachineID = p.MachineID
	status.Host, status.Port, status.User = p.Host, p.Port, p.User
	status.PairedAt = p.PairedAt
	if key, err := p.PublicHostKey(); err == nil {
		status.HostKeyFingerprint = ssh.FingerprintSHA256(key)
	}
	_, principal, _, ok := c.account.Session()
	status.Active = ok && principal.IsBYOC() && principal.Machine == p.MachineID
	return status
}

func (c *byocControl) status(context.Context, json.RawMessage) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot(), nil
}

// key returns this Mac's device public key, creating it on first use. The
// user authorizes it on the computer before pairing.
func (c *byocControl) key(context.Context, json.RawMessage) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	signer, err := byoc.DeviceKey(c.home)
	if err != nil {
		return nil, err
	}
	return map[string]string{"device_key": byoc.AuthorizedKey(signer)}, nil
}

type byocPairRequest struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
}

func (c *byocControl) pair(ctx context.Context, raw json.RawMessage) (any, error) {
	var request byocPairRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid pairing request"}
	}
	if request.Port == 0 {
		request.Port = 22
	}
	if request.User == "" {
		request.User = byoc.DefaultUser
	}
	if strings.TrimSpace(request.Fingerprint) == "" {
		return nil, &control.Fault{Code: "invalid_params", Message: "The computer's host key fingerprint is required"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	previous, previousErr := byoc.Load(c.home)
	if previousErr != nil && !errors.Is(previousErr, byoc.ErrNotPaired) {
		return nil, &control.Fault{Code: "pairing_unreadable", Message: "The existing pairing is unreadable; unpair it first"}
	}
	discoverCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pairing, client, err := byoc.Discover(discoverCtx, c.home, byoc.Endpoint{Host: request.Host, Port: request.Port, User: request.User}, request.Fingerprint)
	if err != nil {
		return nil, &control.Fault{Code: "pairing_failed", Message: "Couldn't pair with the cloud computer: " + err.Error(), Retryable: true}
	}
	client.Close()
	if previousErr == nil && previous.MachineID != pairing.MachineID {
		return nil, &control.Fault{Code: "already_paired", Message: "Another cloud computer is paired; unpair it first"}
	}
	if err := byoc.Save(c.home, pairing); err != nil {
		return nil, err
	}
	if _, err := c.account.Pair(pairing.MachineID); err != nil {
		if previousErr != nil {
			_ = byoc.Remove(c.home)
		}
		return nil, err
	}
	return c.snapshot(), nil
}

type byocUnpairRequest struct {
	MachineID string `json:"machine_id"`
}

// unpair requires the paired machine ID as confirmation. An unreadable
// record has no readable ID, so it is removed with an empty confirmation.
func (c *byocControl) unpair(_ context.Context, raw json.RawMessage) (any, error) {
	var request byocUnpairRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid unpair request"}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := byoc.Load(c.home)
	switch {
	case errors.Is(err, byoc.ErrNotPaired):
		return c.snapshot(), nil
	case err != nil:
		if request.MachineID != "" {
			return nil, &control.Fault{Code: "pairing_unreadable", Message: "The pairing is unreadable; unpair without a machine ID to remove it"}
		}
	case request.MachineID != p.MachineID:
		return nil, &control.Fault{Code: "confirmation_mismatch", Message: "Confirm with the paired cloud computer's machine ID"}
	default:
		// End the principal first: its services stop before the record and
		// keys they use are removed.
		c.account.Unpair(p.MachineID)
	}
	if err := byoc.Remove(c.home); err != nil {
		return nil, err
	}
	if c.restore != nil {
		go c.restore()
	}
	return c.snapshot(), nil
}

// restorePrincipal resumes the principal the Mac used last: the paired
// self-hosted computer when a pairing record exists, otherwise the stored
// Runta account. An unreadable record restores neither, so nothing silently
// targets a different computer; unpairing clears it.
func restorePrincipal(ctx context.Context, home string, manager *account.Manager) {
	p, err := byoc.Load(home)
	switch {
	case err == nil:
		if _, err := manager.Pair(p.MachineID); err != nil {
			log.Printf("self-hosted cloud computer pairing not restored: %v", err)
		}
	case errors.Is(err, byoc.ErrNotPaired):
		_ = manager.Restore(ctx)
	default:
		log.Printf("self-hosted cloud computer pairing is unreadable: %v", err)
	}
}
