package cli

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	cloudruntime "fennec/internal/runtime"
)

// A Fennec upgrade or `fen start` replaces the control daemon, which ends
// every ChatGPT connection. ChatGPT (26.1106) reconnects by itself after any
// close (1 s, then 2, 4, 8, 16 and at most 20 s apart, without limit for its
// local host) and initializes the new connection again, but it neither fails
// nor resends the requests that were in flight on the old one: each waits for
// its own 30 s and then fails ("Timed out waiting for MCP response to ..."). A
// timed-out configRequirements/read shows a blocking error box and quits
// ChatGPT; while that box is up ChatGPT does not reconnect at all.
//
// So a daemon that is asked to stop first drains the bridge while the cloud
// link is still up: it stops accepting connections, lets every request
// ChatGPT has in flight get its answer from the cloud (startup reads still
// waiting at the limit are answered from their remembered answers), and only
// then ends each connection, between whole messages, with a WebSocket close
// (1012, service restart). ChatGPT reconnects to the new daemon, whose bridge
// answers the handshake, initialize and startup reads the same way it does
// while the cloud is away.

var (
	// chatgptDrainLimit bounds how long a stopping daemon waits for ChatGPT's
	// requests; launchd allows a stopping job 20 s before it kills it.
	chatgptDrainLimit = 3 * time.Second
	// chatgptDrainQuiet is how long ChatGPT must have sent nothing before its
	// connection is closed, so a request it is just sending is not cut off.
	chatgptDrainQuiet = 100 * time.Millisecond
	// chatgptGoodbyeWait bounds the wait for the cloud to finish the message
	// it is writing before the close frame goes out.
	chatgptGoodbyeWait = 250 * time.Millisecond
)

// websocketServiceRestart is the close code for a server that restarts.
const websocketServiceRestart = 1012

// chatgptLive is one ChatGPT connection being served.
type chatgptLive struct {
	shield *chatgptShield
	files  *cloudruntime.ChatGPTLocalFiles
	end    context.CancelFunc // closes the connection
}

// join registers a connection for draining; it reports false once the daemon
// is stopping, and the connection is then closed before it is used.
func (b *chatgptBridge) join(conn net.Conn, live *chatgptLive) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.draining {
		return false
	}
	if b.live == nil {
		b.live = map[net.Conn]*chatgptLive{}
	}
	b.live[conn] = live
	return true
}

func (b *chatgptBridge) leave(conn net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.live, conn)
}

// drain ends every ChatGPT connection once its requests have their answers,
// waiting at most limit; it is called before the daemon's cloud link closes.
// It reports how many requests were still unanswered when their connection
// closed.
func (b *chatgptBridge) drain(limit time.Duration) int {
	b.mu.Lock()
	b.draining = true
	if b.listener != nil {
		b.listener.Close()
		b.listener = nil
	}
	live := make([]*chatgptLive, 0, len(b.live))
	for _, connection := range b.live {
		live = append(live, connection)
	}
	b.mu.Unlock()
	deadline := time.Now().Add(limit)
	var (
		wait sync.WaitGroup
		mu   sync.Mutex
		lost int
	)
	for _, connection := range live {
		wait.Add(1)
		go func() {
			defer wait.Done()
			unanswered := connection.drain(deadline)
			mu.Lock()
			lost += unanswered
			mu.Unlock()
		}()
	}
	wait.Wait()
	if lost > 0 {
		b.mu.Lock()
		b.lastErr = fmt.Sprintf("%d ChatGPT requests were unanswered when Fennec stopped", lost)
		b.mu.Unlock()
	}
	return lost
}

func (c *chatgptLive) drain(deadline time.Time) int {
	for !c.shield.settled(chatgptDrainQuiet) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c.shield.answerRemembered()
	unanswered := c.shield.unanswered()
	for goodbye := time.Now().Add(chatgptGoodbyeWait); !c.files.Goodbye(websocketServiceRestart) && time.Now().Before(goodbye); {
		time.Sleep(5 * time.Millisecond)
	}
	c.end()
	return unanswered
}
