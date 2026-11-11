package localssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// AuthorizeHook uses the same prompt queue, decisions and persistent trust file
// as other local access. The scope key is constructed by the daemon, never by
// a guest. Prompt cancellation and execution cancellation have separate lives.
func (b *Broker) AuthorizeHook(prompt, execution context.Context, description, scope string) (context.Context, func(), error) {
	if !strings.HasPrefix(scope, "hook:") || len(scope) != 69 {
		return nil, nil, fmt.Errorf("invalid hook approval scope")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(scope, "hook:")); err != nil {
		return nil, nil, fmt.Errorf("invalid hook approval scope")
	}
	b.mu.Lock()
	generation := b.generation
	b.mu.Unlock()
	if !b.approveKind(prompt, description, scope, "hook") {
		return nil, nil, fmt.Errorf("hook was not approved")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if execution.Err() != nil || generation != b.generation || b.closed {
		return nil, nil, fmt.Errorf("hook authorization expired")
	}
	select {
	case <-b.done:
		return nil, nil, fmt.Errorf("permission service stopped")
	default:
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, nil, err
	}
	id := hex.EncodeToString(raw[:])
	ctx, cancel := context.WithCancel(execution)
	if b.active == nil {
		b.active = map[string]context.CancelFunc{}
	}
	b.active[id] = cancel
	var once sync.Once
	release := func() { once.Do(func() { cancel(); b.mu.Lock(); delete(b.active, id); b.mu.Unlock() }) }
	return ctx, release, nil
}
func (b *Broker) shutdown() {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.generation++
		b.closed = true
		if b.done != nil {
			close(b.done)
		}
		for _, cancel := range b.active {
			cancel()
		}
		b.active = map[string]context.CancelFunc{}
	})
}
