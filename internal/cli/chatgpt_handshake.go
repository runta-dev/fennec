package cli

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

// ChatGPT sends its first request only after the WebSocket handshake
// completes, and over the cloud link that handshake costs a full round trip.
// The bridge remembers the app-server's last 101 response and answers a new
// connection with it at once (with this request's Sec-WebSocket-Accept), so
// ChatGPT's initialize travels right behind the upgrade. The app-server's own
// response is removed from the stream when it arrives; if the app-server
// refuses, the connection closes and ChatGPT reconnects as after any drop.
// Only cloud services that hold ChatGPT's frames until the app-server has
// answered (codex_app_server_early_input) get this: codex app-server drops a
// connection whose request arrives with frames behind it.

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// earlyUpgrade returns the response to give ChatGPT before the app-server
// answers, or nil when none is known or the request asks for negotiation the
// stored response cannot vouch for.
func (b *chatgptBridge) earlyUpgrade(request []byte) []byte {
	key := ""
	for _, line := range strings.Split(string(request), "\r\n")[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "sec-websocket-key":
			key = strings.TrimSpace(value)
		case "sec-websocket-protocol", "sec-websocket-extensions":
			return nil
		}
	}
	b.mu.Lock()
	stored := b.upgrade
	b.mu.Unlock()
	if key == "" || stored == nil {
		return nil
	}
	digest := sha1.Sum([]byte(key + websocketGUID))
	return append(append([]byte(nil), stored...), []byte("sec-websocket-accept: "+base64.StdEncoding.EncodeToString(digest[:])+"\r\n\r\n")...)
}

// rememberUpgrade keeps a 101 response without its per-request accept value
// and terminating blank line.
func (b *chatgptBridge) rememberUpgrade(response []byte) {
	lines := strings.Split(strings.TrimSuffix(string(response), "\r\n\r\n"), "\r\n")
	var kept strings.Builder
	for _, line := range lines {
		name, _, _ := strings.Cut(line, ":")
		if strings.EqualFold(strings.TrimSpace(name), "sec-websocket-accept") {
			continue
		}
		kept.WriteString(line + "\r\n")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if string(b.upgrade) == kept.String() {
		return
	}
	b.upgrade = []byte(kept.String())
	b.pref.Upgrade = kept.String()
	b.saveLocked()
}

// knownEarlyInput reports whether the cloud service last seen holds early
// frames, so a handshake may be answered before the cloud is reachable.
func (b *chatgptBridge) knownEarlyInput() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pref.EarlyInput
}

func (b *chatgptBridge) rememberEarlyInput(supported bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pref.EarlyInput == supported {
		return
	}
	b.pref.EarlyInput = supported
	b.saveLocked()
}

// saveLocked persists the preference; a bridge without a path (tests) keeps
// it in memory only.
func (b *chatgptBridge) saveLocked() {
	if b.path != "" {
		writePrivateJSON(b.path, b.pref)
	}
}

// upgradeReader passes the app-server's output to ChatGPT, removing the
// handshake response when ChatGPT already received the early one.
type upgradeReader struct {
	bridge *chatgptBridge
	dst    io.Writer
	early  bool
	head   []byte
	done   bool
}

func (reader *upgradeReader) Write(data []byte) (int, error) {
	if reader.done {
		return reader.dst.Write(data)
	}
	reader.head = append(reader.head, data...)
	end := bytes.Index(reader.head, []byte("\r\n\r\n"))
	if end < 0 {
		if len(reader.head) > chatgptHeadLimit {
			return 0, fmt.Errorf("oversized app-server handshake")
		}
		return len(data), nil
	}
	response, rest := reader.head[:end+4], reader.head[end+4:]
	reader.done = true
	status, _, _ := bytes.Cut(response, []byte("\r\n"))
	upgraded := len(bytes.Fields(status)) >= 2 && string(bytes.Fields(status)[1]) == "101"
	switch {
	case upgraded:
		reader.bridge.rememberUpgrade(response)
		if !reader.early {
			rest = reader.head
		}
	case reader.early:
		return 0, fmt.Errorf("app-server refused the WebSocket upgrade")
	default:
		rest = reader.head
	}
	reader.head = nil
	if len(rest) > 0 {
		if _, err := reader.dst.Write(rest); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}
