package terminalstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"fennec/internal/hookbridge"
	"os"
	"sync"
)

type hookOwner struct {
	id  string
	ctx context.Context
}
type hookEndpoint struct {
	owners []hookOwner
	close  func()
}
type hookWait struct {
	owner  string
	result chan hookbridge.Result
}
type hookRelay struct {
	mu        sync.Mutex
	endpoints map[string]*hookEndpoint
	pending   map[string]hookWait
	emit      func(Frame) error
	ctx       context.Context
}

func newHookRelay(ctx context.Context, emit func(Frame) error) *hookRelay {
	return &hookRelay{ctx: ctx, emit: emit, endpoints: map[string]*hookEndpoint{}, pending: map[string]hookWait{}}
}
func (relay *hookRelay) bind(ctx context.Context, stream, session string) (func(), error) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	endpoint := relay.endpoints[session]
	if endpoint == nil {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		closeListener, err := hookbridge.Listen(relay.ctx, home, session, func(ctx context.Context, binding hookbridge.Binding, payload []byte) hookbridge.Result {
			return relay.request(ctx, session, binding, payload)
		})
		if err != nil {
			return nil, err
		}
		endpoint = &hookEndpoint{close: closeListener}
		relay.endpoints[session] = endpoint
	}
	endpoint.owners = append(endpoint.owners, hookOwner{id: stream, ctx: ctx})
	return func() {
		relay.mu.Lock()
		defer relay.mu.Unlock()
		endpoint := relay.endpoints[session]
		if endpoint == nil {
			return
		}
		for i, owner := range endpoint.owners {
			if owner.id == stream {
				endpoint.owners = append(endpoint.owners[:i], endpoint.owners[i+1:]...)
				break
			}
		}
		if len(endpoint.owners) == 0 {
			endpoint.close()
			delete(relay.endpoints, session)
		}
	}, nil
}
func (relay *hookRelay) request(ctx context.Context, session string, binding hookbridge.Binding, payload []byte) hookbridge.Result {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return hookbridge.Failure(binding, "is unavailable")
	}
	requestID := hex.EncodeToString(nonce[:])
	relay.mu.Lock()
	endpoint := relay.endpoints[session]
	if endpoint == nil || len(endpoint.owners) == 0 {
		relay.mu.Unlock()
		return hookbridge.Failure(binding, "is unavailable")
	}
	owner := endpoint.owners[len(endpoint.owners)-1]
	reply := make(chan hookbridge.Result, 1)
	relay.pending[requestID] = hookWait{owner: owner.id, result: reply}
	relay.mu.Unlock()
	defer func() { relay.mu.Lock(); delete(relay.pending, requestID); relay.mu.Unlock() }()
	input, _ := json.Marshal(payload)
	if err := relay.emit(Frame{Type: "hook_request", ID: owner.id, HookID: requestID, HookRef: binding.ID, Result: input}); err != nil {
		return hookbridge.Failure(binding, "is unavailable")
	}
	select {
	case result := <-reply:
		return hookbridge.Normalize(binding, result)
	case <-ctx.Done():
		return hookbridge.Failure(binding, "is unavailable")
	case <-owner.ctx.Done():
		return hookbridge.Failure(binding, "is unavailable")
	case <-relay.ctx.Done():
		return hookbridge.Failure(binding, "is unavailable")
	}
}
func (relay *hookRelay) reply(frame Frame) {
	var result hookbridge.Result
	if len(frame.Result) > 2*hookbridge.PayloadLimit+4096 || json.Unmarshal(frame.Result, &result) != nil || !result.Valid() {
		return
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	pending, ok := relay.pending[frame.HookID]
	if !ok || pending.owner != frame.ID {
		return
	}
	select {
	case pending.result <- result:
	default:
	}
}

// Another attachment process may still own the endpoint while a disconnected
// desktop is being retired. Do not steal it or block the agent's terminal.
func (relay *hookRelay) subscribe(ctx context.Context, stream, session string) (func(), error) {
	closeBinding, err := relay.bind(ctx, stream, session)
	if err == nil {
		return closeBinding, nil
	}
	if !errors.Is(err, hookbridge.ErrBusy) {
		return nil, err
	}
	wait, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-wait.Done():
				return
			case <-ticker.C:
			}
			closeBinding, err := relay.bind(wait, stream, session)
			if err == nil {
				<-wait.Done()
				closeBinding()
				return
			}
			if !errors.Is(err, hookbridge.ErrBusy) {
				return
			}
		}
	}()
	return cancel, nil
}
