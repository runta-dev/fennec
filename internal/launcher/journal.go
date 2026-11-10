package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var errReserved = errors.New("launch reserved")

func (j *LaunchJournal) Reserve(ctx context.Context, intent LaunchIntent) error {
	_, err := j.Do(ctx, intent, func(context.Context, LaunchIntent) (LaunchReceipt, error) { return LaunchReceipt{}, errReserved })
	if errors.Is(err, errReserved) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("session already exists; reconnect instead of relaunching")
	}
	return err
}

func (j *LaunchJournal) Complete(intent LaunchIntent, receipt LaunchReceipt) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(j.Directory) || receipt.SessionID != intent.RequestID || receipt.Agent != intent.Agent {
		return fmt.Errorf("launch receipt mismatch")
	}
	payload, _ := json.Marshal(intent)
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	path := filepath.Join(j.Directory, intent.RequestID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var old launchRecord
	if json.Unmarshal(data, &old) != nil || old.Digest != digest || old.State != "pending" {
		return fmt.Errorf("launch reservation mismatch")
	}
	file, err := os.CreateTemp(j.Directory, ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(launchRecord{Digest: digest, State: "complete", Receipt: &receipt})
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	return err
}

type launchRecord struct {
	Digest  string         `json:"digest"`
	State   string         `json:"state"`
	Receipt *LaunchReceipt `json:"receipt,omitempty"`
}

// LaunchJournal persists intent hashes, not prompts or credentials. A pending
// record after a crash is ambiguous and must be inspected, never executed again.
// Exactly one broker owns this journal while its private socket is listening.
type LaunchJournal struct {
	Directory string
	mu        sync.Mutex
	active    map[string]chan struct{}
}

func (j *LaunchJournal) Do(ctx context.Context, intent LaunchIntent, launch func(context.Context, LaunchIntent) (LaunchReceipt, error)) (LaunchReceipt, error) {
	if err := intent.Validate(); err != nil {
		return LaunchReceipt{}, err
	}
	if !filepath.IsAbs(j.Directory) {
		return LaunchReceipt{}, fmt.Errorf("absolute journal directory required")
	}
	// Only duplicate request IDs wait; unrelated sessions prepare independently.
	for {
		j.mu.Lock()
		if j.active == nil {
			j.active = make(map[string]chan struct{})
		}
		pending, exists := j.active[intent.RequestID]
		if !exists {
			pending = make(chan struct{})
			j.active[intent.RequestID] = pending
			j.mu.Unlock()
			defer func() { j.mu.Lock(); delete(j.active, intent.RequestID); close(pending); j.mu.Unlock() }()
			break
		}
		j.mu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
			return LaunchReceipt{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return LaunchReceipt{}, err
	}
	if err := os.MkdirAll(j.Directory, 0700); err != nil {
		return LaunchReceipt{}, err
	}
	payload, _ := json.Marshal(intent)
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	path := filepath.Join(j.Directory, intent.RequestID+".json")
	if data, err := os.ReadFile(path); err == nil {
		var old launchRecord
		if json.Unmarshal(data, &old) != nil || old.Digest != digest {
			return LaunchReceipt{}, fmt.Errorf("launch request identity mismatch")
		}
		if old.State == "complete" && old.Receipt != nil {
			return *old.Receipt, nil
		}
		return LaunchReceipt{}, fmt.Errorf("previous launch outcome unknown; inspect session before retrying")
	} else if !os.IsNotExist(err) {
		return LaunchReceipt{}, err
	}
	// Exclusive create is the cross-process duplicate barrier as well.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return LaunchReceipt{}, err
	}
	err = json.NewEncoder(file).Encode(launchRecord{Digest: digest, State: "pending"})
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return LaunchReceipt{}, err
	}
	receipt, err := launch(ctx, intent)
	if err != nil {
		return LaunchReceipt{}, err
	}
	if receipt.SessionID != intent.RequestID || receipt.Agent != intent.Agent {
		return LaunchReceipt{}, fmt.Errorf("launch receipt mismatch")
	}
	complete, err := os.CreateTemp(j.Directory, ".receipt-")
	if err != nil {
		return LaunchReceipt{}, err
	}
	defer os.Remove(complete.Name())
	err = json.NewEncoder(complete).Encode(launchRecord{Digest: digest, State: "complete", Receipt: &receipt})
	if err == nil {
		err = complete.Sync()
	}
	closeErr = complete.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(complete.Name(), path)
	}
	if err != nil {
		return LaunchReceipt{}, fmt.Errorf("session created but receipt persistence failed: %w", err)
	}
	return receipt, nil
}
