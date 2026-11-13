package runtime

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fennec/internal/localssh"
	"fennec/internal/sessionid"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ChatGPT.app sends its Mac project directory as every thread's cwd. On the
// cloud computer that path does not exist, so commands fail and the agent
// believes it runs on the Mac. NewChatGPTPathFilter gives each Mac directory
// the same cloud session workspace a Fennec CLI launch gets: ~/.fen/<id> with
// the Fennec AGENTS.md naming the Mac directory and SSH alias. Requests carry
// the cloud directory; responses show the Mac directory again so ChatGPT keeps
// its project grouping.
//
// The filter understands the app-server WebSocket stream: it removes the zstd
// compression offer from the upgrade request so messages stay readable, and
// rewrites only JSON fields named "cwd" in text messages. Control and binary
// frames pass through unchanged.
func NewChatGPTPathFilter(conn net.Conn, home, device string) net.Conn {
	filter := &chatgptPathFilter{Conn: conn, home: home, device: device, paths: loadChatGPTPaths(home)}
	if served, ok := conn.(interface{ CodexHome() string }); ok {
		filter.codexHome = served.CodexHome()
	}
	if filter.StreamLabel() == CodexAppServerGateway && gatewayOverrides(filter.codexHome) != nil {
		filter.provider = "magpie"
	}
	filter.statusPending = map[string]statusWait{}
	filter.outCond = sync.NewCond(&filter.outMu)
	filter.upgraded = make(chan struct{})
	filter.inbound.server = false
	filter.outbound.server = true
	return filter
}

const (
	chatgptCompressionHeader = "x-codex-app-server-compression"
	chatgptMessageLimit      = 128 << 20
)

type chatgptPathFilter struct {
	net.Conn
	home, device string

	pathsMu sync.Mutex
	paths   map[string]string // cloud directory -> Mac directory

	codexHome string
	provider  string // model provider forced for this app-server, if any

	statusMu      sync.Mutex
	statusPending map[string]statusWait // JSON-RPC id -> MCP status request

	writeMu sync.Mutex
	inbound wsStream

	// One pump goroutine reads server bytes for ChatGPT, so the filter can
	// also answer requests itself, between whole frames.
	pumpOnce    sync.Once
	upgraded    chan struct{} // closed once the app-server answered the handshake or the stream ended
	upgradeOnce sync.Once
	outbound    wsStream
	outMu       sync.Mutex
	outCond     *sync.Cond
	pending     []byte
	outErr      error
	closed      bool
}

// chatgptOutputBuffer bounds server output held for ChatGPT, so the credit
// window still paces the app-server.
const chatgptOutputBuffer = 1 << 20

func (filter *chatgptPathFilter) StreamLabel() string {
	if labeled, ok := filter.Conn.(interface{ StreamLabel() string }); ok {
		return labeled.StreamLabel()
	}
	return ""
}

// Write receives client bytes (ChatGPT -> app-server).
func (filter *chatgptPathFilter) Write(data []byte) (int, error) {
	filter.writeMu.Lock()
	defer filter.writeMu.Unlock()
	output, err := filter.inbound.feed(data, filter.toCloud)
	if err != nil {
		return 0, err
	}
	// codex app-server drops a connection whose upgrade request arrives with
	// frames behind it. The Mac may already have completed the handshake for
	// ChatGPT, so frames wait here until the app-server has answered.
	if head := filter.inbound.headOut; head > 0 {
		if _, err := filter.Conn.Write(output[:head]); err != nil {
			return 0, err
		}
		output = output[head:]
		filter.pumpOnce.Do(func() { go filter.pump() })
		<-filter.upgraded
	}
	if len(output) > 0 {
		if _, err := filter.Conn.Write(output); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

// Read returns server bytes (app-server -> ChatGPT).
func (filter *chatgptPathFilter) Read(data []byte) (int, error) {
	filter.pumpOnce.Do(func() { go filter.pump() })
	filter.outMu.Lock()
	defer filter.outMu.Unlock()
	for len(filter.pending) == 0 && filter.outErr == nil && !filter.closed {
		filter.outCond.Wait()
	}
	if len(filter.pending) == 0 {
		if filter.outErr != nil {
			return 0, filter.outErr
		}
		return 0, net.ErrClosed
	}
	count := copy(data, filter.pending)
	filter.pending = filter.pending[count:]
	filter.outCond.Broadcast()
	return count, nil
}

func (filter *chatgptPathFilter) pump() {
	defer filter.releaseInput()
	buffer := make([]byte, 32<<10)
	for {
		length, err := filter.Conn.Read(buffer)
		if length > 0 {
			output, feedErr := filter.outbound.feed(buffer[:length], filter.toMac)
			if filter.outbound.headDone {
				filter.releaseInput()
			}
			if feedErr != nil {
				err = feedErr
			} else if len(output) > 0 && !filter.deliver(output, true) {
				return
			}
		}
		if err != nil {
			filter.outMu.Lock()
			filter.outErr = err
			filter.outCond.Broadcast()
			filter.outMu.Unlock()
			return
		}
	}
}

// deliver queues whole frames for ChatGPT. Server output waits for room; the
// filter's own answers never wait.
func (filter *chatgptPathFilter) deliver(frames []byte, wait bool) bool {
	filter.outMu.Lock()
	defer filter.outMu.Unlock()
	for wait && len(filter.pending) >= chatgptOutputBuffer && !filter.closed {
		filter.outCond.Wait()
	}
	if filter.closed {
		return false
	}
	filter.pending = append(filter.pending, frames...)
	filter.outCond.Broadcast()
	return true
}

// releaseInput lets client frames follow the handshake to the app-server.
func (filter *chatgptPathFilter) releaseInput() {
	filter.upgradeOnce.Do(func() { close(filter.upgraded) })
}

func (filter *chatgptPathFilter) Close() error {
	filter.releaseInput()
	filter.forgetStatusRefreshes()
	filter.outMu.Lock()
	filter.closed = true
	filter.outCond.Broadcast()
	filter.outMu.Unlock()
	return filter.Conn.Close()
}

func (filter *chatgptPathFilter) toCloud(message []byte) []byte {
	message, answer := filter.statusRequest(message)
	if answer != nil {
		filter.deliver(encodeWebSocketFrame(0x1, answer, false), false)
	}
	if message == nil {
		return nil
	}
	filter.migrateThreads(message)
	return rewriteMessage(message, filter.desktopServer, filter.cloudPath)
}

// toCloudPaths maps cwd fields only, for stored rollouts.
func (filter *chatgptPathFilter) toCloudPaths(message []byte) []byte {
	return rewriteCwd(message, filter.cloudPath)
}

func (filter *chatgptPathFilter) cloudPath(path string) (string, bool) {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, filepath.Join(filter.home, ".fen")+"/") {
		return "", false
	}
	cloud, err := filter.workspace(path)
	return cloud, err == nil
}

func (filter *chatgptPathFilter) toMac(message []byte) []byte {
	if !filter.statusResponse(message) {
		return nil
	}
	filter.noteCloudThreads(message)
	return rewriteCwd(message, func(path string) (string, bool) {
		filter.pathsMu.Lock()
		defer filter.pathsMu.Unlock()
		mac, ok := filter.paths[path]
		if !ok && strings.HasPrefix(path, filepath.Join(filter.home, ".fen")+"/") {
			// Another connection may have created this workspace since.
			filter.paths = loadChatGPTPaths(filter.home)
			mac, ok = filter.paths[path]
		}
		return mac, ok
	})
}

// chatgptWorkspaceMu serializes name allocation across ChatGPT connections,
// which each hold their own copy of the path map.
var chatgptWorkspaceMu sync.Mutex

// workspaceID returns the cloud session name for a Mac directory: a word pair
// like every other Fennec session (~/.fen/desert-true), allocated once and
// remembered, so reopening a thread reuses its directory.
func (filter *chatgptPathFilter) workspaceID(mac string) (string, error) {
	chatgptWorkspaceMu.Lock()
	defer chatgptWorkspaceMu.Unlock()
	paths := loadChatGPTPaths(filter.home)
	for cloud, known := range paths {
		if known == mac && filepath.Dir(cloud) == filepath.Join(filter.home, ".fen") && sessionid.Memorable(filepath.Base(cloud)) {
			return filepath.Base(cloud), nil
		}
	}
	root := filepath.Join(filter.home, ".fen")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 64; attempt++ {
		id, err := sessionid.Random()
		if err != nil {
			return "", err
		}
		// Exclusive creation never adopts another session's directory.
		if err := os.Mkdir(filepath.Join(root, id), 0700); os.IsExist(err) {
			continue
		} else if err != nil {
			return "", err
		}
		paths[filepath.Join(root, id)] = mac
		saveChatGPTPaths(filter.home, paths)
		return id, nil
	}
	return "", fmt.Errorf("cloud session name allocation exhausted")
}

// workspace prepares the cloud session directory for a Mac directory.
func (filter *chatgptPathFilter) workspace(mac string) (string, error) {
	id, err := filter.workspaceID(mac)
	if err != nil {
		return "", err
	}
	request := LocalLaunch{ID: id, Agent: "codex", LocalDirectory: mac, Device: filter.device}
	cloud := filepath.Join(filter.home, ".fen", request.ID)
	if _, err := prepareSessionWorkspace(filter.home, request); err != nil {
		// An earlier prompt (for example another device alias) is preserved.
		if info, statErr := os.Lstat(cloud); statErr != nil || !info.IsDir() {
			return "", err
		}
	}
	filter.pathsMu.Lock()
	defer filter.pathsMu.Unlock()
	filter.paths[cloud] = mac
	return cloud, nil
}

func chatgptPathsFile(home string) string {
	return filepath.Join(home, ".local/fennec/chatgpt/paths.json")
}

func loadChatGPTPaths(home string) map[string]string {
	paths := map[string]string{}
	if data, err := os.ReadFile(chatgptPathsFile(home)); err == nil {
		json.Unmarshal(data, &paths)
	}
	return paths
}

func saveChatGPTPaths(home string, paths map[string]string) {
	path := chatgptPathsFile(home)
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	data, err := json.Marshal(paths)
	if err != nil {
		return
	}
	temporary := path + ".tmp"
	if os.WriteFile(temporary, data, 0600) == nil {
		os.Rename(temporary, path)
	}
}

// rewriteCwd maps every string field named "cwd" in a JSON message. Messages
// without such a field, or that are not JSON, are returned unchanged.
func rewriteCwd(message []byte, mapping func(string) (string, bool)) []byte {
	return rewriteMessage(message, nil, mapping)
}

// rewriteMessage maps "cwd" fields and, for requests to the cloud, disables MCP
// servers whose commands are ChatGPT.app executables on the Mac. ChatGPT adds
// them through per-thread config and config writes; on the cloud computer they
// cannot start and their startup timeouts stall every other request.
func rewriteMessage(message []byte, replace desktopReplacer, mapping func(string) (string, bool)) []byte {
	desktop := replace != nil && (bytes.Contains(message, []byte("/Applications/ChatGPT.app/")) || bytes.Contains(message, []byte("/Applications/Codex.app/")))
	if !desktop && !bytes.Contains(message, []byte(`"cwd"`)) {
		return message
	}
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return message
	}
	changed := false
	var walk func(any, string)
	walk = func(node any, name string) {
		switch typed := node.(type) {
		case map[string]any:
			if command, ok := typed["command"].(string); ok && desktop && desktopOnlyCommand(command) {
				replacement := replace(strings.TrimPrefix(name, "mcp_servers."), typed)
				for key := range typed {
					delete(typed, key)
				}
				for key, value := range replacement {
					typed[key] = value
				}
				changed = true
				return
			}
			keyPath, _ := typed["keyPath"].(string)
			for key, child := range typed {
				if text, ok := child.(string); ok && key == "cwd" {
					if mapped, ok := mapURIOrPath(text, mapping); ok && mapped != text {
						typed[key] = mapped
						changed = true
					}
					continue
				}
				childName := key
				if key == "value" && keyPath != "" {
					childName = keyPath
				}
				walk(child, childName)
			}
		case []any:
			for _, child := range typed {
				walk(child, name)
			}
		}
	}
	walk(value, "")
	if !changed {
		return message
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return message
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n"))
}

// wsStream incrementally parses one direction of an HTTP-upgraded WebSocket
// connection and re-emits it with text messages transformed.
type wsStream struct {
	server     bool // frames from the server are unmasked; client frames are masked
	headDone   bool
	headOut    int // length of the handshake at the start of the last output, if any
	buffer     []byte
	message    []byte
	messageOp  byte
	collecting bool
	checked    bool // the first frame looked like WebSocket from this side
	raw        bool // not a WebSocket stream: bytes pass through untouched
}

// plausibleFirstFrame reports whether a stream starts like WebSocket frames
// from this side: no reserved bits, not a continuation, and masked exactly
// when sent by the client.
func (stream *wsStream) plausibleFirstFrame() bool {
	first, second := stream.buffer[0], stream.buffer[1]
	masked := second&0x80 != 0
	return first&0x70 == 0 && first&0x0f != 0 && masked != stream.server
}

func (stream *wsStream) feed(data []byte, transform func([]byte) []byte) ([]byte, error) {
	stream.buffer = append(stream.buffer, data...)
	var output []byte
	stream.headOut = 0
	if !stream.headDone {
		end := bytes.Index(stream.buffer, []byte("\r\n\r\n"))
		if end < 0 {
			if len(stream.buffer) > 64<<10 {
				return nil, fmt.Errorf("oversized app-server handshake")
			}
			return nil, nil
		}
		head := stream.buffer[:end+4]
		if !stream.server {
			head = dropHeader(head, chatgptCompressionHeader)
		}
		output = append(output, head...)
		stream.headOut = len(head)
		stream.buffer = stream.buffer[end+4:]
		stream.headDone = true
	}
	if !stream.raw && !stream.checked && len(stream.buffer) >= 2 {
		stream.checked = true
		stream.raw = !stream.plausibleFirstFrame()
	}
	if stream.raw {
		output = append(output, stream.buffer...)
		stream.buffer = nil
		return output, nil
	}
	for {
		frame, size, err := parseWebSocketFrame(stream.buffer)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			break
		}
		stream.buffer = stream.buffer[size:]
		switch {
		case frame.opcode >= 0x8: // control frames may interleave fragments
			output = append(output, frame.raw...)
		case frame.opcode == 0x1 || (frame.opcode == 0x0 && stream.collecting && stream.messageOp == 0x1):
			if frame.opcode == 0x1 {
				stream.message, stream.messageOp, stream.collecting = nil, 0x1, true
			}
			stream.message = append(stream.message, frame.payload...)
			if len(stream.message) > chatgptMessageLimit {
				return nil, fmt.Errorf("app-server message too large")
			}
			if frame.fin {
				// A nil transform result drops the message.
				if transformed := transform(stream.message); transformed != nil {
					output = append(output, encodeWebSocketFrame(0x1, transformed, !stream.server)...)
				}
				stream.message, stream.collecting = nil, false
			}
		default: // binary messages and their continuations pass through
			if frame.opcode != 0x0 {
				stream.messageOp, stream.collecting = frame.opcode, !frame.fin
			} else if frame.fin {
				stream.collecting = false
			}
			output = append(output, frame.raw...)
		}
	}
	return output, nil
}

func dropHeader(head []byte, name string) []byte {
	lines := bytes.Split(head, []byte("\r\n"))
	kept := lines[:0]
	for _, line := range lines {
		if key, _, ok := bytes.Cut(line, []byte(":")); ok && strings.EqualFold(strings.TrimSpace(string(key)), name) {
			continue
		}
		kept = append(kept, line)
	}
	return bytes.Join(kept, []byte("\r\n"))
}

type webSocketFrame struct {
	fin     bool
	opcode  byte
	payload []byte // unmasked
	raw     []byte
}

// parseWebSocketFrame returns size 0 when the buffer holds no complete frame.
func parseWebSocketFrame(buffer []byte) (webSocketFrame, int, error) {
	if len(buffer) < 2 {
		return webSocketFrame{}, 0, nil
	}
	header := 2
	length := uint64(buffer[1] & 0x7f)
	switch length {
	case 126:
		if len(buffer) < 4 {
			return webSocketFrame{}, 0, nil
		}
		length, header = uint64(binary.BigEndian.Uint16(buffer[2:4])), 4
	case 127:
		if len(buffer) < 10 {
			return webSocketFrame{}, 0, nil
		}
		length, header = binary.BigEndian.Uint64(buffer[2:10]), 10
	}
	if length > chatgptMessageLimit {
		return webSocketFrame{}, 0, fmt.Errorf("app-server frame too large")
	}
	masked := buffer[1]&0x80 != 0
	var mask []byte
	if masked {
		if len(buffer) < header+4 {
			return webSocketFrame{}, 0, nil
		}
		mask = buffer[header : header+4]
		header += 4
	}
	size := header + int(length)
	if len(buffer) < size {
		return webSocketFrame{}, 0, nil
	}
	payload := append([]byte(nil), buffer[header:size]...)
	for i := range payload {
		if masked {
			payload[i] ^= mask[i%4]
		}
	}
	return webSocketFrame{fin: buffer[0]&0x80 != 0, opcode: buffer[0] & 0x0f, payload: payload, raw: append([]byte(nil), buffer[:size]...)}, size, nil
}

func encodeWebSocketFrame(opcode byte, payload []byte, masked bool) []byte {
	frame := []byte{0x80 | opcode}
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch length := len(payload); {
	case length < 126:
		frame = append(frame, maskBit|byte(length))
	case length < 1<<16:
		frame = append(frame, maskBit|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(length))
	default:
		frame = append(frame, maskBit|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(length))
	}
	if !masked {
		return append(frame, payload...)
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	frame = append(frame, mask...)
	start := len(frame)
	frame = append(frame, payload...)
	for i := range payload {
		frame[start+i] ^= mask[i%4]
	}
	return frame
}

// mapURIOrPath maps a plain absolute path or its file:// URI form, keeping the
// form the message used.
func mapURIOrPath(value string, mapping func(string) (string, bool)) (string, bool) {
	if path, ok := strings.CutPrefix(value, "file://"); ok {
		mapped, ok := mapping(path)
		if !ok {
			return "", false
		}
		return "file://" + mapped, true
	}
	return mapping(value)
}

// desktopReplacer returns the cloud definition for one of ChatGPT.app's MCP
// servers that runs a Mac executable.
type desktopReplacer func(name string, server map[string]any) map[string]any

// disabledDesktopServer is ChatGPT's own placeholder for a disabled server.
func disabledDesktopServer(string, map[string]any) map[string]any {
	return map[string]any{"command": "", "enabled": false}
}

// desktopServer runs ChatGPT's desktop MCP servers (node_repl, cua_repl) on
// the user's Mac through its Fennec SSH alias: they drive that Mac, and their
// executables exist only there. The original command, arguments and
// environment travel unchanged; the Mac connector accepts only ChatGPT.app
// executables and asks for approval. Without an alias the server is disabled
// so its startup cannot stall other requests.
func (filter *chatgptPathFilter) desktopServer(name string, server map[string]any) map[string]any {
	alias := localssh.RegisteredAlias(filter.home, filter.device)
	if filter.device == "" || alias == "" || name == "" {
		return disabledDesktopServer(name, server)
	}
	spec := localssh.DesktopMCPSpec{Name: name}
	spec.Command, _ = server["command"].(string)
	spec.Cwd, _ = server["cwd"].(string)
	if args, ok := server["args"].([]any); ok {
		for _, arg := range args {
			if text, ok := arg.(string); ok {
				spec.Args = append(spec.Args, text)
			}
		}
	}
	if env, ok := server["env"].(map[string]any); ok {
		spec.Env = map[string]string{}
		for key, value := range env {
			if text, ok := value.(string); ok {
				spec.Env[key] = text
			}
		}
	}
	remote, err := localssh.EncodeDesktopMCP(spec)
	if err != nil {
		return disabledDesktopServer(name, server)
	}
	replacement := map[string]any{}
	for key, value := range server {
		switch key {
		case "command", "args", "env", "cwd", "env_vars":
		default:
			replacement[key] = value
		}
	}
	replacement["command"] = "ssh"
	replacement["args"] = []any{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", alias, remote}
	return replacement
}
