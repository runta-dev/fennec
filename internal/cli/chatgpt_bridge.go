package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fennec/internal/agent"
	"fennec/internal/control"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ChatGPT.app (bundle com.openai.codex) connects its default "Local" host to
// CODEX_APP_SERVER_WS_URL instead of spawning a local `codex app-server`. The
// daemon serves that URL on loopback and relays every WebSocket connection over
// the resident terminal channel to the cloud runtime's app-server socket.
const (
	chatgptBundleID    = "com.openai.codex"
	chatgptEnvironment = "CODEX_APP_SERVER_WS_URL"
	chatgptDefaultPort = 47631
	chatgptPathPrefix  = "/fennec/"
	chatgptHeadLimit   = 16 << 10
	// chatgptOpenWait bounds the wait for the cloud channel before ChatGPT has
	// a handshake answer; chatgptHeldOpenWait applies once it has one and its
	// requests are simply waiting for the cloud to come back.
	chatgptOpenWait     = 45 * time.Second
	chatgptHeldOpenWait = 15 * time.Minute
)

type chatgptPreference struct {
	Enabled bool   `json:"enabled"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
	// Upgrade is the app-server's last 101 response (without its accept
	// value) and EarlyInput whether the cloud service holds ChatGPT's frames
	// until the app-server answers. Kept across daemon restarts so ChatGPT's
	// first connection is answered at once even while the cloud is away.
	Upgrade    string `json:"upgrade,omitempty"`
	EarlyInput bool   `json:"early_input,omitempty"`
}

type chatgptBridge struct {
	services           *daemonServices
	path               string
	launchctl          func(context.Context, ...string) error
	restartApplication func(context.Context, string) (bool, error)

	mu       sync.Mutex
	ctx      context.Context
	pref     chatgptPreference
	listener net.Listener
	envError string
	active   int
	served   uint64
	lastErr  string
	conns    map[net.Conn]string
	upgrade  []byte // last app-server 101 response, without its accept value
}

func newChatGPTBridge(home string, services *daemonServices) *chatgptBridge {
	return &chatgptBridge{services: services, path: filepath.Join(home, ".config/fennec/chatgpt.json"), launchctl: func(ctx context.Context, arguments ...string) error {
		return exec.CommandContext(ctx, "/bin/launchctl", arguments...).Run()
	}, restartApplication: restartChatGPT}
}

func (b *chatgptBridge) load() chatgptPreference {
	var pref chatgptPreference
	if data, err := os.ReadFile(b.path); err == nil {
		json.Unmarshal(data, &pref)
	}
	if pref.Port <= 0 || pref.Port > 65535 {
		pref.Port = chatgptDefaultPort
	}
	if len(pref.Token) != 64 {
		var nonce [32]byte
		rand.Read(nonce[:])
		pref.Token = hex.EncodeToString(nonce[:])
	}
	return pref
}

func (b *chatgptBridge) url() string {
	return "ws://127.0.0.1:" + strconv.Itoa(b.pref.Port) + chatgptPathPrefix + b.pref.Token
}

// run restores an enabled bridge for the daemon lifetime. Stopping the daemon
// keeps the environment variable so ChatGPT reconnects once the daemon returns.
func (b *chatgptBridge) run(ctx context.Context) {
	b.mu.Lock()
	b.ctx = ctx
	b.pref = b.load()
	if b.pref.Upgrade != "" {
		b.upgrade = []byte(b.pref.Upgrade)
	}
	if b.pref.Enabled {
		b.enableLocked()
	}
	b.mu.Unlock()
	<-ctx.Done()
	b.mu.Lock()
	if b.listener != nil {
		b.listener.Close()
		b.listener = nil
	}
	b.mu.Unlock()
}

func (b *chatgptBridge) enableLocked() error {
	if b.listener == nil {
		listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(b.pref.Port))
		if err != nil {
			listener, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return err
			}
			b.pref.Port = listener.Addr().(*net.TCPAddr).Port
		}
		b.listener = listener
		go b.accept(b.ctx, listener)
	}
	if err := writePrivateJSON(b.path, b.pref); err != nil {
		return err
	}
	setenv, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	if err := b.launchctl(setenv, "setenv", chatgptEnvironment, b.url()); err != nil {
		b.envError = "launchctl setenv failed"
		return err
	}
	b.envError = ""
	return nil
}

func (b *chatgptBridge) disableLocked() error {
	if b.listener != nil {
		b.listener.Close()
		b.listener = nil
	}
	if err := writePrivateJSON(b.path, b.pref); err != nil {
		return err
	}
	unset, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.launchctl(unset, "unsetenv", chatgptEnvironment); err != nil {
		b.envError = "launchctl unsetenv failed"
		return err
	}
	b.envError = ""
	return nil
}

func (b *chatgptBridge) snapshotLocked() map[string]any {
	state := map[string]any{"enabled": b.pref.Enabled, "listening": b.listener != nil, "bundle_id": chatgptBundleID, "environment": chatgptEnvironment, "active_connections": b.active, "served_connections": b.served}
	if b.pref.Enabled {
		state["url"] = b.url()
	}
	if b.envError != "" {
		state["environment_error"] = b.envError
	}
	if b.lastErr != "" {
		state["last_error"] = b.lastErr
	}
	return state
}

func (b *chatgptBridge) handlers() map[string]control.Handler {
	return map[string]control.Handler{
		"chatgpt.get": func(context.Context, json.RawMessage) (any, error) {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.ctx == nil {
				return nil, &control.Fault{Code: "busy", Message: "Daemon is starting", Retryable: true}
			}
			return b.snapshotLocked(), nil
		},
		"chatgpt.set": func(_ context.Context, raw json.RawMessage) (any, error) {
			var request struct {
				Enabled *bool `json:"enabled"`
			}
			if json.Unmarshal(raw, &request) != nil || request.Enabled == nil {
				return nil, &control.Fault{Code: "invalid_params", Message: "enabled is required"}
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.ctx == nil {
				return nil, &control.Fault{Code: "busy", Message: "Daemon is starting", Retryable: true}
			}
			b.pref.Enabled = *request.Enabled
			var err error
			if b.pref.Enabled {
				err = b.enableLocked()
			} else {
				err = b.disableLocked()
			}
			if err != nil {
				return nil, &control.Fault{Code: "chatgpt_bridge_failed", Message: "Unable to update the ChatGPT cloud connection"}
			}
			return b.snapshotLocked(), nil
		},
	}
}

func (b *chatgptBridge) accept(ctx context.Context, listener net.Listener) {
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			b.mu.Lock()
			b.active++
			b.served++
			token := b.pref.Token
			b.mu.Unlock()
			err := b.serve(ctx, conn, token)
			b.mu.Lock()
			b.active--
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
				b.lastErr = err.Error()
			}
			b.mu.Unlock()
		}()
	}
}

// readUpgrade validates the token-bearing request path and rewrites it to the
// path codex app-server serves over its control socket. The token keeps other
// local users off the loopback port; the URL lives only in this user's launchd
// environment and private preference file.
func readUpgrade(reader *bufio.Reader, token string) ([]byte, error) {
	var head bytes.Buffer
	for {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return nil, fmt.Errorf("incomplete upgrade request")
		}
		if head.Len()+len(line) > chatgptHeadLimit {
			return nil, fmt.Errorf("oversized upgrade request")
		}
		if head.Len() == 0 {
			fields := strings.Fields(string(line))
			if len(fields) != 3 || fields[0] != "GET" || !strings.HasPrefix(fields[2], "HTTP/1.") {
				return nil, errForbidden
			}
			path, _, _ := strings.Cut(fields[1], "?")
			supplied, ok := strings.CutPrefix(path, chatgptPathPrefix)
			if !ok || subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
				return nil, errForbidden
			}
			head.WriteString("GET /rpc " + fields[2] + "\r\n")
			continue
		}
		head.Write(line)
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			return head.Bytes(), nil
		}
	}
}

var errForbidden = errors.New("forbidden")

func (b *chatgptBridge) serve(ctx context.Context, conn net.Conn, token string) error {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReaderSize(conn, chatgptHeadLimit)
	head, err := readUpgrade(reader, token)
	if err != nil {
		if errors.Is(err, errForbidden) {
			io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			return nil
		}
		return err
	}
	conn.SetDeadline(time.Time{})
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(operation, func() { conn.Close() })
	defer stop()
	answered := false
	unavailable := func(err error) error {
		if answered {
			return err // ChatGPT already has a 101; closing makes it reconnect
		}
		io.WriteString(conn, "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return err
	}
	// ChatGPT fails every request waiting on a connection attempt that does
	// not complete its handshake (it gives up after about 15 s), and a failed
	// permission-profile request leaves the composer blocked with "Permission
	// mode is unavailable" until ChatGPT restarts. So when the cloud link is
	// down (network change, weak network) the handshake is still answered at
	// once from the remembered response and ChatGPT's requests wait here until
	// the cloud is back, instead of failing.
	early := b.earlyUpgrade(head)
	if early != nil && b.knownEarlyInput() {
		if _, err := conn.Write(early); err != nil {
			return err
		}
		answered = true
	}
	buffered, _ := reader.Peek(reader.Buffered())
	pending := append(head, buffered...)
	reader.Discard(len(buffered))
	// ChatGPT's bytes are read on their own goroutine so a cloud stream can be
	// replaced before it is confirmed without losing any of them.
	incoming := make(chan []byte, 64)
	go func() {
		defer close(incoming)
		buffer := make([]byte, terminalstream.Chunk)
		for {
			length, err := reader.Read(buffer)
			if length > 0 {
				select {
				case incoming <- append([]byte(nil), buffer[:length]...):
				case <-operation.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	openWait := chatgptOpenWait
	if answered {
		openWait = chatgptHeldOpenWait
	}
	deadline := time.Now().Add(openWait)
	var session *terminalstream.Session
	var window *terminalstream.CreditWindow
	var receipt terminalstream.Frame
	replay := pending
	for {
		opened, earlyInput, err := b.open(operation, max(time.Until(deadline), time.Second))
		if err != nil {
			return unavailable(err)
		}
		b.rememberEarlyInput(earlyInput)
		if answered && !earlyInput {
			opened.Close()
			return fmt.Errorf("cloud service cannot accept an early handshake") // ChatGPT reconnects and waits for the real one
		}
		if !answered && early != nil && earlyInput {
			if _, err := conn.Write(early); err != nil {
				opened.Close()
				return err
			}
			answered = true
		}
		credit := terminalstream.NewCreditWindow()
		forwarder := newChatGPTForwarder(opened, credit)
		go forwarder.run(replay, incoming, cancel)
		// The upgrade request travels with the open instead of after the
		// receipt: the cloud queues input until its app-server socket is
		// connected, which saves one round trip per connection.
		wait, stopWaiting := context.WithTimeout(operation, 45*time.Second)
		frame, err := opened.Next(wait)
		stopWaiting()
		if err == nil && frame.Type == "receipt" {
			forwarder.confirm()
			session, window, receipt = opened, credit, frame
			break
		}
		// Nothing has come back from the app-server yet, so the same bytes can
		// go to a fresh stream: a half-open channel after a network change is
		// replaced within the heartbeat timeout and ChatGPT never notices.
		replay = append(replay, forwarder.abandon()...)
		credit.Close()
		opened.Close()
		if !answered || operation.Err() != nil || time.Now().After(deadline) {
			return unavailable(fmt.Errorf("cloud codex app-server unavailable"))
		}
	}
	defer session.Close()
	defer window.Close()
	b.track(conn, receipt.Message)
	defer b.track(conn, "")
	// Compressed output is decoded on its own goroutine; writes into the pipe
	// block until ChatGPT has received the bytes, so credits still pace the
	// remote stream.
	sink := &upgradeReader{bridge: b, dst: conn, early: answered}
	var output io.Writer = sink
	decoded := make(chan error, 1)
	if receipt.Term == terminalstream.SocketCompressionZstd {
		reader, writer := io.Pipe()
		defer writer.Close()
		decoder, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(terminalstream.SocketCompressionWindow))
		if err != nil {
			return err
		}
		go func() {
			_, err := io.Copy(sink, decoder)
			decoder.Close()
			reader.CloseWithError(io.ErrClosedPipe)
			decoded <- err
			cancel()
		}()
		output = writer
	} else {
		close(decoded)
	}
	for {
		frame, err := session.Next(operation)
		if err != nil {
			return err
		}
		switch frame.Type {
		case "data":
			if _, err := output.Write(frame.Data); err != nil {
				return err
			}
			if err := session.Send(terminalstream.Frame{Type: "credit", Bytes: len(frame.Data)}); err != nil {
				return err
			}
		case "input_credit":
			if !window.Add(frame.Bytes) {
				return fmt.Errorf("invalid app-server input credit")
			}
		case "exit":
			if writer, ok := output.(*io.PipeWriter); ok {
				writer.Close()
				return <-decoded
			}
			return nil
		case "error":
			return fmt.Errorf("remote app-server stream failed")
		}
	}
}

// open waits for the daemon's resident terminal channel, then opens a socket
// stream on it without waiting for the cloud receipt. It never starts another
// SSH connection or exec channel.
func (b *chatgptBridge) open(ctx context.Context, patience time.Duration) (*terminalstream.Session, bool, error) {
	wait, cancel := context.WithTimeout(ctx, patience)
	defer cancel()
	var transport *terminalstream.Transport
	for transport == nil {
		b.services.mu.Lock()
		channel := b.services.channel
		b.services.mu.Unlock()
		if channel != nil {
			current, err := channel.get(wait)
			if err == nil && current != nil && current.Ready(wait) == nil {
				transport = current
				break
			}
		}
		select {
		case <-wait.Done():
			return nil, false, fmt.Errorf("cloud runtime channel unavailable")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !transport.Supports(terminalstream.CapabilityCodexAppServer) {
		return nil, false, fmt.Errorf("cloud terminal service does not support codex app-server yet")
	}
	var nonce [16]byte
	rand.Read(nonce[:])
	b.services.mu.Lock()
	device := b.services.config.Device
	b.services.mu.Unlock()
	session, err := transport.Launch(terminalstream.Frame{Type: "socket", ID: hex.EncodeToString(nonce[:]), Target: terminalstream.SocketCodexAppServer, Message: device, Term: terminalstream.SocketCompressionZstd})
	return session, transport.Supports(terminalstream.CapabilityCodexAppServerEarlyInput), err
}

// track records which cloud app-server mode serves a connection; an empty mode
// forgets it.
func (b *chatgptBridge) track(conn net.Conn, mode string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conns == nil {
		b.conns = map[net.Conn]string{}
	}
	if mode == "" {
		delete(b.conns, conn)
	} else {
		b.conns[conn] = mode
	}
}

// reconcileMode closes ChatGPT connections served by the other agent mode once
// the cloud configuration for magpie has been applied. ChatGPT reconnects and
// the new stream reaches the app-server for the current mode.
func (b *chatgptBridge) reconcileMode(magpie bool) int {
	expected := cloudruntime.CodexAppServerNative
	if magpie {
		expected = cloudruntime.CodexAppServerGateway
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	closed := 0
	for conn, mode := range b.conns {
		if mode != expected {
			conn.Close()
			delete(b.conns, conn)
			closed++
		}
	}
	return closed
}

func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".chatgpt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// location reports whether Codex Desktop is set to the cloud computer and
// whether the loopback endpoint is serving.
func (b *chatgptBridge) location() (enabled, listening bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pref.Enabled, b.listener != nil
}

// switchLocation is the Agents tab's location switch. The user has already
// confirmed that a running ChatGPT.app restarts; the restart runs in the
// background so the switch acknowledges immediately.
func (b *chatgptBridge) switchLocation(enabled bool) error {
	b.mu.Lock()
	if b.ctx == nil {
		b.mu.Unlock()
		return &control.Fault{Code: "busy", Message: "Daemon is starting", Retryable: true}
	}
	b.pref.Enabled = enabled
	var err error
	if enabled {
		err = b.enableLocked()
	} else {
		err = b.disableLocked()
	}
	url := ""
	if enabled {
		url = b.url()
	}
	ctx := b.ctx
	b.mu.Unlock()
	if err != nil {
		return &control.Fault{Code: "chatgpt_bridge_failed", Message: "Unable to update the Codex Desktop location"}
	}
	go func() {
		restart, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if _, err := b.restartApplication(restart, url); err != nil {
			b.mu.Lock()
			b.lastErr = err.Error()
			b.mu.Unlock()
		}
	}()
	return nil
}

func (s *daemonServices) setCodexDesktop(raw json.RawMessage) (any, bool, error) {
	var request struct {
		Agent   string `json:"agent"`
		Enabled *bool  `json:"enabled"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Agent != agent.CodexDesktopID {
		return nil, false, nil
	}
	if request.Enabled == nil || s.chatgpt == nil {
		return nil, true, &control.Fault{Code: "invalid_params", Message: "Explicit enabled value required"}
	}
	if err := s.chatgpt.switchLocation(*request.Enabled); err != nil {
		return nil, true, err
	}
	return s.snapshot(), true, nil
}

// chatgptForwarder sends ChatGPT's bytes to one cloud stream. Until the stream
// is confirmed it records what it took from ChatGPT so a replacement stream
// can replay it.
type chatgptForwarder struct {
	session *terminalstream.Session
	window  *terminalstream.CreditWindow
	stop    chan struct{}
	done    chan struct{}

	mu        sync.Mutex
	recording bool
	sent      []byte
}

func newChatGPTForwarder(session *terminalstream.Session, window *terminalstream.CreditWindow) *chatgptForwarder {
	return &chatgptForwarder{session: session, window: window, stop: make(chan struct{}), done: make(chan struct{}), recording: true}
}

func (f *chatgptForwarder) run(replay []byte, incoming <-chan []byte, closeConnection context.CancelFunc) {
	defer close(f.done)
	send := func(data []byte) bool {
		for len(data) > 0 {
			count := f.window.Take(min(len(data), terminalstream.Chunk))
			if count == 0 {
				return false
			}
			if f.session.Send(terminalstream.Frame{Type: "input", Data: append([]byte(nil), data[:count]...)}) != nil {
				return false
			}
			data = data[count:]
		}
		return true
	}
	// A failure before confirmation leaves the connection to the retry loop.
	failed := func() {
		f.mu.Lock()
		confirmed := !f.recording
		f.mu.Unlock()
		if confirmed {
			closeConnection()
		}
	}
	if !send(replay) {
		failed()
		return
	}
	for {
		select {
		case <-f.stop:
			return
		case chunk, ok := <-incoming:
			if !ok {
				closeConnection() // ChatGPT closed its connection
				return
			}
			f.mu.Lock()
			if f.recording {
				f.sent = append(f.sent, chunk...)
			}
			f.mu.Unlock()
			if !send(chunk) {
				failed()
				return
			}
		}
	}
}

// confirm stops recording once the cloud stream is established.
func (f *chatgptForwarder) confirm() {
	f.mu.Lock()
	f.recording = false
	f.sent = nil
	f.mu.Unlock()
}

// abandon stops the forwarder and returns every byte it took from ChatGPT,
// which the next stream replays.
func (f *chatgptForwarder) abandon() []byte {
	close(f.stop)
	f.window.Close() // unblocks a send waiting for credit
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	sent := f.sent
	f.sent = nil
	return sent
}
