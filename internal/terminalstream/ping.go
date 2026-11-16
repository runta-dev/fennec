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

// PingMeasurement uses only the client's monotonic clock. SendDuration includes
// local scheduling and the underlying write; it is not network transit time.
// A fast peer may reply before Write returns, so these durations must not be
// subtracted as disjoint RTT components.
type PingMeasurement struct {
	ID           string
	RTT          time.Duration
	SendDuration time.Duration
}

// Ping measures a fresh ping's elapsed time through the production writer to
// receipt of its matching pong. It includes application queueing, but excludes
// initial readiness. Unlike Probe it never coalesces with a heartbeat, accepts
// unrelated traffic as success, or closes a shared transport on timeout.
// At most eight measurements/writes can be outstanding. A timed-out write may
// still finish, but retains its slot until it releases the transport buffer.
func (transport *Transport) Ping(ctx context.Context) (time.Duration, error) {
	measurement, err := transport.MeasurePing(ctx)
	return measurement.RTT, err
}

// MeasurePing exposes the matched ID and local send duration for diagnostics.
// It sends exactly the same frame as Ping and has identical admission bounds.
func (transport *Transport) MeasurePing(ctx context.Context) (PingMeasurement, error) {
	var measurement PingMeasurement
	if err := transport.Ready(ctx); err != nil {
		return measurement, err
	}
	transport.mu.Lock()
	if !transport.capabilities["probe_id"] {
		transport.mu.Unlock()
		return measurement, ErrPingUnsupported
	}
	if transport.measuredPingWrites >= 8 || len(transport.measuredPings) >= 8 {
		transport.mu.Unlock()
		return measurement, ErrPingBusy
	}
	transport.probeSequence++
	id := "measure-" + strconv.FormatUint(transport.probeSequence, 10)
	measurement.ID = id
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
	type sendResult struct {
		err     error
		elapsed time.Duration
	}
	sent := make(chan sendResult, 1)
	go func() {
		err := ctx.Err()
		if err == nil {
			err = transport.Send(Frame{Type: "ping", ID: id})
		}
		elapsed := time.Since(started)
		transport.mu.Lock()
		transport.measuredPingWrites--
		transport.mu.Unlock()
		sent <- sendResult{err: err, elapsed: elapsed}
	}()
	select {
	case <-ctx.Done():
		return measurement, ctx.Err()
	case <-transport.done:
		return measurement, io.EOF
	case result := <-sent:
		measurement.SendDuration = result.elapsed
		if result.err != nil {
			return measurement, result.err
		}
	}
	select {
	case <-ctx.Done():
		return measurement, ctx.Err()
	case <-transport.done:
		return measurement, io.EOF
	case received := <-ack:
		measurement.RTT = received.Sub(started)
		return measurement, nil
	}
}
