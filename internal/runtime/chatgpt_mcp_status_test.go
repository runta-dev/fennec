package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// statusHarness connects a fake ChatGPT and a fake app-server through one
// filter, past the WebSocket handshake.
type statusHarness struct {
	client, server             net.Conn
	clientReader, serverReader *bufio.Reader
	filter                     net.Conn
}

type homeConn struct {
	net.Conn
	home string
}

func (conn homeConn) CodexHome() string { return conn.home }

func newStatusHarness(t *testing.T, home string) *statusHarness {
	t.Helper()
	client, filterSide := net.Pipe()
	serverSide, server := net.Pipe()
	filter := NewChatGPTPathFilter(homeConn{serverSide, home + "/codex"}, home, "")
	go io.Copy(filter, filterSide)
	go io.Copy(filterSide, filter)
	deadline := time.Now().Add(5 * time.Second)
	client.SetDeadline(deadline)
	server.SetDeadline(deadline)
	h := &statusHarness{client: client, server: server, clientReader: bufio.NewReader(client), serverReader: bufio.NewReader(server), filter: filter}
	go io.WriteString(client, "GET /rpc HTTP/1.1\r\nUpgrade: websocket\r\n\r\n")
	readHead(t, h.serverReader)
	go io.WriteString(server, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
	readHead(t, h.clientReader)
	t.Cleanup(func() { client.Close(); server.Close(); filter.Close() })
	return h
}

func readHead(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			return
		}
	}
}

func (h *statusHarness) request(t *testing.T, message string) {
	t.Helper()
	go h.client.Write(maskedFrame(0x1, true, []byte(message)))
}

func (h *statusHarness) serverReceives(t *testing.T) jsonRPCRequest {
	t.Helper()
	var request jsonRPCRequest
	if err := json.Unmarshal(readFrame(t, h.serverReader).payload, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func (h *statusHarness) reply(id json.RawMessage, result string) {
	go h.server.Write(encodeWebSocketFrame(0x1, []byte(`{"id":`+string(id)+`,"result":`+result+`}`), false))
}

func (h *statusHarness) clientReceives(t *testing.T) jsonRPCResponse {
	t.Helper()
	var response jsonRPCResponse
	if err := json.Unmarshal(readFrame(t, h.clientReader).payload, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

// statusClock lets a test move time past the freshness window.
func statusClock(t *testing.T) *time.Time {
	now := time.Now()
	previous := mcpStatusNow
	mcpStatusNow = func() time.Time { return now }
	t.Cleanup(func() { mcpStatusNow = previous })
	return &now
}

func TestMCPStatusAnsweredFromStoreAndRefreshedInBackground(t *testing.T) {
	clock := statusClock(t)
	home := t.TempDir()
	first := newStatusHarness(t, home)
	first.request(t, `{"id":1,"method":"mcpServerStatus/list","params":{"detail":"toolsAndAuthOnly","limit":100}}`)
	forwarded := first.serverReceives(t)
	if string(forwarded.ID) != "1" {
		t.Fatal("first request must reach the app-server", string(forwarded.ID))
	}
	first.reply(forwarded.ID, `{"data":[{"name":"node_repl","tools":{"a":{}}}]}`)
	if got := first.clientReceives(t); string(got.ID) != "1" || !bytes.Contains(got.Result, []byte("node_repl")) {
		t.Fatal(string(got.Result))
	}

	// Once the answer is no longer fresh, a new connection (ChatGPT
	// relaunched) is answered at once, in any key order, while the app-server
	// refreshes under a private ID.
	*clock = clock.Add(mcpStatusFresh + time.Second)
	second := newStatusHarness(t, home)
	second.request(t, `{"id":"a7","method":"mcpServerStatus/list","params":{"limit":100,"detail":"toolsAndAuthOnly"}}`)
	if got := second.clientReceives(t); string(got.ID) != `"a7"` || !bytes.Contains(got.Result, []byte("node_repl")) {
		t.Fatal(string(got.ID), string(got.Result))
	}
	refresh := second.serverReceives(t)
	if !strings.HasPrefix(string(refresh.ID), `"`+mcpStatusRefreshID) || refresh.Method != mcpStatusMethod {
		t.Fatal("no background refresh", string(refresh.ID))
	}
	// The refresh answer updates the store but never reaches ChatGPT.
	second.reply(refresh.ID, `{"data":[{"name":"cua_repl"}]}`)
	second.request(t, `{"id":"a8","method":"account/read","params":{}}`)
	if next := second.serverReceives(t); next.Method != "account/read" {
		t.Fatal(next.Method)
	}
	second.reply(json.RawMessage(`"a8"`), `{}`)
	if got := second.clientReceives(t); string(got.ID) != `"a8"` {
		t.Fatal("refresh response leaked to ChatGPT", string(got.ID))
	}
	second.request(t, `{"id":"a9","method":"mcpServerStatus/list","params":{"detail":"toolsAndAuthOnly","limit":100}}`)
	if got := second.clientReceives(t); !bytes.Contains(got.Result, []byte("cua_repl")) {
		t.Fatal("refreshed status not served", string(got.Result))
	}
	// The refresh just confirmed it: no further app-server request (each one
	// starts the Mac's desktop tools over SSH).
	second.request(t, `{"id":"a10","method":"account/read","params":{}}`)
	if next := second.serverReceives(t); next.Method != "account/read" {
		t.Fatal("fresh status refreshed again", next.Method, string(next.ID))
	}
}

func TestMCPStatusFollowsConfigChangesAndSkipsThreads(t *testing.T) {
	statusClock(t)
	home := t.TempDir()
	config := filepath.Join(home, "codex", "config.toml")
	os.MkdirAll(filepath.Dir(config), 0700)
	os.WriteFile(config, []byte("[mcp_servers.a]\ncommand = 'a'\n"), 0600)
	h := newStatusHarness(t, home)
	h.request(t, `{"id":1,"method":"mcpServerStatus/list","params":{}}`)
	h.reply(h.serverReceives(t).ID, `{"data":[{"name":"a"}]}`)
	h.clientReceives(t)
	// ChatGPT rewrites the same entries when it connects: still answered.
	h.request(t, `{"id":2,"method":"config/batchWrite","params":{"edits":[]}}`)
	h.reply(h.serverReceives(t).ID, `{}`)
	h.clientReceives(t)
	h.request(t, `{"id":3,"method":"mcpServerStatus/list","params":{}}`)
	if got := h.clientReceives(t); string(got.ID) != "3" || !bytes.Contains(got.Result, []byte(`"a"`)) {
		t.Fatal("unchanged config lost the stored status", string(got.Result))
	}
	// A real change asks the app-server again.
	os.WriteFile(config, []byte("[mcp_servers.b]\ncommand = 'b'\n"), 0600)
	h.request(t, `{"id":4,"method":"mcpServerStatus/list","params":{}}`)
	if got := h.serverReceives(t); string(got.ID) != "4" {
		t.Fatal("status served after a config change", string(got.ID))
	}
	h.reply(json.RawMessage("4"), `{"data":[{"name":"b"}]}`)
	if got := h.clientReceives(t); !bytes.Contains(got.Result, []byte(`"b"`)) {
		t.Fatal(string(got.Result))
	}
	// Plugin changes clear the store.
	h.request(t, `{"id":5,"method":"plugin/install","params":{}}`)
	h.reply(h.serverReceives(t).ID, `{}`)
	h.clientReceives(t)
	h.request(t, `{"id":6,"method":"mcpServerStatus/list","params":{}}`)
	if got := h.serverReceives(t); string(got.ID) != "6" {
		t.Fatal("status served after a plugin install", string(got.ID))
	}
	h.reply(json.RawMessage("6"), `{"data":[]}`)
	h.clientReceives(t)
	// Thread-scoped requests reuse the thread's MCP connections and pass through.
	h.request(t, `{"id":7,"method":"mcpServerStatus/list","params":{"threadId":"t1"}}`)
	if got := h.serverReceives(t); string(got.ID) != "7" {
		t.Fatal(string(got.ID))
	}
}
