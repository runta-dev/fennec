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

func TestChatGPTPathFilterMovesThreadsIntoFennecWorkspaces(t *testing.T) {
	home := t.TempDir()
	client, filterSide := net.Pipe()
	serverSide, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	filter := NewChatGPTPathFilter(serverSide, home, "1234abcd")
	// Relay like the terminal stream does: client bytes are written into the
	// filter, server bytes are read from it.
	go io.Copy(filter, filterSide)
	go io.Copy(filterSide, filter)
	deadline := time.Now().Add(5 * time.Second)
	client.SetDeadline(deadline)
	server.SetDeadline(deadline)

	mac := "/Users/developer/repos/mockuplabs"
	request, _ := json.Marshal(map[string]any{"id": 1, "method": "thread/start", "params": map[string]any{"cwd": mac, "model": "zai/glm-5.3"}})
	upgrade := "GET /rpc HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nX-Codex-App-Server-Compression: zstd-stream-v1\r\n\r\n"
	half := len(request) / 2
	go func() {
		io.WriteString(client, upgrade)
		// A fragmented masked text message with a ping between fragments.
		client.Write(maskedFrame(0x1, false, request[:half]))
		client.Write(maskedFrame(0x9, true, []byte("p")))
		client.Write(maskedFrame(0x0, true, request[half:]))
	}()

	reader := bufio.NewReader(server)
	var head strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		head.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	if strings.Contains(strings.ToLower(head.String()), "compression") || !strings.Contains(head.String(), "Upgrade: websocket") {
		t.Fatalf("handshake not filtered: %q", head.String())
	}
	ping := readFrame(t, reader)
	if ping.opcode != 0x9 {
		t.Fatal("control frame not passed through", ping.opcode)
	}
	message := readFrame(t, reader)
	var received struct {
		Params struct {
			Cwd   string `json:"cwd"`
			Model string `json:"model"`
		} `json:"params"`
	}
	if err := json.Unmarshal(message.payload, &received); err != nil || received.Params.Model != "zai/glm-5.3" {
		t.Fatal(string(message.payload), err)
	}
	cloud := received.Params.Cwd
	if filepath.Dir(cloud) != filepath.Join(home, ".fen") {
		t.Fatal("cwd not moved to a Fennec workspace", cloud)
	}
	instructions, err := os.ReadFile(filepath.Join(cloud, "AGENTS.md"))
	if err != nil || !strings.Contains(string(instructions), mac) || !strings.Contains(string(instructions), "You run in the cloud") {
		t.Fatal("Fennec instructions missing", string(instructions), err)
	}

	// The server reports the cloud directory; ChatGPT sees its Mac project.
	go func() {
		io.WriteString(server, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n\r\n")
		reply, _ := json.Marshal(map[string]any{"id": 1, "result": map[string]any{"thread": map[string]any{"id": "t1", "cwd": cloud}}})
		server.Write(encodeWebSocketFrame(0x1, reply, false))
	}()
	clientReader := bufio.NewReader(client)
	for {
		line, err := clientReader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	answer := readFrame(t, clientReader)
	if !bytes.Contains(answer.payload, []byte(`"cwd":"`+mac+`"`)) || bytes.Contains(answer.payload, []byte(".fen")) {
		t.Fatal("response cwd not mapped back", string(answer.payload))
	}
	// The mapping survives a new connection.
	if loadChatGPTPaths(home)[cloud] != mac {
		t.Fatal("path mapping not persisted")
	}
}

func TestRewriteCwdLeavesOtherMessagesUntouched(t *testing.T) {
	message := []byte(`{"method":"turn/started","params":{"text":"cwd is fine"}}`)
	if got := rewriteCwd(message, func(string) (string, bool) { return "/x", true }); !bytes.Equal(got, message) {
		t.Fatal(string(got))
	}
	large := []byte(`{"id":12345678901234567890,"params":{"cwd":"/a","items":[{"cwd":"/a"}]}}`)
	got := string(rewriteCwd(large, func(path string) (string, bool) { return "/b" + path, true }))
	if !strings.Contains(got, "12345678901234567890") || strings.Count(got, `"/b/a"`) != 2 {
		t.Fatal(got)
	}
}

func maskedFrame(opcode byte, fin bool, payload []byte) []byte {
	frame := encodeWebSocketFrame(opcode, payload, true)
	if !fin {
		frame[0] &^= 0x80
	}
	return frame
}

func readFrame(t *testing.T, reader *bufio.Reader) webSocketFrame {
	t.Helper()
	var buffer []byte
	chunk := make([]byte, 4096)
	for {
		if frame, size, err := parseWebSocketFrame(buffer); err != nil {
			t.Fatal(err)
		} else if size > 0 {
			// Return unread bytes to the stream for the next call.
			rest := append([]byte(nil), buffer[size:]...)
			if len(rest) > 0 {
				*reader = *bufio.NewReader(io.MultiReader(bytes.NewReader(rest), reader))
			}
			return frame
		}
		length, err := reader.Read(chunk)
		if err != nil {
			t.Fatal(err)
		}
		buffer = append(buffer, chunk[:length]...)
	}
}

func TestRewriteCwdKeepsFileURIForm(t *testing.T) {
	message := []byte(`{"params":{"cwd":"file:///Users/a/p","command":["ls"]}}`)
	got := string(rewriteCwd(message, func(path string) (string, bool) { return "/root/.fen/x", path == "/Users/a/p" }))
	if !strings.Contains(got, `"cwd":"file:///root/.fen/x"`) {
		t.Fatal(got)
	}
}

func TestRequestsDisableDesktopOnlyMCPServers(t *testing.T) {
	request := []byte(`{"id":3,"method":"thread/start","params":{"cwd":"/a","config":{"mcp_servers.node_repl":{"command":"/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl","args":["x"],"env":{"K":"V"}},"mcp_servers.team":{"command":"npx"}}}}`)
	got := string(rewriteMessage(request, true, func(string) (string, bool) { return "", false }))
	if strings.Contains(got, "/Applications/") || !strings.Contains(got, `"mcp_servers.node_repl":{"command":"","enabled":false}`) || !strings.Contains(got, `"command":"npx"`) {
		t.Fatal(got)
	}
	batch := []byte(`{"id":4,"method":"config/batchWrite","params":{"edits":[{"keyPath":"mcp_servers.cua_repl","value":{"command":"/Applications/ChatGPT.app/Contents/MacOS/ChatGPT","args":["mcp"]},"mergeStrategy":"replace"}]}}`)
	if got := string(rewriteMessage(batch, true, func(string) (string, bool) { return "", false })); !strings.Contains(got, `"value":{"command":"","enabled":false}`) || !strings.Contains(got, `"keyPath":"mcp_servers.cua_repl"`) {
		t.Fatal(got)
	}
	// Responses to ChatGPT are never altered for MCP commands.
	response := []byte(`{"id":5,"result":{"command":"/Applications/ChatGPT.app/x"}}`)
	if got := rewriteCwd(response, func(string) (string, bool) { return "", false }); !bytes.Equal(got, response) {
		t.Fatal(string(got))
	}
}
