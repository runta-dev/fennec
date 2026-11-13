package mcpserver

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// SessionMetaKey is the initialize request's _meta key that names a session
// to keep across connections: a client that connects again with the same
// name gets the same Session, so the handler's per-session state (a REPL
// runtime and its bindings, browser tabs) survives between connections.
// The command line's `fennec-cua js` uses it; agents that hold one MCP
// connection never need it.
const SessionMetaKey = "fennec-cua/session"

// EndSessionMethod is a request that ends the connection's named session
// when the connection closes, instead of keeping it until it idles out.
const EndSessionMethod = "fennec-cua/endSession"

// Named-session limits.
const (
	// DefaultNamedIdle is how long a named session with no connection is
	// kept before it is closed.
	DefaultNamedIdle = 2 * time.Hour
	// DefaultNamedMax bounds the named sessions kept at once; the least
	// recently used idle one is closed to make room.
	DefaultNamedMax = 32
)

var namedPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidSessionName says whether name can name a session: 1 to 64 ASCII
// letters, digits, '.', '_' or '-'.
func ValidSessionName(name string) bool { return namedPattern.MatchString(name) }

// Named keeps named sessions for a daemon. The zero value is ready to use.
type Named struct {
	// Idle and Max override DefaultNamedIdle and DefaultNamedMax.
	Idle time.Duration
	Max  int

	mu     sync.Mutex
	by     map[string]*namedEntry
	closer SessionCloser
	// now is the clock (tests replace it).
	now func() time.Time
}

type namedEntry struct {
	sess  *Session
	conns int
	used  time.Time
	timer *time.Timer
}

func (n *Named) idle() time.Duration {
	if n.Idle > 0 {
		return n.Idle
	}
	return DefaultNamedIdle
}

func (n *Named) max() int {
	if n.Max > 0 {
		return n.Max
	}
	return DefaultNamedMax
}

func (n *Named) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// attach returns the session name holds, starting it when there is none,
// and counts one more connection on it.
func (n *Named) attach(name, prefix string, closer SessionCloser) (*Session, error) {
	if !ValidSessionName(name) {
		return nil, fmt.Errorf("session name %q is not 1 to 64 letters, digits, '.', '_' or '-'", name)
	}
	n.mu.Lock()
	if closer != nil {
		n.closer = closer
	}
	if n.by == nil {
		n.by = map[string]*namedEntry{}
	}
	if e := n.by[name]; e != nil {
		e.conns++
		e.used = n.clock()
		if e.timer != nil {
			e.timer.Stop()
			e.timer = nil
		}
		n.mu.Unlock()
		return e.sess, nil
	}
	var evicted *Session
	if len(n.by) >= n.max() {
		var oldest string
		for k, e := range n.by {
			if e.conns == 0 && (oldest == "" || e.used.Before(n.by[oldest].used)) {
				oldest = k
			}
		}
		if oldest == "" {
			n.mu.Unlock()
			return nil, errors.New("too many named sessions are in use; end one first")
		}
		evicted = n.remove(oldest)
	}
	e := &namedEntry{sess: &Session{AgentID: agentID(prefix, "", "session-"+name)}, conns: 1, used: n.clock()}
	n.by[name] = e
	n.mu.Unlock()
	if evicted != nil && closer != nil {
		closer.CloseSession(evicted)
	}
	return e.sess, nil
}

// detach counts one connection less on name. A session left without a
// connection is closed after the idle time, or at once when end is set.
func (n *Named) detach(name string, end bool, closer SessionCloser) {
	n.mu.Lock()
	if closer != nil {
		n.closer = closer
	}
	e := n.by[name]
	if e == nil {
		n.mu.Unlock()
		return
	}
	e.conns--
	e.used = n.clock()
	var closed *Session
	switch {
	case end:
		closed = n.remove(name)
	case e.conns <= 0:
		e.conns = 0
		e.timer = time.AfterFunc(n.idle(), func() { n.expire(name, e) })
	}
	n.mu.Unlock()
	if closed != nil && closer != nil {
		closer.CloseSession(closed)
	}
}

// expire closes e when it is still name's session and still idle.
func (n *Named) expire(name string, e *namedEntry) {
	n.mu.Lock()
	if n.by[name] != e || e.conns > 0 || n.clock().Sub(e.used) < n.idle() {
		n.mu.Unlock()
		return
	}
	s := n.remove(name)
	closer := n.closer
	n.mu.Unlock()
	if closer != nil {
		closer.CloseSession(s)
	}
}

// remove drops name with n.mu held and returns its session.
func (n *Named) remove(name string) *Session {
	e := n.by[name]
	if e == nil {
		return nil
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	delete(n.by, name)
	return e.sess
}

// Names lists the named sessions kept now.
func (n *Named) Names() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.by))
	for k := range n.by {
		out = append(out, k)
	}
	return out
}

func closerOf(h Handler) SessionCloser {
	sc, _ := h.(SessionCloser)
	return sc
}
