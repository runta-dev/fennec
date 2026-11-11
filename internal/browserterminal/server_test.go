package browserterminal

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"fennec/internal/terminalstream"
	"github.com/gorilla/websocket"
)

func TestAttachmentBridgeAndAuthorization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Server{}
	defer s.Close()
	attached := make(chan net.Conn, 1)
	url, err := s.URL(ctx, "sunny-spicy", func(context.Context) (net.Conn, string, error) {
		a, b := net.Pipe()
		attached <- b
		return a, "0123456789abcdef0123456789abcdef", nil
	}, func() Metadata { return Metadata{Title: "Check local computer", Icon: "/icons/codex-color.svg"} })
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(url, "/#")
	origin := parts[0]
	token := parts[1]
	dial := func(originHeader string) (*websocket.Conn, error) {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(origin, "http")+"/socket", http.Header{"Origin": []string{originHeader}})
		return conn, err
	}
	if conn, err := dial("https://attacker.example"); err == nil {
		conn.Close()
		t.Fatal("accepted cross-origin attachment")
	}
	bad, err := dial(origin)
	if err != nil {
		t.Fatal(err)
	}
	bad.WriteJSON(map[string]any{"token": "wrong", "columns": 80, "rows": 24})
	bad.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = bad.ReadMessage(); err == nil {
		t.Fatal("accepted invalid token")
	}
	bad.Close()
	ws, err := dial(origin)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.WriteJSON(map[string]any{"token": token, "columns": 90, "rows": 30})
	var metadata Metadata
	if err := ws.ReadJSON(&metadata); err != nil || metadata.Type != "metadata" || metadata.Title != "Check local computer" || metadata.Icon != "/icons/codex-color.svg" {
		t.Fatalf("missing session branding: %+v %v", metadata, err)
	}
	var peer net.Conn
	select {
	case peer = <-attached:
	case <-time.After(time.Second):
		t.Fatal("not attached")
	}
	defer peer.Close()
	peer.SetDeadline(time.Now().Add(3 * time.Second))
	scan := bufio.NewScanner(peer)
	if !scan.Scan() {
		t.Fatal(scan.Err())
	}
	var hello terminalstream.Frame
	json.Unmarshal(scan.Bytes(), &hello)
	if hello.Type != "connect" || hello.ID != "0123456789abcdef0123456789abcdef" || hello.Columns != 90 {
		t.Fatalf("bad hello %+v", hello)
	}
	output := terminalstream.Frame{Type: "data", Data: []byte("\x1b[32mHello 世界\x1b[0m")}
	json.NewEncoder(peer).Encode(output)
	ws.SetReadDeadline(time.Now().Add(time.Second))
	var got terminalstream.Frame
	if err = ws.ReadJSON(&got); err != nil || string(got.Data) != string(output.Data) {
		t.Fatalf("output corrupted %v", err)
	}
	ws.WriteJSON(terminalstream.Frame{Type: "input", ID: "wrong-session", Data: []byte("hello\r")})
	if !scan.Scan() {
		t.Fatal(scan.Err())
	}
	json.Unmarshal(scan.Bytes(), &got)
	if got.ID != "0123456789abcdef0123456789abcdef" || string(got.Data) != "hello\r" {
		t.Fatal("identity or input mismatch")
	}
	ws.WriteJSON(terminalstream.Frame{Type: "resize", Rows: 45, Columns: 120})
	if !scan.Scan() {
		t.Fatal(scan.Err())
	}
	json.Unmarshal(scan.Bytes(), &got)
	if got.Columns != 120 || got.Rows != 45 {
		t.Fatal("resize lost")
	}
	cancel()
	if _, _, err = ws.ReadMessage(); err == nil {
		t.Fatal("account lifecycle did not revoke stream")
	}
}
func TestHostAndAssetBoundary(t *testing.T) {
	s := &Server{}
	defer s.Close()
	url, err := s.URL(context.Background(), "sunny-spicy", nil)
	if err != nil {
		t.Fatal(err)
	}
	origin := strings.Split(url, "/#")[0]
	req, _ := http.NewRequest("GET", origin, nil)
	req.Host = "attacker.example"
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("DNS rebinding allowed")
	}
	response, err = http.Get(origin + "/browser.js")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing browser assets or policy")
	}
}
