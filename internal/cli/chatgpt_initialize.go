package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"

	cloudruntime "fennec/internal/runtime"
)

// ChatGPT gives the app-server 30 s to answer initialize and then fails the
// connection together with every request queued behind it; a failed
// permission-profile request blocks new conversations with "Permission mode
// is unavailable" until ChatGPT restarts. When the cloud is away longer than a
// moment, the bridge answers initialize itself from the app-server's last
// answer and keeps ChatGPT's requests until the cloud is back. ChatGPT's own
// initialize still reaches the app-server first, and that answer is dropped
// so ChatGPT never sees two.

// chatgptLocalInitializeAfter is how long the cloud may take before the
// bridge answers initialize itself.
var chatgptLocalInitializeAfter = 3 * time.Second

type initializeShield struct {
	bridge *chatgptBridge
	conn   io.Writer

	mu        sync.Mutex
	early     bool // ChatGPT already has a handshake answer
	confirmed bool // the cloud stream is established
	requestID json.RawMessage
	answered  bool // initialize was answered here
	timer     *time.Timer

	// Client frames are inspected only until initialize is found.
	scanning bool
	pending  []byte
}

func newInitializeShield(bridge *chatgptBridge, conn io.Writer, early bool) *initializeShield {
	return &initializeShield{bridge: bridge, conn: conn, early: early, scanning: true}
}

// observeInput inspects ChatGPT's frames (after the upgrade request) for its
// initialize request.
func (s *initializeShield) observeInput(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.scanning {
		return
	}
	s.pending = append(s.pending, data...)
	for s.scanning {
		opcode, fin, payload, size, err := cloudruntime.ParseWebSocketFrame(s.pending)
		if err != nil || (size > 0 && (!fin || opcode != 0x1)) {
			s.scanning = false // initialize is ChatGPT's first, unfragmented text frame
			break
		}
		if size == 0 {
			if len(s.pending) > chatgptHeadLimit {
				s.scanning = false
			}
			break
		}
		s.pending = s.pending[size:]
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		s.scanning = false
		if json.Unmarshal(payload, &request) == nil && request.Method == "initialize" && len(request.ID) > 0 {
			s.requestID = request.ID
			if s.early && !s.confirmed {
				s.timer = time.AfterFunc(chatgptLocalInitializeAfter, s.answerLocally)
			}
		}
	}
	if !s.scanning {
		s.pending = nil
	}
}

func (s *initializeShield) answerLocally() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cached := s.bridge.cachedInitialize()
	if s.confirmed || s.answered || len(s.requestID) == 0 || cached == nil {
		return
	}
	response, err := json.Marshal(map[string]any{"id": s.requestID, "result": cached})
	if err != nil {
		return
	}
	if _, err := s.conn.Write(cloudruntime.EncodeWebSocketFrame(0x1, response, false)); err == nil {
		s.answered = true
	}
}

// confirm marks the cloud stream established; no local answer follows.
func (s *initializeShield) confirm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.confirmed = true
	if s.timer != nil {
		s.timer.Stop()
	}
}

func (s *initializeShield) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.confirmed = true
}

// output returns the writer for the app-server's frames to ChatGPT. It
// remembers the app-server's initialize answer and drops it when ChatGPT was
// already answered here.
func (s *initializeShield) output() io.Writer { return &initializeOutput{shield: s} }

type initializeOutput struct {
	shield  *initializeShield
	pending []byte
	done    bool
}

func (o *initializeOutput) Write(data []byte) (int, error) {
	s := o.shield
	if o.done {
		return s.write(data)
	}
	o.pending = append(o.pending, data...)
	// Server frames are unmasked with no reserved bits; anything else is not a
	// WebSocket stream and passes through untouched.
	if len(o.pending) >= 2 && (o.pending[0]&0x70 != 0 || o.pending[0]&0x0f == 0 || o.pending[1]&0x80 != 0) {
		o.done = true
	}
	for !o.done {
		opcode, fin, payload, size, err := cloudruntime.ParseWebSocketFrame(o.pending)
		if err != nil {
			o.done = true
			break
		}
		if size == 0 {
			if len(o.pending) > chatgptHeadLimit*64 {
				o.done = true // not a frame stream we understand; pass it through
			}
			break
		}
		frame := o.pending[:size]
		drop := false
		if fin && opcode == 0x1 {
			var response struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Method string          `json:"method"`
			}
			s.mu.Lock()
			id, answered := s.requestID, s.answered
			s.mu.Unlock()
			if len(id) > 0 && json.Unmarshal(payload, &response) == nil && response.Method == "" && bytes.Equal(bytes.TrimSpace(response.ID), bytes.TrimSpace(id)) {
				o.done = true
				if len(response.Result) > 0 {
					s.bridge.rememberInitialize(response.Result)
				}
				drop = answered
			}
		}
		if !drop {
			if _, err := s.write(frame); err != nil {
				return 0, err
			}
		}
		o.pending = o.pending[size:]
	}
	if o.done && len(o.pending) > 0 {
		rest := o.pending
		o.pending = nil
		if _, err := s.write(rest); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

func (s *initializeShield) write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.Write(data)
}

func (b *chatgptBridge) cachedInitialize() json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pref.Initialize == "" {
		return nil
	}
	return json.RawMessage(b.pref.Initialize)
}

func (b *chatgptBridge) rememberInitialize(result json.RawMessage) {
	if !json.Valid(result) || len(result) > 16<<10 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pref.Initialize == string(result) {
		return
	}
	b.pref.Initialize = string(result)
	b.saveLocked()
}
