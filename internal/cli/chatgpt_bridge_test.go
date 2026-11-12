package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/terminalstream"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const bridgeTestToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestReadUpgradeRewritesAuthorizedPath(t *testing.T) {
	request := "GET /fennec/" + bridgeTestToken + "?x=1 HTTP/1.1\r\nHost: 127.0.0.1:47631\r\nUpgrade: websocket\r\n\r\nrest"
	head, err := readUpgrade(bufio.NewReader(strings.NewReader(request)), bridgeTestToken)
	if err != nil {
		t.Fatal(err)
	}
	if string(head) != "GET /rpc HTTP/1.1\r\nHost: 127.0.0.1:47631\r\nUpgrade: websocket\r\n\r\n" {
		t.Fatalf("%q", head)
	}
}

func TestReadUpgradeRejectsWrongToken(t *testing.T) {
	for _, line := range []string{
		"GET /fennec/" + strings.Repeat("f", 64) + " HTTP/1.1\r\n\r\n",
		"GET /rpc HTTP/1.1\r\n\r\n",
		"POST /fennec/" + bridgeTestToken + " HTTP/1.1\r\n\r\n",
	} {
		if _, err := readUpgrade(bufio.NewReader(strings.NewReader(line)), bridgeTestToken); !errors.Is(err, errForbidden) {
			t.Fatal(line, err)
		}
	}
}

func TestChatGPTBridgeTogglesLaunchdEnvironment(t *testing.T) {
	home := t.TempDir()
	var calls []string
	bridge := newChatGPTBridge(home, &daemonServices{})
	bridge.launchctl = func(_ context.Context, arguments ...string) error {
		calls = append(calls, strings.Join(arguments, " "))
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bridge.run(ctx)
	set := bridge.handlers()["chatgpt.set"]
	var result any
	var err error
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if result, err = set(ctx, json.RawMessage(`{"enabled":true}`)); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	state := result.(map[string]any)
	url, _ := state["url"].(string)
	if state["listening"] != true || !strings.HasPrefix(url, "ws://127.0.0.1:") || len(calls) != 1 || calls[0] != "setenv "+chatgptEnvironment+" "+url {
		t.Fatal(state, calls)
	}
	info, err := os.Stat(filepath.Join(home, ".config/fennec/chatgpt.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if _, err = set(ctx, json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1] != "unsetenv "+chatgptEnvironment {
		t.Fatal(calls)
	}
}

func TestChatGPTBridgeRelaysWebSocketThroughResidentChannel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverInput, clientInput := io.Pipe()
	serverOutput, clientOutput := io.Pipe()
	defer clientInput.Close()
	defer serverOutput.Close()
	go terminalstream.ServeWithSockets(ctx, serverInput, clientOutput, nil, nil, nil, nil, func(ctx context.Context, target string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			reader := bufio.NewReader(remote)
			line, _ := reader.ReadString('\n')
			if line != "GET /rpc HTTP/1.1\r\n" {
				return
			}
			for {
				header, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if header == "\r\n" {
					break
				}
			}
			io.WriteString(remote, "HTTP/1.1 101 Switching Protocols\r\n\r\n")
			io.Copy(remote, reader)
		}()
		return local, nil
	})
	transport, err := startTestTransport(ctx, clientInput, serverOutput)
	if err != nil {
		t.Fatal(err)
	}
	services := &daemonServices{channel: &terminalChannel{ctx: ctx, transport: transport}}
	bridge := newChatGPTBridge(t.TempDir(), services)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	bridge.pref.Token = bridgeTestToken
	go bridge.accept(ctx, listener)
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /fennec/"+bridgeTestToken+" HTTP/1.1\r\nHost: x\r\n\r\nping")
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if status, err := reader.ReadString('\n'); err != nil || status != "HTTP/1.1 101 Switching Protocols\r\n" {
		t.Fatal(status, err)
	}
	reader.ReadString('\n')
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil || string(echo) != "ping" {
		t.Fatal(string(echo), err)
	}
}

func startTestTransport(ctx context.Context, input io.WriteCloser, output io.ReadCloser) (*terminalstream.Transport, error) {
	transport := terminalstream.StartPipes(input, output)
	return transport, transport.Ready(ctx)
}
