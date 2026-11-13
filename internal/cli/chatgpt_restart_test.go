package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudruntime "fennec/internal/runtime"
)

// fakeChatGPT connects to the bridge the way ChatGPT 26.1106's
// AppServerConnection does over its websocket transport:
//   - after any close it reconnects, first after backoff, then twice as long
//     each time up to maxBackoff, without limit (its local host has none);
//   - requests in flight on a closed connection are neither failed nor resent:
//     each fails only when its own timeout (30 s in ChatGPT) runs out, and an
//     answer to it is accepted on whichever connection it arrives;
//   - a request made while no connection is up fails at once;
//   - each new connection sends initialize, and once it is answered the
//     startup reads; a configRequirements/read that fails shows a blocking
//     error box (the "modal") and ChatGPT quits.
type fakeChatGPT struct {
	address    string
	timeout    time.Duration
	backoff    time.Duration
	maxBackoff time.Duration

	writes sync.Mutex
	mu     sync.Mutex
	conn   net.Conn
	ready  bool // initialized
	inits  int
	next   int
	// pending requests by id; timedOut, failed and closes record outcomes
	pending  map[string]*fakeRequest
	timedOut []string
	failed   []string
	closes   []int
	modal    bool
}

type fakeRequest struct {
	method   string
	deadline time.Time
	done     chan json.RawMessage
}

func newFakeChatGPT(address string, timeout time.Duration) *fakeChatGPT {
	return &fakeChatGPT{address: address, timeout: timeout, backoff: 100 * time.Millisecond, maxBackoff: time.Second, pending: map[string]*fakeRequest{}}
}

func (c *fakeChatGPT) run(ctx context.Context) {
	go c.expire(ctx)
	delay := c.backoff
	for ctx.Err() == nil {
		conn, err := net.Dial("tcp", c.address)
		if err == nil {
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			if c.serve(conn) {
				delay = c.backoff // a connection that initialized resets the backoff
			}
			stop()
		}
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
		delay = min(2*delay, c.maxBackoff)
	}
}

// expire fails requests whose own timeout ran out.
func (c *fakeChatGPT) expire(ctx context.Context) {
	for ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
		c.mu.Lock()
		for id, request := range c.pending {
			if time.Now().After(request.deadline) {
				delete(c.pending, id)
				c.timedOut = append(c.timedOut, request.method)
				if request.method == "configRequirements/read" {
					c.modal = true
				}
				if request.method == "initialize" && c.conn != nil {
					c.conn.Close() // ChatGPT fails the connection and reconnects
				}
				close(request.done)
			}
		}
		c.mu.Unlock()
	}
}

// serve runs one connection and reports whether it initialized.
func (c *fakeChatGPT) serve(conn net.Conn) bool {
	defer conn.Close()
	io.WriteString(conn, "GET /fennec/"+bridgeTestToken+" HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil || !bytes.Contains([]byte(status), []byte(" 101 ")) {
		return false
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		if line == "\r\n" {
			break
		}
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	initialize := c.send(conn, "__codex_initialize__", "initialize", `{}`)
	var initialized atomic.Bool
	go func() {
		if _, ok := <-initialize.done; !ok {
			return
		}
		c.mu.Lock()
		if c.conn == conn {
			c.ready = true
			initialized.Store(true)
			c.inits++
		}
		c.mu.Unlock()
		// The connection is "connected": ChatGPT reads its requirements and
		// permission profiles.
		requirements := c.request("configRequirements/read", "")
		c.request("permissionProfile/list", `{"cursor":null,"limit":100,"cwd":null}`)
		if requirements == nil {
			c.mu.Lock()
			c.modal = true
			c.mu.Unlock()
		}
	}()
	var buffer []byte
	chunk := make([]byte, 64<<10)
	for {
		n, err := reader.Read(chunk)
		buffer = append(buffer, chunk[:n]...)
		for {
			opcode, _, payload, size, parseErr := cloudruntime.ParseWebSocketFrame(buffer)
			if parseErr != nil || size == 0 {
				break
			}
			buffer = buffer[size:]
			switch opcode {
			case 0x1:
				c.deliver(payload)
			case 0x8:
				code := 1005
				if len(payload) >= 2 {
					code = int(binary.BigEndian.Uint16(payload))
				}
				c.mu.Lock()
				c.closes = append(c.closes, code)
				c.mu.Unlock()
				err = io.EOF
			}
		}
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	if c.conn == conn {
		c.conn, c.ready = nil, false
	}
	c.mu.Unlock()
	return initialized.Load()
}

func (c *fakeChatGPT) deliver(payload []byte) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(payload, &message) != nil || message.Method != "" || len(message.ID) == 0 {
		return
	}
	var id string
	if json.Unmarshal(message.ID, &id) != nil {
		id = string(message.ID)
	}
	c.mu.Lock()
	request := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if request != nil {
		request.done <- message.Result
	}
}

func (c *fakeChatGPT) send(conn net.Conn, id, method, params string) *fakeRequest {
	request := &fakeRequest{method: method, deadline: time.Now().Add(c.timeout), done: make(chan json.RawMessage, 1)}
	c.mu.Lock()
	c.pending[id] = request
	c.mu.Unlock()
	message := `{"id":` + strconv.Quote(id) + `,"method":` + strconv.Quote(method)
	if params != "" {
		message += `,"params":` + params
	}
	c.writes.Lock()
	conn.Write(cloudruntime.EncodeWebSocketFrame(0x1, []byte(message+"}"), true))
	c.writes.Unlock()
	return request
}

// request sends a request on the current connection; without one it fails at
// once, like ChatGPT ("Codex app-server is not available").
func (c *fakeChatGPT) request(method, params string) *fakeRequest {
	c.mu.Lock()
	conn, ready := c.conn, c.ready
	c.next++
	id := fmt.Sprintf("%s:%d", method, c.next)
	if conn == nil || !ready {
		c.failed = append(c.failed, method)
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.send(conn, id, method, params)
}

// await returns a request's answer, or nil when it failed or timed out.
func (c *fakeChatGPT) await(request *fakeRequest) map[string]any {
	if request == nil {
		return nil
	}
	data, ok := <-request.done
	if !ok {
		return nil
	}
	var result map[string]any
	json.Unmarshal(data, &result)
	return result
}

func (c *fakeChatGPT) state() (ready bool, inits int, timedOut, failed []string, closes []int, modal bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready, c.inits, append([]string(nil), c.timedOut...), append([]string(nil), c.failed...), append([]int(nil), c.closes...), c.modal
}

func (c *fakeChatGPT) waitInitialized(t *testing.T, count int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		ready, inits, timedOut, _, _, _ := c.state()
		if ready && inits >= count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ChatGPT not connected (initialized %d times, want %d; timed out: %v)", inits, count, timedOut)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// daemonBridge starts a bridge the way the daemon does (run with the saved
// preference), with launchctl stubbed out.
func daemonBridge(t *testing.T, ctx context.Context, home string, services *daemonServices) *chatgptBridge {
	t.Helper()
	bridge := newChatGPTBridge(home, services)
	bridge.launchctl = func(context.Context, ...string) error { return nil }
	go bridge.run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, listening := bridge.location(); listening {
			return bridge
		}
		if time.Now().After(deadline) {
			t.Fatal("bridge not listening")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// savedBridgeHome is a home whose ChatGPT preference has served ChatGPT
// before, on a free loopback port; startup reads are not remembered yet.
func savedBridgeHome(t *testing.T) (string, string) {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	home := t.TempDir()
	pref := chatgptPreference{Enabled: true, Port: port, Token: bridgeTestToken, Upgrade: "HTTP/1.1 101 Switching Protocols\r\nupgrade: websocket\r\n", EarlyInput: true, Initialize: `{"from":"remembered"}`}
	if err := writePrivateJSON(filepath.Join(home, ".config/fennec/chatgpt.json"), pref); err != nil {
		t.Fatal(err)
	}
	return home, "127.0.0.1:" + strconv.Itoa(port)
}

// The scaled stand-ins for ChatGPT's 30 s request timeout and the bridge's
// bounds keep their real proportions.
func scaleRestartTimings(t *testing.T) time.Duration {
	t.Helper()
	setReadBounds(t, 300*time.Millisecond, 300*time.Millisecond, 2*time.Second)
	limit, quiet := chatgptDrainLimit, chatgptDrainQuiet
	chatgptDrainLimit, chatgptDrainQuiet = 1500*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { chatgptDrainLimit, chatgptDrainQuiet = limit, quiet })
	return 3 * time.Second // ChatGPT's 30 s
}

// A Fennec upgrade replaces the daemon while ChatGPT is connected and has a
// request in flight. The old daemon lets the request finish, closes with
// "service restart", and ChatGPT reconnects to the new daemon (whose cloud
// link is still cold) without a single request timing out and without the
// configRequirements/read modal.
func TestChatGPTSurvivesADaemonRestart(t *testing.T) {
	timeout := scaleRestartTimings(t)
	home, address := savedBridgeHome(t)
	clientCtx, stopClient := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopClient()

	// The running daemon, with its cloud link up.
	oldCtx, stopOld := context.WithCancel(context.Background())
	defer stopOld()
	gate := make(chan struct{})
	oldServices := &daemonServices{}
	connectCloud(t, oldCtx, oldServices, &delayedAppServer{slow: map[string]chan struct{}{"thread/read": gate}})
	old := daemonBridge(t, oldCtx, home, oldServices)

	chatgpt := newFakeChatGPT(address, timeout)
	go chatgpt.run(clientCtx)
	chatgpt.waitInitialized(t, 1, 5*time.Second)
	// Its startup reads are answered by the cloud and remembered.
	deadline := time.Now().Add(5 * time.Second)
	for !old.startupReadsRemembered() {
		if time.Now().After(deadline) {
			t.Fatal("startup reads not remembered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitReadsSaved(old)

	// A large thread/read is in flight when the daemon is asked to stop.
	read := chatgpt.request("thread/read", `{"threadId":"t"}`)
	time.Sleep(50 * time.Millisecond)
	go func() { time.Sleep(300 * time.Millisecond); close(gate) }()
	started := time.Now()
	if lost := old.drain(chatgptDrainLimit); lost != 0 {
		t.Fatalf("%d requests unanswered at the restart", lost)
	}
	if elapsed := time.Since(started); elapsed > chatgptDrainLimit+500*time.Millisecond {
		t.Fatalf("drain took %v", elapsed)
	}
	stopOld() // the cloud link and everything else stop only now
	if result := chatgpt.await(read); result == nil || result["from"] != "cloud" {
		t.Fatalf("the request in flight was not answered: %v", result)
	}

	// launchd starts the new daemon a moment later; its cloud link takes a
	// while to come up.
	time.Sleep(300 * time.Millisecond)
	newCtx, stopNew := context.WithCancel(context.Background())
	defer stopNew()
	newServices := &daemonServices{}
	daemonBridge(t, newCtx, home, newServices)
	go func() {
		time.Sleep(time.Second)
		connectCloud(t, newCtx, newServices, &delayedAppServer{})
	}()

	chatgpt.waitInitialized(t, 2, 5*time.Second)
	// Ordinary requests reach the new daemon's cloud once it is up.
	list := chatgpt.request("thread/list", `{}`)
	if result := chatgpt.await(list); result == nil || result["from"] != "cloud" {
		t.Fatalf("thread/list after the restart: %v", result)
	}
	time.Sleep(timeout) // every request's own timeout has run out by now
	ready, inits, timedOut, _, closes, modal := chatgpt.state()
	if !ready || inits != 2 || len(timedOut) != 0 || modal {
		t.Fatalf("ChatGPT connected=%v initialized=%d timed out=%v modal=%v", ready, inits, timedOut, modal)
	}
	if len(closes) != 1 || closes[0] != websocketServiceRestart {
		t.Fatalf("close codes %v; want one service restart", closes)
	}
}

// The fake behaves like ChatGPT: when the daemon stops without draining, the
// request in flight is orphaned and times out even though ChatGPT reconnects.
func TestChatGPTOrphansRequestsOnAnAbruptRestart(t *testing.T) {
	timeout := scaleRestartTimings(t)
	timeout = timeout / 2
	home, address := savedBridgeHome(t)
	clientCtx, stopClient := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopClient()
	oldCtx, stopOld := context.WithCancel(context.Background())
	gate := make(chan struct{})
	defer close(gate)
	oldServices := &daemonServices{}
	connectCloud(t, oldCtx, oldServices, &delayedAppServer{slow: map[string]chan struct{}{"thread/read": gate}})
	daemonBridge(t, oldCtx, home, oldServices)
	chatgpt := newFakeChatGPT(address, timeout)
	go chatgpt.run(clientCtx)
	chatgpt.waitInitialized(t, 1, 5*time.Second)

	read := chatgpt.request("thread/read", `{"threadId":"t"}`)
	time.Sleep(50 * time.Millisecond)
	stopOld()
	newCtx, stopNew := context.WithCancel(context.Background())
	defer stopNew()
	newServices := &daemonServices{}
	connectCloud(t, newCtx, newServices, &delayedAppServer{})
	daemonBridge(t, newCtx, home, newServices)
	chatgpt.waitInitialized(t, 2, 5*time.Second)
	if result := chatgpt.await(read); result != nil {
		t.Fatalf("an orphaned request was answered: %v", result)
	}
	if _, _, timedOut, _, _, _ := chatgpt.state(); len(timedOut) != 1 || timedOut[0] != "thread/read" {
		t.Fatalf("timed out %v; want the orphaned thread/read", timedOut)
	}
}

// When the cloud does not answer before the drain limit, startup reads are
// answered from their remembered answers before the connection closes, so a
// configRequirements/read caught by the restart never becomes the modal; the
// drain never waits longer than its limit.
func TestChatGPTDrainAnswersRememberedReadsAtItsLimit(t *testing.T) {
	timeout := scaleRestartTimings(t)
	home, address := savedBridgeHome(t)
	clientCtx, stopClient := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopClient()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stalled := make(chan struct{})
	defer close(stalled)
	services := &daemonServices{}
	connectCloud(t, ctx, services, &delayedAppServer{slow: map[string]chan struct{}{"config/read": stalled, "command/exec": stalled}})
	bridge := daemonBridge(t, ctx, home, services)
	bridge.rememberRead("", `config/read {"includeLayers":false}`, json.RawMessage(`{"from":"remembered"}`))

	chatgpt := newFakeChatGPT(address, timeout)
	go chatgpt.run(clientCtx)
	chatgpt.waitInitialized(t, 1, 5*time.Second)
	read := chatgpt.request("config/read", `{"includeLayers":false}`)
	exec := chatgpt.request("command/exec", `{}`)
	time.Sleep(50 * time.Millisecond)
	started := time.Now()
	lost := bridge.drain(chatgptDrainLimit)
	elapsed := time.Since(started)
	if elapsed < chatgptDrainLimit || elapsed > chatgptDrainLimit+500*time.Millisecond {
		t.Fatalf("drain took %v; want its limit %v", elapsed, chatgptDrainLimit)
	}
	if result := chatgpt.await(read); result == nil || result["from"] != "remembered" {
		t.Fatalf("config/read at the drain limit: %v", result)
	}
	if lost != 1 {
		t.Fatalf("%d unanswered; want only command/exec", lost)
	}
	_ = exec
	if _, _, _, _, closes, _ := chatgpt.state(); len(closes) != 1 || closes[0] != websocketServiceRestart {
		t.Fatalf("close codes %v", closes)
	}
	if _, listening := bridge.location(); listening {
		t.Fatal("a draining bridge still accepts connections")
	}
}
