package terminalstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"fennec/internal/cmuxhooks"
	"os"
	"sync"
)

type cmuxOwner struct {
	id  string
	ctx context.Context
}
type cmuxEndpoint struct {
	owners []cmuxOwner
	close  func()
}
type cmuxWait struct {
	owner  string
	result chan cmuxhooks.Response
}
type cmuxRelay struct {
	mu        sync.Mutex
	endpoints map[string]*cmuxEndpoint
	pending   map[string]cmuxWait
	emit      func(Frame) error
	ctx       context.Context
}

func newCmuxRelay(ctx context.Context, emit func(Frame) error) *cmuxRelay {
	return &cmuxRelay{ctx: ctx, emit: emit, endpoints: map[string]*cmuxEndpoint{}, pending: map[string]cmuxWait{}}
}
func (relay *cmuxRelay) bind(ctx context.Context, stream, session string) (func(), error) {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	endpoint := relay.endpoints[session]
	if endpoint == nil {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		closeListener, err := cmuxhooks.Listen(relay.ctx, home, session, func(ctx context.Context, event string, payload []byte) cmuxhooks.Response {
			return relay.request(ctx, session, event, payload)
		})
		if err != nil {
			return nil, err
		}
		endpoint = &cmuxEndpoint{close: closeListener}
		relay.endpoints[session] = endpoint
	}
	endpoint.owners = append(endpoint.owners, cmuxOwner{id: stream, ctx: ctx})
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
func (relay *cmuxRelay) request(ctx context.Context, session, event string, payload []byte) cmuxhooks.Response {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return cmuxhooks.Empty()
	}
	requestID := hex.EncodeToString(nonce[:])
	relay.mu.Lock()
	endpoint := relay.endpoints[session]
	if endpoint == nil || len(endpoint.owners) == 0 {
		relay.mu.Unlock()
		return cmuxhooks.Empty()
	}
	owner := endpoint.owners[len(endpoint.owners)-1]
	reply := make(chan cmuxhooks.Response, 1)
	relay.pending[requestID] = cmuxWait{owner: owner.id, result: reply}
	relay.mu.Unlock()
	defer func() { relay.mu.Lock(); delete(relay.pending, requestID); relay.mu.Unlock() }()
	if err := relay.emit(Frame{Type: "cmux_hook", ID: owner.id, HookID: requestID, HookEvent: event, Result: append(json.RawMessage(nil), payload...)}); err != nil {
		return cmuxhooks.Empty()
	}
	select {
	case result := <-reply:
		return result
	case <-ctx.Done():
		return cmuxhooks.Empty()
	case <-owner.ctx.Done():
		return cmuxhooks.Empty()
	case <-relay.ctx.Done():
		return cmuxhooks.Empty()
	}
}
func (relay *cmuxRelay) reply(frame Frame) {
	if len(frame.Result) > cmuxhooks.PayloadLimit || !json.Valid(frame.Result) || (frame.Code != 0 && frame.Code != 2) {
		return
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	pending, ok := relay.pending[frame.HookID]
	if !ok || pending.owner != frame.ID {
		return
	}
	select {
	case pending.result <- cmuxhooks.Response{Data: frame.Result, Code: frame.Code}:
	default:
	}
}

// Another attachment process may still own the endpoint while a disconnected
// desktop is being retired. Do not steal it or block the agent's terminal.
func (relay *cmuxRelay) subscribe(ctx context.Context, stream, session string) (func(), error) {
	closeBinding, err := relay.bind(ctx, stream, session)
	if err == nil {
		return closeBinding, nil
	}
	if !errors.Is(err, cmuxhooks.ErrBusy) {
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
			if !errors.Is(err, cmuxhooks.ErrBusy) {
				return
			}
		}
	}()
	return cancel, nil
}
