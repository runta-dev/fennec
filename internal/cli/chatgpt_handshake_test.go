package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestEarlyUpgradeReplaysTheLastResponse(t *testing.T) {
	b := &chatgptBridge{}
	request := []byte("GET /rpc HTTP/1.1\r\nHost: x\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	if b.earlyUpgrade(request) != nil {
		t.Fatal("no response is known before the first connection")
	}
	// The first connection waits for the app-server and keeps its response.
	var first bytes.Buffer
	response := "HTTP/1.1 101 Switching Protocols\r\nupgrade: websocket\r\nsec-websocket-accept: abc=\r\nx-codex-websocket-max-unfragmented-message-bytes: 16777216\r\n\r\n"
	reader := &upgradeReader{bridge: b, dst: &first}
	reader.Write([]byte(response[:20]))
	reader.Write([]byte(response[20:] + "\x81\x02hi"))
	if first.String() != response+"\x81\x02hi" {
		t.Fatalf("%q", first.String())
	}
	early := string(b.earlyUpgrade(request))
	if !strings.HasPrefix(early, "HTTP/1.1 101 Switching Protocols\r\n") || !strings.Contains(early, "x-codex-websocket-max-unfragmented-message-bytes: 16777216\r\n") ||
		!strings.HasSuffix(early, "sec-websocket-accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n") || strings.Contains(early, "abc=") {
		t.Fatalf("%q", early)
	}
	// Later connections drop the app-server's own response from the stream.
	var later bytes.Buffer
	reader = &upgradeReader{bridge: b, dst: &later, early: true}
	reader.Write([]byte(response + "\x81\x02ok"))
	if later.String() != "\x81\x02ok" {
		t.Fatalf("%q", later.String())
	}
	// A refusal after an early answer closes the connection.
	reader = &upgradeReader{bridge: b, dst: &later, early: true}
	if _, err := reader.Write([]byte("HTTP/1.1 503 Service Unavailable\r\n\r\n")); err == nil {
		t.Fatal("refusal accepted")
	}
	if b.earlyUpgrade([]byte("GET /rpc HTTP/1.1\r\nSec-WebSocket-Key: k\r\nSec-WebSocket-Protocol: x\r\n\r\n")) != nil {
		t.Fatal("subprotocol negotiation answered from memory")
	}
}
