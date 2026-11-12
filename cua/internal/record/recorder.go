package record

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Listen starts a listen-only tap that hands each input to sink; stop ends
// it. The tap must never block on sink.
type Listen func(sink func(Input)) (stop func(), err error)

// Recorder runs the recordings of one daemon: at most one at a time, and
// the last one made.
type Recorder struct {
	// Dir holds the recording files (0700; files 0600).
	Dir  string
	Desk Desk
	// Listen starts the event tap; nil uses the platform's tap.
	Listen Listen
	// Acting counts fennec-cua's own actions, whose input is left out.
	Acting *Acting
	// PerPoint is the display's pixels per point (desktop units per
	// point); nil means 1.
	PerPoint func() float64
	// Settle overrides how long input pauses before after windows are
	// read (default Settle).
	Settle time.Duration
	// Stopped reports whether the user stopped fennec-cua; no recording
	// starts then.
	Stopped func() bool
	// Self is fennec-cua's own process id (default os.Getpid()).
	Self int
	// Log reports a recording that could not be saved.
	Log func(format string, args ...any)

	mu    sync.Mutex
	live  *live
	ended *live
}

type live struct {
	id     string
	path   string
	inputs *queue
	stop   func()
	done   chan struct{}
	rec    *Recording
	err    error
}

// Recording reports whether a recording is running.
func (r *Recorder) Recording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live != nil
}

// Start starts recording the user's input, in apps alone when it names any,
// and answers the recording's id.
func (r *Recorder) Start(apps []string) (string, error) {
	if r.Stopped != nil && r.Stopped() {
		return "", ErrStopped
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live != nil {
		return "", ErrRunning
	}
	if r.Desk == nil {
		return "", ErrNoDesk
	}
	listen := r.Listen
	if listen == nil {
		listen = PlatformListen
	}
	if listen == nil {
		return "", errNoListen
	}
	now := time.Now()
	id := fmt.Sprintf("rec-%d", now.UnixMilli())
	l := &live{id: id, path: filepath.Join(r.Dir, id+".json"), inputs: newQueue(), done: make(chan struct{})}
	stop, err := listen(func(in Input) {
		if r.Acting.Own(in.At) {
			return
		}
		l.inputs.push(in)
	})
	if err != nil {
		return "", err
	}
	l.stop = stop
	self := r.Self
	if self == 0 {
		self = os.Getpid()
	}
	per := 1.0
	if r.PerPoint != nil {
		per = r.PerPoint()
	}
	s := &session{id: id, apps: append([]string(nil), apps...), perPoint: per, settle: r.Settle, self: self, desk: r.Desk, inputs: l.inputs, started: now}
	r.live = l
	go func() {
		defer close(l.done)
		rec := s.work(context.Background())
		rec.Path = l.path
		if err := Save(l.path, rec); err != nil {
			l.err = fmt.Errorf("save %s: %w", l.path, err)
			if r.Log != nil {
				r.Log("recording: %v", l.err)
			}
		}
		l.rec = rec
	}()
	return id, nil
}

// Note adds the user's note to the running recording, where it is now.
func (r *Recorder) Note(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live == nil {
		return ErrNotLive
	}
	r.live.inputs.push(Input{Kind: InputNote, At: time.Now(), Note: text})
	return nil
}

// Hotkey tells the running recording that the kill hotkey with this key
// code is ending it, so that keystroke is no step of it.
func (r *Recorder) Hotkey(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live != nil {
		r.live.inputs.push(Input{Kind: InputHotkey, At: time.Now(), Code: code})
	}
}

// end ends the running recording; its worker saves it once it has read
// the last input.
func (r *Recorder) end() *live {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l := r.live; l != nil {
		r.live = nil
		if l.stop != nil {
			l.stop()
		}
		l.inputs.close()
		r.ended = l
	}
	return r.ended
}

// Finish ends a running recording and returns once it is saved (a daemon
// shutdown or a stop calls it).
func (r *Recorder) Finish(ctx context.Context) error {
	l := r.end()
	if l == nil {
		return nil
	}
	select {
	case <-l.done:
		return l.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop ends the running recording, or takes the last one that ended, and
// answers it once it is saved, so the answer and the file agree.
func (r *Recorder) Stop(ctx context.Context) (*Recording, error) {
	l := r.end()
	if l == nil {
		return nil, ErrNone
	}
	select {
	case <-l.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if l.err != nil {
		return nil, l.err
	}
	return l.rec, nil
}
