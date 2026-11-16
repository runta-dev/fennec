package terminalstream

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"
)

var ErrPingBusy = errors.New("too many pending terminal measurements")
var ErrPingUnsupported = errors.New("terminal peer does not support matched ping IDs")

// Ping measures a fresh ping's elapsed time through the production writer to
// receipt of its matching pong. It includes application queueing, but excludes
// initial readiness. Unlike Probe it never coalesces with a heartbeat, accepts
// unrelated traffic as success, or closes a shared transport on timeout.
// At most eight measurements/writes can be outstanding. A timed-out write may
// still finish, but retains its slot until it releases the transport buffer.
func (transport *Transport) Ping(ctx context.Context) (time.Duration, error) {
	if err := transport.Ready(ctx); err != nil {
		return 0, err
	}
	transport.mu.Lock()
	if !transport.capabilities["probe_id"] {
		transport.mu.Unlock()
		return 0, ErrPingUnsupported
	}
	if transport.measuredPingWrites >= 8 || len(transport.measuredPings) >= 8 {
		transport.mu.Unlock()
		return 0, ErrPingBusy
	}
	transport.probeSequence++
	id := "measure-" + strconv.FormatUint(transport.probeSequence, 10)
	ack := make(chan time.Time, 1)
	if transport.measuredPings == nil {
		transport.measuredPings = make(map[string]chan time.Time)
	}
	transport.measuredPings[id] = ack
	transport.measuredPingWrites++
	transport.mu.Unlock()
	defer func() {
		transport.mu.Lock()
		delete(transport.measuredPings, id)
		transport.mu.Unlock()
	}()
	started := time.Now()
	sent := make(chan error, 1)
	go func() {
		err := ctx.Err()
		if err == nil {
			err = transport.Send(Frame{Type: "ping", ID: id})
		}
		transport.mu.Lock()
		transport.measuredPingWrites--
		transport.mu.Unlock()
		sent <- err
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-transport.done:
		return 0, io.EOF
	case err := <-sent:
		if err != nil {
			return 0, err
		}
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-transport.done:
		return 0, io.EOF
	case received := <-ack:
		return received.Sub(started), nil
	}
}
