package localssh

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const onlineStableWindow = 3 * time.Second
const onlineBatchWindow = 30 * time.Second

type onlineBatch struct {
	Runtime      string `json:"runtime"`
	Generation   string `json:"generation"`
	Since        int64  `json:"since"`
	LastRecovery int64  `json:"last_recovery"`
	LastHealthy  int64  `json:"last_healthy,omitempty"`
	OfflineSince int64  `json:"offline_since,omitempty"`
}

// A sliding batch window merges flapping recoveries, including daemon restarts.
// Only a verified, stable recovery advances it. The outbox uses the unchanged
// generation to suppress already delivered targets and retain deferred targets.
func nextOnlineBatch(previous onlineBatch, runtimeID string, now time.Time) (onlineBatch, error) {
	elapsed := now.Sub(time.Unix(0, previous.LastRecovery))
	if previous.Runtime == runtimeID && previous.Generation != "" && elapsed >= 0 && elapsed < onlineBatchWindow {
		previous.LastRecovery = now.UnixNano()
		return previous, nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return onlineBatch{}, err
	}
	return onlineBatch{Runtime: runtimeID, Generation: hex.EncodeToString(raw[:]), Since: now.UnixNano(), LastRecovery: now.UnixNano(), LastHealthy: previous.LastHealthy}, nil
}

func loadOnlineBatch(directory string) onlineBatch {
	var batch onlineBatch
	raw, err := os.ReadFile(filepath.Join(directory, "online-batch.json"))
	if err == nil {
		_ = json.Unmarshal(raw, &batch)
	}
	if batch.Generation != "" && (batch.Since <= 0 || (LinkHealth{Device: "aabbccdd", Generation: batch.Generation}).Validate() != nil) {
		return onlineBatch{}
	}
	return batch
}

func saveOnlineBatch(directory string, batch onlineBatch) error {
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "online-batch.json")
	if err = os.WriteFile(path+".next", raw, 0600); err != nil {
		return err
	}
	return os.Rename(path+".next", path)
}

// Recovery eligibility is fixed at the first successful probe, before stability
// debounce. Initial startup is silent; persisted health covers abrupt restarts.
func (b *onlineBatch) recovered(runtimeID string, now time.Time) bool {
	if b.Runtime != runtimeID {
		*b = onlineBatch{Runtime: runtimeID}
	}
	since := b.OfflineSince
	if since == 0 {
		since = b.LastHealthy
	}
	notify := since > 0 && now.Sub(time.Unix(0, since)) > onlineBatchWindow
	b.OfflineSince = 0
	b.LastHealthy = now.UnixNano()
	return notify
}
func (b *onlineBatch) disconnected(now time.Time) {
	if b.LastHealthy > 0 && b.OfflineSince == 0 {
		b.OfflineSince = now.UnixNano()
	}
}
