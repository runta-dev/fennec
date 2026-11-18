package localssh

import (
	"sync"
	"time"
)

// One OpenSSH ControlMaster carries every command an agent runs through one
// relay. On a single Mac connection all of them share one resident stream, so
// a bulk transfer (cat of a large file, scp, rsync, git) queued megabytes of
// SSH window and loopback buffer ahead of every later command's exec request
// and reply: a 200 ms `echo` waited 15 s behind a 3 MB copy. The resident
// schedules its streams fairly, so the relay spreads concurrent sessions over
// separate Mac connections ("lanes"), each its own stream.
//
// The first lane is the relay's own upstream, which alone carries remote
// forwards (-R). A session takes an idle lane; when none is idle a new one is
// dialed, up to relayMaxLanes, beyond which the least busy is shared. A lane
// is warmed in the background once a session has run for relayLaneWarmDelay,
// so the next command finds a pooled channel instead of a handshake, and an
// extra lane idle for relayLaneIdleLimit is closed.
const relayMaxLanes = 4

// Variables for tests.
var (
	relayLaneWarmDelay = time.Second
	relayLaneIdleLimit = time.Minute
)

type relayLane struct {
	upstream *macUpstream
	active   int
	idle     *time.Timer
}

type relayLanes struct {
	mu     sync.Mutex
	lanes  []*relayLane // lanes[0] is the relay's own upstream
	closed bool
}

func newRelayLanes(primary *macUpstream) *relayLanes {
	return &relayLanes{lanes: []*relayLane{{upstream: primary}}}
}

// newLane returns an additional upstream sharing the relay's options and
// lifetime. It has no downstream and never carries remote forwards.
func (u *macUpstream) newLane() *relayLane {
	return &relayLane{upstream: &macUpstream{options: u.options, ctx: u.ctx, pool: make(chan pooledSession, relayPoolSize)}}
}

// acquireLane picks the upstream for one session and returns it with a
// release function the session must call when it ends.
func (u *macUpstream) acquireLane() (*macUpstream, func()) {
	set := u.lanes
	if set == nil {
		return u, func() {}
	}
	set.mu.Lock()
	if set.closed {
		set.mu.Unlock()
		return u, func() {}
	}
	var lane *relayLane
	for _, candidate := range set.lanes {
		if candidate.active == 0 {
			lane = candidate
			break
		}
	}
	if lane == nil && len(set.lanes) < relayMaxLanes {
		lane = u.newLane()
		set.lanes = append(set.lanes, lane)
	}
	if lane == nil {
		lane = set.lanes[0]
		for _, candidate := range set.lanes[1:] {
			if candidate.active < lane.active {
				lane = candidate
			}
		}
	}
	if lane.idle != nil {
		lane.idle.Stop()
		lane.idle = nil
	}
	lane.active++
	set.mu.Unlock()
	warm := time.AfterFunc(relayLaneWarmDelay, u.warmLane)
	var once sync.Once
	return lane.upstream, func() {
		once.Do(func() {
			warm.Stop()
			u.releaseLane(lane)
		})
	}
}

func (u *macUpstream) releaseLane(lane *relayLane) {
	set := u.lanes
	set.mu.Lock()
	defer set.mu.Unlock()
	lane.active--
	if lane.active == 0 && lane.upstream != u && !set.closed {
		lane.idle = time.AfterFunc(relayLaneIdleLimit, func() { u.retireLane(lane) })
	}
}

// warmLane makes sure an idle lane is connected while a session runs long, so
// a concurrent command does not wait behind it or behind a fresh handshake.
func (u *macUpstream) warmLane() {
	set := u.lanes
	set.mu.Lock()
	if set.closed {
		set.mu.Unlock()
		return
	}
	var lane *relayLane
	for _, candidate := range set.lanes {
		if candidate.active == 0 {
			lane = candidate
			break
		}
	}
	if lane == nil {
		if len(set.lanes) >= relayMaxLanes {
			set.mu.Unlock()
			return
		}
		lane = u.newLane()
		set.lanes = append(set.lanes, lane)
		if lane.idle == nil {
			lane.idle = time.AfterFunc(relayLaneIdleLimit, func() { u.retireLane(lane) })
		}
	}
	set.mu.Unlock()
	// connect also fills the lane's channel pool. A failure is reported to the
	// session that next takes the lane, as for the first lane.
	go lane.upstream.connect()
}

func (u *macUpstream) retireLane(lane *relayLane) {
	set := u.lanes
	set.mu.Lock()
	if lane.active > 0 || set.closed {
		set.mu.Unlock()
		return
	}
	for i, candidate := range set.lanes {
		if candidate == lane {
			set.lanes = append(set.lanes[:i:i], set.lanes[i+1:]...)
			break
		}
	}
	lane.idle = nil
	set.mu.Unlock()
	lane.upstream.close()
}

// closeLanes closes every additional lane; the relay closes its own upstream.
func (u *macUpstream) closeLanes() {
	set := u.lanes
	if set == nil {
		return
	}
	set.mu.Lock()
	set.closed = true
	extra := append([]*relayLane(nil), set.lanes[1:]...)
	set.lanes = set.lanes[:1]
	for _, lane := range extra {
		if lane.idle != nil {
			lane.idle.Stop()
		}
	}
	set.mu.Unlock()
	for _, lane := range extra {
		lane.upstream.close()
	}
}
