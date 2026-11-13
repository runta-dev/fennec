package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// chatgptReads are the app-server's last answers to ChatGPT's startup reads,
// kept per cloud app-server mode in ~/.config/fennec/chatgpt-reads.json so a
// daemon restarted while the cloud is away can still answer them.
type chatgptReads struct {
	Mode    string                     `json:"mode,omitempty"`
	Results map[string]json.RawMessage `json:"results"`
}

func (b *chatgptBridge) readsPath() string {
	if b.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(b.path), "chatgpt-reads.json")
}

// loadReadsLocked restores the remembered answers at daemon start.
func (b *chatgptBridge) loadReadsLocked() {
	b.reads = chatgptReads{Results: map[string]json.RawMessage{}}
	path := b.readsPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var stored chatgptReads
	if json.Unmarshal(data, &stored) != nil || stored.Results == nil {
		return
	}
	b.reads = stored
}

func (b *chatgptBridge) cachedRead(key string) json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reads.Results[key]
}

// noteReadMode forgets answers given by the other app-server mode (gateway or
// native), whose configuration and models differ.
func (b *chatgptBridge) noteReadMode(mode string) {
	if mode == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reads.Mode == mode {
		return
	}
	b.reads = chatgptReads{Mode: mode, Results: map[string]json.RawMessage{}}
	b.saveReadsLocked()
}

// rememberRead keeps the app-server's answer to a startup read; the file is
// written off the connection's path and only when an answer changed.
func (b *chatgptBridge) rememberRead(mode, key string, result json.RawMessage) {
	if len(result) > chatgptReadResultLimit {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reads.Results == nil || (mode != "" && b.reads.Mode != mode) {
		b.reads = chatgptReads{Mode: mode, Results: map[string]json.RawMessage{}}
	}
	if existing, ok := b.reads.Results[key]; ok && bytes.Equal(existing, result) {
		return
	}
	if _, ok := b.reads.Results[key]; !ok && len(b.reads.Results) >= chatgptReadEntries {
		for other := range b.reads.Results {
			delete(b.reads.Results, other)
			break
		}
	}
	b.reads.Results[key] = append(json.RawMessage(nil), result...)
	b.saveReadsLocked()
}

func (b *chatgptBridge) saveReadsLocked() {
	path := b.readsPath()
	if path == "" {
		return
	}
	b.readsDirty = true
	if b.readsSaving {
		return
	}
	b.readsSaving = true
	go func() {
		for {
			b.mu.Lock()
			if !b.readsDirty {
				b.readsSaving = false
				b.mu.Unlock()
				return
			}
			b.readsDirty = false
			data, err := json.Marshal(b.reads)
			b.mu.Unlock()
			if err == nil {
				writePrivateRaw(path, data)
			}
		}
	}()
}
