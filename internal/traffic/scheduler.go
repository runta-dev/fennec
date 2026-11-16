// Package traffic schedules bounded application packets before transport buffering.
package traffic

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Class uint8

const (
	Interactive Class = iota
	Bulk
)

// Config counts wire bytes, including the caller's transport overhead estimate.
// The bulk bucket leaves bandwidth available to interactive traffic. Both
// classes consume the root bucket; priority never bypasses the total budget.
type Config struct {
	BytesPerSecond     int
	BulkBytesPerSecond int
	BurstBytes         int
	MaxPacketBytes     int
	QueuePackets       int
}

func DefaultConfig() Config {
	return Config{
		BytesPerSecond: 160 * 1024, BulkBytesPerSecond: 128 * 1024,
		BurstBytes: 2048, MaxPacketBytes: 2048, QueuePackets: 64,
	}
}

var ErrPacketSize = errors.New("traffic packet exceeds the configured atomic write limit")

// ErrWouldBlock is returned by a TryWrite callback that wrote no bytes. The
// scheduler releases its tokens and tries this packet again after a short
// delay, allowing unrelated writable destinations to keep progressing.
var ErrWouldBlock = errors.New("traffic destination is not writable")

type packet struct {
	ctx   context.Context
	flow  string
	class Class
	bytes int
	write func() error
	done  chan error
	retry bool
	ready time.Time
}

// Scheduler preserves FIFO within each flow and fairly visits ready flows.
// A caller must split bulk data into bounded packets before Write. Callbacks
// execute serially: they must write only the submitted packet, and the owner
// must close blocked transport IO when its context is canceled. Write never
// returns while its callback can still access the caller's buffer.
type Scheduler struct {
	ctx       context.Context
	config    Config
	mu        sync.Mutex
	queue     []*packet
	running   bool
	wake      chan struct{}
	slots     chan struct{}
	bulkSlots chan struct{}
	root      bucket
	bulk      bucket
	urgentRun int
	lastFlow  [2]string
}

func New(ctx context.Context, config Config) (*Scheduler, error) {
	if config.BytesPerSecond <= 0 || config.BulkBytesPerSecond <= 0 ||
		config.BulkBytesPerSecond > config.BytesPerSecond || config.MaxPacketBytes <= 0 ||
		config.BurstBytes < config.MaxPacketBytes || config.QueuePackets < 2 {
		return nil, errors.New("invalid traffic scheduler budget")
	}
	now := time.Now()
	return &Scheduler{
		ctx: ctx, config: config, wake: make(chan struct{}, 1), slots: make(chan struct{}, config.QueuePackets),
		bulkSlots: make(chan struct{}, config.QueuePackets-max(1, config.QueuePackets/8)),
		root:      newBucket(config.BytesPerSecond, config.BurstBytes, now),
		bulk:      newBucket(config.BulkBytesPerSecond, config.BurstBytes, now),
	}, nil
}

func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Scheduler) Write(ctx context.Context, flow string, class Class, wireBytes int, write func() error) error {
	return s.write(ctx, flow, class, wireBytes, write, false)
}

// TryWrite is for nonblocking destination writes only. Its callback may return
// ErrWouldBlock only when it made no progress. SSH Channel.Write is blocking
// and must not be used here. A successful partial write must return nil and
// submit its remaining bytes separately, with framing ownership retained.
func (s *Scheduler) TryWrite(ctx context.Context, flow string, class Class, wireBytes int, write func() error) error {
	return s.write(ctx, flow, class, wireBytes, write, true)
}

func (s *Scheduler) write(ctx context.Context, flow string, class Class, wireBytes int, write func() error, retry bool) error {
	if wireBytes <= 0 || wireBytes > s.config.MaxPacketBytes {
		return ErrPacketSize
	}
	if class != Interactive && class != Bulk || write == nil {
		return errors.New("invalid traffic packet")
	}
	// Reserve admission capacity as well as bandwidth. Bulk callers must not
	// fill every queue slot and block a heartbeat before it can be classified.
	if class == Bulk {
		select {
		case s.bulkSlots <- struct{}{}:
			defer func() { <-s.bulkSlots }()
		case <-ctx.Done():
			return ctx.Err()
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	p := &packet{ctx: ctx, flow: flow, class: class, bytes: wireBytes, write: write, retry: retry, done: make(chan error, 1)}
	stop := context.AfterFunc(ctx, s.signal)
	defer stop()
	s.mu.Lock()
	s.queue = append(s.queue, p)
	if !s.running {
		s.running = true
		go s.drain()
	}
	s.mu.Unlock()
	s.signal()
	return <-p.done
}

func (s *Scheduler) delay(p *packet, now time.Time) time.Duration {
	d := s.root.delay(p.bytes, now)
	if p.class == Bulk {
		d = max(d, s.bulk.delay(p.bytes, now))
	}
	return d
}

// selectPacket considers only each flow's head. A newly queued interactive
// packet can interrupt a bulk token wait; a waiting bulk packet owns no tokens.
func (s *Scheduler) selectPacket(now time.Time) (int, time.Duration) {
	ready := [2]int{-1, -1}
	bulkTurn := -1
	seen := make(map[string]bool, len(s.queue))
	wait := time.Hour
	for i, p := range s.queue {
		if p.ctx.Err() != nil || s.ctx.Err() != nil {
			return i, 0
		}
		if seen[p.flow] {
			continue
		}
		seen[p.flow] = true
		if now.Before(p.ready) {
			wait = min(wait, p.ready.Sub(now))
			continue
		}
		if p.class == Bulk && s.bulk.delay(p.bytes, now) == 0 &&
			(bulkTurn < 0 || s.queue[bulkTurn].flow == s.lastFlow[Bulk] && p.flow != s.lastFlow[Bulk]) {
			bulkTurn = i
		}
		delay := s.delay(p, now)
		wait = min(wait, delay)
		if delay > 0 {
			continue
		}
		previous := ready[p.class]
		if previous < 0 || s.queue[previous].flow == s.lastFlow[p.class] && p.flow != s.lastFlow[p.class] {
			ready[p.class] = i
		}
	}
	// After the interactive burst, accumulate enough root tokens for a bulk
	// packet. Otherwise a stream of tiny interactive packets could consume
	// each new token and starve a larger bulk packet forever. This hold is
	// bounded by MaxPacketBytes / BytesPerSecond, not the slower bulk rate.
	if s.urgentRun >= 8 && bulkTurn >= 0 {
		delay := s.root.delay(s.queue[bulkTurn].bytes, now)
		if delay > 0 {
			return -1, delay
		}
		return bulkTurn, 0
	}
	if ready[Interactive] >= 0 && (s.urgentRun < 8 || ready[Bulk] < 0) {
		return ready[Interactive], 0
	}
	if ready[Bulk] >= 0 {
		return ready[Bulk], 0
	}
	return -1, wait
}

func (s *Scheduler) drain() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		now := time.Now()
		index, delay := s.selectPacket(now)
		if index < 0 {
			s.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-s.wake:
			case <-s.ctx.Done():
			}
			timer.Stop()
			continue
		}
		p := s.queue[index]
		s.queue = append(s.queue[:index], s.queue[index+1:]...)
		root, bulk, urgentRun, lastFlow := s.root, s.bulk, s.urgentRun, s.lastFlow
		err := s.ctx.Err()
		if err == nil {
			err = p.ctx.Err()
		}
		if err == nil {
			s.root.take(p.bytes, now)
			if p.class == Bulk {
				s.bulk.take(p.bytes, now)
				s.urgentRun = 0
			} else {
				s.urgentRun++
			}
			s.lastFlow[p.class] = p.flow
		}
		s.mu.Unlock()
		if err == nil {
			err = p.write()
		}
		if p.retry && errors.Is(err, ErrWouldBlock) {
			s.mu.Lock()
			// Only drain mutates the buckets. Returning the exact prior state
			// neither spends bandwidth on a stalled destination nor mints a
			// fresh burst when it becomes writable again.
			s.root, s.bulk, s.urgentRun, s.lastFlow = root, bulk, urgentRun, lastFlow
			p.ready = time.Now().Add(5 * time.Millisecond)
			// Retain FIFO before later packets of the same flow, including
			// callers admitted while the callback ran.
			s.queue = append(s.queue, nil)
			copy(s.queue[index+1:], s.queue[index:])
			s.queue[index] = p
			s.mu.Unlock()
			continue
		}
		p.done <- err
		<-s.slots
	}
}

type bucket struct {
	rate, burst, tokens float64
	updated             time.Time
}

func newBucket(rate, burst int, now time.Time) bucket {
	return bucket{rate: float64(rate), burst: float64(burst), tokens: float64(burst), updated: now}
}

func (b *bucket) refill(now time.Time) {
	if now.After(b.updated) {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.updated).Seconds()*b.rate)
		b.updated = now
	}
}

func (b *bucket) delay(bytes int, now time.Time) time.Duration {
	b.refill(now)
	if float64(bytes) <= b.tokens {
		return 0
	}
	return max(time.Nanosecond, time.Duration((float64(bytes)-b.tokens)/b.rate*float64(time.Second))+1)
}

func (b *bucket) take(bytes int, now time.Time) {
	b.refill(now)
	b.tokens -= float64(bytes)
}
