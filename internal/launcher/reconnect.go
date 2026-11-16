package launcher

import (
	"context"
	"errors"
	"fennec/internal/predict"
	"fennec/internal/terminalinput"
	"fennec/internal/terminalstate"
	"fennec/internal/terminalstream"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// StreamRecovery only reattaches a confirmed session. Plan must never launch an
// agent. Wake reports connectivity transitions, not periodic successful probes.
type StreamRecovery struct {
	Plan func(context.Context, LaunchReceipt) (LaunchPlan, error)
	Wake func(context.Context) <-chan struct{}
	Lost func()
}

// ReconnectStopped identifies a failure that waiting cannot repair.
type ReconnectStopped struct{ Message string }

func (e ReconnectStopped) Error() string { return e.Message }

const (
	reconnectCap   = 5 * time.Second
	reconnectGrace = 3 * time.Second
)

func reconnectDelay(base time.Duration) time.Duration {
	return time.Duration(float64(min(base, reconnectCap)) * (0.8 + rand.Float64()*0.2))
}

type streamInput struct {
	epoch uint64
	data  []byte
	stamp inputStamp
}

// inputQueue bounds keystrokes waiting behind an upload. The reader keeps
// reading (and the screen keeps updating) until this many packets are queued.
const inputQueue = 256

// One terminal owner survives socket replacement and daemon restarts. The input
// reader is never duplicated; disconnected keystrokes are consumed, not replayed.
type streamOwner struct {
	ctx         context.Context
	cancel      context.CancelFunc
	input       chan streamInput
	done        chan struct{}
	mu          sync.Mutex
	active      bool
	epoch       uint64
	changed     chan struct{}
	connectedAt time.Time
	progress    *reconnectProgress
	receipt     LaunchReceipt
	// engine is the active attachment's predictor; the reader records keys
	// with it as they are typed, before they wait for transport credit.
	engine atomic.Pointer[predict.Engine]
}

func executeRecoveringStream(plan LaunchPlan, intent LaunchIntent, home string, ready func(), prepared *preparedDisplay, recovery StreamRecovery) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || recovery.Plan == nil {
		return executeStream(plan, intent, home, ready, prepared)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	defer terminalstate.Restore(os.Stdin, os.Stdout, state)
	fmt.Fprint(os.Stdout, "\x1b[?1004h")
	if prepared == nil {
		facts, early, err := probeDisplay(os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		prepared = &preparedDisplay{facts: facts, input: early}
	}
	owner := &streamOwner{ctx: ctx, cancel: cancel, input: make(chan streamInput, inputQueue), done: make(chan struct{}), changed: make(chan struct{}), active: true}
	go owner.readInput(prepared.input)
	defer func() { cancel(); <-owner.done; owner.clearProgress() }()
	base := time.Second
	var receipt LaunchReceipt
	var wake <-chan struct{}
	err = executeStream(plan, intent, home, ready, prepared, owner)
	for {
		if ctx.Err() != nil {
			return DetachedError{SessionID: intent.RequestID}
		}
		var lost StreamLostError
		if !errors.As(err, &lost) {
			return err
		}
		receipt = lost.Receipt
		owner.setActive(false)
		if recovery.Lost != nil {
			recovery.Lost()
		}
		if !owner.connectedAt.IsZero() && time.Since(owner.connectedAt) >= time.Minute {
			base = time.Second
		}
		owner.connectedAt = time.Time{}
		if owner.progress == nil {
			owner.progress = newReconnectProgress(ctx, prepared.facts)
			if recovery.Wake != nil {
				wake = recovery.Wake(owner.progress.ctx)
			}
		}
		// Try once immediately after a healthy attachment is lost. Failed plans
		// and failed attachment handshakes share the same bounded backoff.
		for {
			owner.progress.retryAt(time.Time{})
			attempt, stop := context.WithTimeout(ctx, 15*time.Second)
			plan, err = recovery.Plan(attempt, receipt)
			stop()
			if ctx.Err() != nil {
				return DetachedError{SessionID: intent.RequestID}
			}
			var permanent ReconnectStopped
			if errors.As(err, &permanent) {
				return err
			}
			if err == nil {
				if !plan.Resume || plan.RuntimeID != receipt.RuntimeID || plan.SessionID != receipt.SessionID || plan.Agent != receipt.Agent {
					return ReconnectStopped{Message: "The reconnected session identity could not be verified."}
				}
				err = executeStream(plan, intent, home, ready, prepared, owner)
				if ctx.Err() != nil {
					return DetachedError{SessionID: intent.RequestID}
				}
				if !errors.As(err, &lost) {
					return err
				}
				owner.setActive(false)
				if owner.progress == nil {
					// A usable terminal was restored; this is a new outage.
					break
				}
			}
			delay := reconnectDelay(base)
			base = min(base*2, reconnectCap)
			owner.progress.retryAt(time.Now().Add(delay))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return DetachedError{SessionID: intent.RequestID}
			case <-wake:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

func (o *streamOwner) setActive(active bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active == active {
		return
	}
	o.active = active
	o.epoch++
	close(o.changed)
	o.changed = make(chan struct{})
}

func (o *streamOwner) usable() {
	if o.connectedAt.IsZero() {
		o.connectedAt = time.Now()
	}
	if o.progress != nil {
		o.clearProgress()
	}
	o.setActive(true)
}

func (o *streamOwner) clearProgress() {
	if o.progress != nil {
		o.progress.close()
		o.progress = nil
	}
}

func (o *streamOwner) readInput(early []byte) {
	defer close(o.done)
	keys := terminalinput.Decoder{}
	buffer := make([]byte, terminalstream.Chunk)
	lastEpoch := uint64(0)
	var lastKey time.Time
	for o.ctx.Err() == nil {
		var raw []byte
		flushed := false
		if len(early) > 0 {
			raw, early = early, nil
		} else {
			poll := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}
			if _, err := unix.Poll(poll, keys.PollTimeout(50)); err != nil {
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				o.cancel()
				return
			}
			if poll[0].Revents == 0 {
				raw = keys.Flush()
				flushed = true
			} else {
				n, err := unix.Read(int(os.Stdin.Fd()), buffer)
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				if err != nil || n == 0 {
					o.cancel()
					return
				}
				raw = buffer[:n]
			}
		}
		o.mu.Lock()
		active, epoch, changed := o.active, o.epoch, o.changed
		o.mu.Unlock()
		if epoch != lastEpoch {
			keys = terminalinput.Decoder{}
			lastEpoch = epoch
		}
		data, detach := raw, false
		if !flushed {
			data, detach = keys.Feed(raw)
		}
		if detach {
			o.cancel()
			return
		}
		if !active || len(data) == 0 {
			continue
		}
		packet := streamInput{epoch: epoch, data: append([]byte(nil), data...), stamp: stampInput(o.engine.Load(), data, &lastKey)}
		select {
		case <-o.ctx.Done():
			return
		case <-changed:
		case o.input <- packet:
		}
	}
}

func (o *streamOwner) pumpInput(ctx context.Context, window *terminalstream.CreditWindow, send func([]byte, inputStamp) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case packet := <-o.input:
			o.mu.Lock()
			valid := o.active && o.epoch == packet.epoch
			o.mu.Unlock()
			if !valid {
				continue
			}
			for len(packet.data) > 0 {
				n := window.Take(min(len(packet.data), terminalstream.Chunk))
				if n == 0 {
					return ctx.Err()
				}
				if err := send(packet.data[:n], packet.stamp); err != nil {
					return err
				}
				packet.stamp.seq += uint64(n)
				packet.stamp.gap = 0
				packet.data = packet.data[n:]
			}
		}
	}
}

// Progress owns output only while the old stream is stopped. close joins it
// before the first restored frame, preventing stale status writes over the TUI.
type reconnectProgress struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	next    time.Time
	visible bool
	row     int
}

func newReconnectProgress(parent context.Context, display *terminalstream.DisplayFacts) *reconnectProgress {
	ctx, cancel := context.WithCancel(parent)
	p := &reconnectProgress{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		grace := time.NewTimer(reconnectGrace)
		defer grace.Stop()
		select {
		case <-ctx.Done():
			return
		case <-grace.C:
		}
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		visibleAt := time.Now()
		shimmer := newReconnectShimmer(display)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		for {
			p.mu.Lock()
			next := p.next
			p.mu.Unlock()
			details := ""
			if remaining := time.Until(next); !next.IsZero() && remaining > 0 {
				details += fmt.Sprintf(" · Retrying in %ds", max(1, int((remaining+time.Second-1)/time.Second)))
			}
			details += " · Ctrl+C to detach"
			columns, rows, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil {
				columns, rows = 80, 24
			}
			elapsed := time.Since(visibleAt)
			text := shimmer.line(frames[int(elapsed/(100*time.Millisecond))%len(frames)], details, columns, elapsed)
			p.visible, p.row = true, max(1, rows)
			fmt.Fprintf(os.Stdout, "\x1b7\x1b[%d;1H\x1b[2K\x1b[0m%s\x1b8", p.row, text)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return p
}

func (p *reconnectProgress) retryAt(next time.Time) { p.mu.Lock(); p.next = next; p.mu.Unlock() }
func (p *reconnectProgress) close() {
	p.cancel()
	<-p.done
	if p.visible {
		fmt.Fprintf(os.Stdout, "\x1b7\x1b[%d;1H\x1b[2K\x1b8", p.row)
	}
}
