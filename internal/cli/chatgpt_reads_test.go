package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cloudruntime "fennec/internal/runtime"
)

// delayedAppServer answers JSON-RPC like codex app-server; methods listed in
// slow are answered only after their channel is closed.
type delayedAppServer struct {
	mu   sync.Mutex
	slow map[string]chan struct{}
}

func (server *delayedAppServer) dial(context.Context, string) (net.Conn, error) {
	local, remote := net.Pipe()
	go func() {
		defer remote.Close()
		reader := bufio.NewReader(remote)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		io.WriteString(remote, "HTTP/1.1 101 Switching Protocols\r\nupgrade: websocket\r\nsec-websocket-accept: x\r\n\r\n")
		var writes sync.Mutex
		var buffer []byte
		chunk := make([]byte, 4096)
		for {
			n, err := reader.Read(chunk)
			buffer = append(buffer, chunk[:n]...)
			for {
				_, _, payload, size, parseErr := cloudruntime.ParseWebSocketFrame(buffer)
				if parseErr != nil || size == 0 {
					break
				}
				buffer = buffer[size:]
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.Unmarshal(payload, &request) != nil || len(request.ID) == 0 {
					continue
				}
				reply, _ := json.Marshal(map[string]any{"id": request.ID, "result": map[string]any{"from": "cloud", "method": request.Method}})
				server.mu.Lock()
				gate := server.slow[request.Method]
				server.mu.Unlock()
				if gate == nil {
					writes.Lock()
					remote.Write(cloudruntime.EncodeWebSocketFrame(0x1, reply, false))
					writes.Unlock()
					continue
				}
				go func() {
					<-gate
					writes.Lock()
					remote.Write(cloudruntime.EncodeWebSocketFrame(0x1, reply, false))
					writes.Unlock()
				}()
			}
			if err != nil {
				return
			}
		}
	}()
	return local, nil
}

// readsBridge is a bridge that has served ChatGPT before, listening on
// loopback with the given cloud services.
func readsBridge(t *testing.T, ctx context.Context, services *daemonServices) (*chatgptBridge, string) {
	t.Helper()
	bridge := newChatGPTBridge(t.TempDir(), services)
	bridge.path = ""
	bridge.pref.Token = bridgeTestToken
	bridge.pref.EarlyInput = true
	bridge.pref.Initialize = `{"from":"remembered"}`
	bridge.upgrade = []byte("HTTP/1.1 101 Switching Protocols\r\nupgrade: websocket\r\n")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go bridge.accept(ctx, listener)
	return bridge, listener.Addr().String()
}

// openChatGPT connects like ChatGPT and returns once the handshake answer
// arrived; initialize is sent behind the upgrade.
func openChatGPT(t *testing.T, address string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	io.WriteString(conn, "GET /fennec/"+bridgeTestToken+" HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	sendRequest(conn, `{"id":"__codex_initialize__","method":"initialize","params":{}}`)
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, reader
}

func sendRequest(conn net.Conn, message string) {
	conn.Write(cloudruntime.EncodeWebSocketFrame(0x1, []byte(message), true))
}

func resultOf(t *testing.T, message map[string]any) map[string]any {
	t.Helper()
	result, ok := message["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", message)
	}
	return result
}

func setReadBounds(t *testing.T, initialize, unconfirmed, confirmed time.Duration) {
	t.Helper()
	previous := [3]time.Duration{chatgptLocalInitializeAfter, chatgptLocalReadAfter, chatgptLocalReadAfterConfirmed}
	chatgptLocalInitializeAfter, chatgptLocalReadAfter, chatgptLocalReadAfterConfirmed = initialize, unconfirmed, confirmed
	t.Cleanup(func() {
		chatgptLocalInitializeAfter, chatgptLocalReadAfter, chatgptLocalReadAfterConfirmed = previous[0], previous[1], previous[2]
	})
}

func connectCloud(t *testing.T, ctx context.Context, services *daemonServices, server *delayedAppServer) {
	t.Helper()
	channel := startFakeCloudWith(t, ctx, server.dial)
	services.mu.Lock()
	services.channel = channel
	services.mu.Unlock()
}

// ChatGPT starts its 30 s clock for configRequirements/read as soon as the
// bridge answered initialize; while the cloud is away the bridge answers the
// read from the app-server's last answer, and the app-server's own answer to
// it, arriving once the cloud is back, never reaches ChatGPT.
func TestChatGPTBridgeAnswersStartupReadsWhileTheCloudIsAway(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	setReadBounds(t, 100*time.Millisecond, 200*time.Millisecond, 5*time.Second)
	services := &daemonServices{} // the resident channel is reconnecting
	bridge, address := readsBridge(t, ctx, services)
	bridge.rememberRead("", `configRequirements/read null`, json.RawMessage(`{"requirements":null,"from":"remembered"}`))

	conn, reader := openChatGPT(t, address)
	if answer := readServerMessage(t, reader); answer["id"] != "__codex_initialize__" {
		t.Fatalf("initialize: %v", answer)
	}
	sent := time.Now()
	sendRequest(conn, `{"id":"configRequirements/read:1","method":"configRequirements/read"}`)
	answer := readServerMessage(t, reader)
	waited := time.Since(sent)
	if answer["id"] != "configRequirements/read:1" || resultOf(t, answer)["from"] != "remembered" {
		t.Fatalf("configRequirements/read not answered while the cloud was away: %v", answer)
	}
	if waited > 2*time.Second {
		t.Fatalf("answered after %v; ChatGPT gives up after 30 s", waited)
	}

	// The cloud comes back: the held requests reach the app-server, whose
	// answers to initialize and the read are dropped. The next request gets
	// the cloud's answer, and nothing else arrives in between.
	connectCloud(t, ctx, services, &delayedAppServer{})
	sendRequest(conn, `{"id":"thread/list:2","method":"thread/list","params":{}}`)
	next := readServerMessage(t, reader)
	if next["id"] != "thread/list:2" || resultOf(t, next)["from"] != "cloud" {
		t.Fatalf("ChatGPT got %v; want only the cloud's answer to thread/list", next)
	}
	// The app-server's real answer replaces the remembered one.
	deadline := time.Now().Add(5 * time.Second)
	for string(bridge.cachedRead(`configRequirements/read null`)) != `{"from":"cloud","method":"configRequirements/read"}` {
		if time.Now().After(deadline) {
			t.Fatalf("cloud answer not remembered: %s", bridge.cachedRead(`configRequirements/read null`))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Without a remembered answer nothing is made up: the read waits for the cloud.
// Requests with side effects are never answered here.
func TestChatGPTBridgeHoldsReadsWithoutARememberedAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	setReadBounds(t, 100*time.Millisecond, 100*time.Millisecond, 5*time.Second)
	services := &daemonServices{}
	bridge, address := readsBridge(t, ctx, services)
	bridge.rememberRead("", `account/read {"refreshToken":true}`, json.RawMessage(`{"from":"remembered"}`))

	conn, reader := openChatGPT(t, address)
	readServerMessage(t, reader) // initialize
	sendRequest(conn, `{"id":"pp","method":"permissionProfile/list","params":{}}`)
	sendRequest(conn, `{"id":"acct","method":"account/read","params":{"refreshToken":true}}`)
	sendRequest(conn, `{"id":"start","method":"thread/start","params":{}}`)
	time.Sleep(500 * time.Millisecond)
	connectCloud(t, ctx, services, &delayedAppServer{})
	for _, want := range []string{"pp", "acct", "start"} {
		message := readServerMessage(t, reader)
		if message["id"] != want || resultOf(t, message)["from"] != "cloud" {
			t.Fatalf("got %v; want the cloud's answer to %s", message, want)
		}
	}
}

// On an established stream whose app-server does not answer (a half-open
// channel after a network change), a startup read is answered before
// ChatGPT's 30 s run out; the late answer is dropped.
func TestChatGPTBridgeAnswersStalledReadsOnAnEstablishedStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	setReadBounds(t, time.Second, time.Second, 300*time.Millisecond)
	gate := make(chan struct{})
	server := &delayedAppServer{slow: map[string]chan struct{}{"config/read": gate}}
	services := &daemonServices{}
	connectCloud(t, ctx, services, server)
	bridge, address := readsBridge(t, ctx, services)
	bridge.rememberRead("", `config/read {"includeLayers":false,"cwd":null}`, json.RawMessage(`{"config":{},"from":"remembered"}`))

	conn, reader := openChatGPT(t, address)
	if answer := readServerMessage(t, reader); resultOf(t, answer)["from"] != "cloud" {
		t.Fatalf("initialize not answered by the cloud: %v", answer)
	}
	sent := time.Now()
	sendRequest(conn, `{"id":"cfg","method":"config/read","params":{"includeLayers":false,"cwd":null}}`)
	answer := readServerMessage(t, reader)
	if answer["id"] != "cfg" || resultOf(t, answer)["from"] != "remembered" {
		t.Fatalf("stalled config/read: %v", answer)
	}
	if waited := time.Since(sent); waited < 250*time.Millisecond || waited > 3*time.Second {
		t.Fatalf("answered after %v; want the established-stream bound", waited)
	}
	close(gate) // the app-server's late answer must not reach ChatGPT
	sendRequest(conn, `{"id":"next","method":"thread/list","params":{}}`)
	if next := readServerMessage(t, reader); next["id"] != "next" {
		t.Fatalf("ChatGPT got %v; want only the answer to its next request", next)
	}
}

// With the cloud present every request is answered by the app-server at its
// own pace, and the answers are remembered for later outages.
func TestChatGPTBridgeLeavesTheNormalPathToTheCloud(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	services := &daemonServices{}
	connectCloud(t, ctx, services, &delayedAppServer{})
	bridge, address := readsBridge(t, ctx, services)
	bridge.rememberRead("", `model/list {"cursor":null}`, json.RawMessage(`{"from":"remembered"}`))

	conn, reader := openChatGPT(t, address)
	readServerMessage(t, reader)
	started := time.Now()
	sendRequest(conn, `{"id":"ml","method":"model/list","params":{"cursor": null}}`)
	sendRequest(conn, `{"id":"cr","method":"configRequirements/read"}`)
	for _, want := range []string{"ml", "cr"} {
		message := readServerMessage(t, reader)
		if message["id"] != want || resultOf(t, message)["from"] != "cloud" {
			t.Fatalf("got %v; want the cloud's answer to %s", message, want)
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cloud answers took %v", elapsed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for bridge.cachedRead(`configRequirements/read null`) == nil || string(bridge.cachedRead(`model/list {"cursor":null}`)) == `{"from":"remembered"}` {
		if time.Now().After(deadline) {
			t.Fatal("cloud answers not remembered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChatGPTReadKey(t *testing.T) {
	for _, test := range []struct {
		method, params, key string
		ok                  bool
	}{
		{"configRequirements/read", ``, `configRequirements/read null`, true},
		{"config/read", `{ "includeLayers": false, "cwd": "/root" }`, `config/read {"includeLayers":false,"cwd":"/root"}`, true},
		{"account/read", `{"refreshToken":false}`, `account/read {"refreshToken":false}`, true},
		{"account/read", `{"refreshToken":true}`, ``, false},
		{"thread/start", `{}`, ``, false},
		{"turn/start", `{}`, ``, false},
		{"thread/read", `{}`, ``, false},
		{"config/batchWrite", `{}`, ``, false},
	} {
		key, ok := chatgptReadKey(test.method, json.RawMessage(test.params))
		if key != test.key || ok != test.ok {
			t.Errorf("%s %s: %q %v", test.method, test.params, key, ok)
		}
	}
}

// Answers survive a daemon restart and belong to one app-server mode.
func TestChatGPTReadsPersistPerMode(t *testing.T) {
	directory := t.TempDir()
	bridge := newChatGPTBridge(directory, &daemonServices{})
	bridge.loadReadsLocked()
	t.Cleanup(func() { waitReadsSaved(bridge) })
	bridge.rememberRead("gateway", "configRequirements/read null", json.RawMessage(`{"requirements":null}`))
	path := filepath.Join(directory, ".config/fennec/chatgpt-reads.json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		restarted := newChatGPTBridge(directory, &daemonServices{})
		restarted.loadReadsLocked()
		t.Cleanup(func() { waitReadsSaved(restarted) })
		if string(restarted.cachedRead("configRequirements/read null")) == `{"requirements":null}` {
			restarted.noteReadMode("gateway")
			if restarted.cachedRead("configRequirements/read null") == nil {
				t.Fatal("same mode dropped the answers")
			}
			restarted.noteReadMode("native")
			if restarted.cachedRead("configRequirements/read null") != nil {
				t.Fatal("answers of the other mode kept")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("answers not saved to %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
	bridge.rememberRead("gateway", "model/list null", make(json.RawMessage, chatgptReadResultLimit+1))
	if bridge.cachedRead("model/list null") != nil {
		t.Fatal("oversized answer kept")
	}
}

func waitReadsSaved(bridge *chatgptBridge) {
	for {
		bridge.mu.Lock()
		saving := bridge.readsSaving
		bridge.mu.Unlock()
		if !saving {
			return
		}
		time.Sleep(time.Millisecond)
	}
}
