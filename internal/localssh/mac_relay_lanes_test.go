package localssh

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// stallConn blocks writes while stalled, as a resident stream does when a bulk
// transfer has filled every buffer in front of it.
type stallConn struct {
	net.Conn
	mu      sync.Mutex
	stalled chan struct{} // non-nil while stalled; closed to resume
}

func (c *stallConn) stall() {
	c.mu.Lock()
	c.stalled = make(chan struct{})
	c.mu.Unlock()
}

func (c *stallConn) resume() {
	c.mu.Lock()
	if c.stalled != nil {
		close(c.stalled)
		c.stalled = nil
	}
	c.mu.Unlock()
}

func (c *stallConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	gate := c.stalled
	c.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return c.Conn.Write(data)
}

// A command must not wait behind another session's stalled Mac connection:
// concurrent sessions take separate lanes.
func TestMacRelayCommandIsNotQueuedBehindBusySession(t *testing.T) {
	h := newMacServer(t, 0)
	options := h.options()
	dial := options.Dial
	var mu sync.Mutex
	var conns []*stallConn
	options.Dial = func(ctx context.Context) (net.Conn, error) {
		conn, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		stalling := &stallConn{Conn: conn}
		mu.Lock()
		conns = append(conns, stalling)
		mu.Unlock()
		return stalling, nil
	}
	client := h.client(t, options)
	if out, _, err := run(t, client, "warm up", ""); err != nil || out != "ran warm up\n" {
		t.Fatal(out, err)
	}
	// A long session (stdin left open) occupies the first lane.
	busy, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	stdin, err := busy.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err = busy.Start("cat"); err != nil {
		t.Fatal(err)
	}
	if _, err = stdin.Write([]byte("bulk")); err != nil {
		t.Fatal(err)
	}
	// Its Mac connection (the first dial; the second is its health stream) stops.
	mu.Lock()
	data := conns[0]
	mu.Unlock()
	data.stall()
	defer data.resume()

	done := make(chan error, 1)
	go func() {
		out, _, err := run(t, client, "after", "")
		if err == nil && out != "ran after\n" {
			err = errors.New("unexpected output " + out)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command queued behind a busy session's connection")
	}
}

func TestRelayLanesSpreadSessionsAndRetireIdleLanes(t *testing.T) {
	previousWarm, previousIdle := relayLaneWarmDelay, relayLaneIdleLimit
	relayLaneWarmDelay, relayLaneIdleLimit = time.Hour, 20*time.Millisecond
	defer func() { relayLaneWarmDelay, relayLaneIdleLimit = previousWarm, previousIdle }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &macUpstream{ctx: ctx, pool: make(chan pooledSession, relayPoolSize)}
	u.lanes = newRelayLanes(u)

	first, releaseFirst := u.acquireLane()
	if first != u {
		t.Fatal("an idle relay did not use its own upstream")
	}
	second, releaseSecond := u.acquireLane()
	if second == u {
		t.Fatal("a concurrent session shared the busy upstream")
	}
	releaseFirst()
	again, releaseAgain := u.acquireLane()
	if again != u {
		t.Fatal("an idle primary lane was not preferred")
	}
	var releases []func()
	for len(u.lanes.lanes) < relayMaxLanes {
		_, release := u.acquireLane()
		releases = append(releases, release)
	}
	// All lanes busy: the next session shares one instead of dialing more.
	_, releaseShared := u.acquireLane()
	if count := len(u.lanes.lanes); count != relayMaxLanes {
		t.Fatalf("%d lanes, want at most %d", count, relayMaxLanes)
	}
	releaseShared()
	releaseShared() // release is idempotent
	releaseAgain()
	releaseSecond()
	for _, release := range releases {
		release()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		u.lanes.mu.Lock()
		count := len(u.lanes.lanes)
		u.lanes.mu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d lanes remain after they went idle", count)
		}
		time.Sleep(5 * time.Millisecond)
	}
	u.close()
	if lane, release := u.acquireLane(); lane != u {
		t.Fatal("a closed relay handed out an additional lane")
	} else {
		release()
	}
}
