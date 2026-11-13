package cli

import (
	"bytes"
	"encoding/json"
	"sync"
	"time"

	cloudruntime "fennec/internal/runtime"
)

// ChatGPT gives every app-server request 30 s (the clock starts when it sends
// the request) and then fails it: a failed initialize fails the connection
// together with every request queued behind it, a failed permission-profile
// request blocks new conversations with "Permission mode is unavailable" until
// ChatGPT restarts, and a failed configRequirements/read shows "Timed out
// waiting for MCP response to configRequirements/read" when a conversation
// starts. When the cloud is away (resident channel reconnecting, cloud
// app-server restarting) the bridge holds ChatGPT's requests until it is back,
// which can take longer than that.
//
// So the bridge answers initialize and ChatGPT's side-effect-free startup reads
// itself from the app-server's last answer to the same request when the cloud
// has not answered within a short bound; initialize only once the startup
// reads it leads to can be answered the same way. The request still reaches the
// app-server, and its answer is dropped so ChatGPT never sees two. Requests
// with side effects, and reads with no remembered answer, keep waiting for the
// cloud.

var (
	// chatgptLocalInitializeAfter is how long the cloud may take before the
	// bridge answers initialize itself.
	chatgptLocalInitializeAfter = 3 * time.Second
	// chatgptLocalReadAfter bounds a startup read while the cloud stream is
	// not established, and chatgptLocalReadAfterConfirmed on an established
	// stream, whose app-server normally answers these in well under a second;
	// both stay below ChatGPT's 30 s.
	chatgptLocalReadAfter          = 3 * time.Second
	chatgptLocalReadAfterConfirmed = 20 * time.Second
)

// chatgptStartupReads are the reads ChatGPT issues when a window, connection
// or conversation starts. They have no side effects and their answers change
// rarely (configuration, requirements, permission profiles, models, account,
// skills), so a remembered answer is a faithful stand-in while the cloud is
// away. Thread, turn and file requests are never answered here.
var chatgptStartupReads = map[string]bool{
	"configRequirements/read":  true,
	"permissionProfile/list":   true,
	"config/read":              true,
	"account/read":             true,
	"model/list":               true,
	"collaborationMode/list":   true,
	"experimentalFeature/list": true,
	"skills/list":              true,
	"hooks/list":               true,
}

const (
	// Startup reads are small requests; larger messages are not inspected.
	chatgptReadRequestLimit = 64 << 10
	// The largest startup answer seen is model/list at about 66 KB.
	chatgptReadResultLimit = 512 << 10
	chatgptReadEntries     = 64
)

// chatgptReadKey identifies a startup read by method and parameters, or
// reports that the request is not one.
func chatgptReadKey(method string, params json.RawMessage) (string, bool) {
	if !chatgptStartupReads[method] {
		return "", false
	}
	if method == "account/read" {
		var request struct {
			RefreshToken bool `json:"refreshToken"`
		}
		json.Unmarshal(params, &request)
		if request.RefreshToken {
			return "", false // asks the cloud to refresh credentials
		}
	}
	params = bytes.TrimSpace(params)
	if len(params) == 0 {
		params = []byte("null")
	}
	var compact bytes.Buffer
	if json.Compact(&compact, params) != nil {
		return "", false
	}
	return method + " " + compact.String(), true
}

type heldRequest struct {
	id         json.RawMessage
	key        string // startup read cache key
	initialize bool
	since      time.Time
	timer      *time.Timer
}

// chatgptShield serves one ChatGPT connection.
type chatgptShield struct {
	bridge *chatgptBridge
	files  *cloudruntime.ChatGPTLocalFiles

	mu        sync.Mutex
	early     bool   // ChatGPT already has a handshake answer
	confirmed bool   // the cloud stream is established
	closed    bool   // the connection is over
	mode      string // the cloud app-server mode serving the stream
	held      map[string]*heldRequest
	answered  map[string]*heldRequest // answered here; the cloud's answer is dropped
	// pending are all of ChatGPT's requests still waiting for an answer from
	// the cloud, by id, and lastRequest when ChatGPT last sent one; a daemon
	// that stops waits for them (see drain).
	pending     map[string]bool
	lastRequest time.Time
}

func newChatGPTShield(bridge *chatgptBridge, files *cloudruntime.ChatGPTLocalFiles, early bool) *chatgptShield {
	shield := &chatgptShield{bridge: bridge, files: files, early: early, held: map[string]*heldRequest{}, answered: map[string]*heldRequest{}, pending: map[string]bool{}}
	files.Intercept(shield.observe, shield.filter)
	return shield
}

// observe sees each of ChatGPT's requests on its way to the cloud.
func (s *chatgptShield) observe(message []byte) {
	if !bytes.Contains(message, []byte(`"method"`)) {
		return
	}
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if len(message) > chatgptReadRequestLimit {
		// Large requests (attachments) are only counted, never answered here.
		var head struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(message, &head) != nil {
			return
		}
		request.ID, request.Method = head.ID, head.Method
	} else if json.Unmarshal(message, &request) != nil {
		return
	}
	if request.Method == "" || len(request.ID) == 0 || string(request.ID) == "null" {
		return // a notification, or ChatGPT's answer to the app-server
	}
	id := string(bytes.TrimSpace(request.ID))
	held := &heldRequest{id: request.ID, initialize: request.Method == "initialize", since: time.Now()}
	tracked := held.initialize
	if !held.initialize && len(message) <= chatgptReadRequestLimit {
		held.key, tracked = chatgptReadKey(request.Method, request.Params)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.pending[id] = true
	s.lastRequest = held.since
	if !tracked {
		return
	}
	if previous := s.held[id]; previous != nil && previous.timer != nil {
		previous.timer.Stop()
	}
	s.held[id] = held
	s.scheduleLocked(id, held)
}

// scheduleLocked starts the bound after which held is answered here.
func (s *chatgptShield) scheduleLocked(id string, held *heldRequest) {
	if held.timer != nil {
		held.timer.Stop()
		held.timer = nil
	}
	bound := chatgptLocalReadAfter
	switch {
	case held.initialize && (!s.early || s.confirmed):
		return // the app-server answers initialize at once once it is reachable
	case held.initialize:
		bound = chatgptLocalInitializeAfter
	case s.confirmed:
		bound = chatgptLocalReadAfterConfirmed
	}
	held.timer = time.AfterFunc(max(bound-time.Since(held.since), 0), func() { s.answerLocally(id, held) })
}

// answerLocally answers a request still waiting for the cloud from the last
// answer the app-server gave to the same request. Without one the request
// keeps waiting; nothing is made up.
func (s *chatgptShield) answerLocally(id string, held *heldRequest) {
	s.mu.Lock()
	if s.closed || s.held[id] != held {
		s.mu.Unlock()
		return
	}
	var result json.RawMessage
	if held.initialize {
		// Answering initialize starts ChatGPT's 30 s clocks for the startup
		// reads that follow. Without remembered answers to them (first use,
		// cloud preparation failed) they would time out and leave a modal or
		// a stuck "Permission mode is unavailable"; an unanswered initialize
		// only fails this connection, and ChatGPT reconnects.
		if s.bridge.startupReadsRemembered() {
			result = s.bridge.cachedInitialize()
		}
	} else {
		result = s.bridge.cachedRead(held.key)
	}
	if result == nil {
		s.mu.Unlock()
		return
	}
	if held.timer != nil {
		held.timer.Stop()
	}
	delete(s.held, id)
	delete(s.pending, id)
	s.answered[id] = held
	s.mu.Unlock()
	// The lock is released first: the answer goes out under the connection's
	// own lock, which also calls filter.
	if !s.files.Answer(held.id, result) {
		s.mu.Lock()
		delete(s.answered, id)
		s.mu.Unlock()
	}
}

// filter sees each message from the cloud. It remembers the app-server's
// answers to the tracked requests and withholds those ChatGPT already got.
func (s *chatgptShield) filter(message []byte) bool {
	s.mu.Lock()
	tracked := false
	for id := range s.held {
		if bytes.Contains(message, []byte(id)) {
			tracked = true
			break
		}
	}
	for id := range s.answered {
		if tracked {
			break
		}
		tracked = bytes.Contains(message, []byte(id))
	}
	for id := range s.pending {
		if tracked {
			break
		}
		tracked = bytes.Contains(message, []byte(id))
	}
	s.mu.Unlock()
	if !tracked {
		return false
	}
	var response struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(message, &response) != nil || response.Method != "" || len(response.ID) == 0 {
		return false
	}
	id := string(bytes.TrimSpace(response.ID))
	s.mu.Lock()
	delete(s.pending, id)
	held, drop := s.held[id], false
	if held != nil {
		delete(s.held, id)
		if held.timer != nil {
			held.timer.Stop()
		}
	} else if held = s.answered[id]; held != nil {
		delete(s.answered, id)
		drop = true
	}
	mode := s.mode
	s.mu.Unlock()
	if held != nil && len(response.Result) > 0 {
		if held.initialize {
			s.bridge.rememberInitialize(response.Result)
		} else {
			s.bridge.rememberRead(mode, held.key, response.Result)
		}
	}
	return drop
}

// handshakeAnswered records that ChatGPT got the remembered handshake after
// the connection was set up.
func (s *chatgptShield) handshakeAnswered() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.early = true
}

// confirm marks the cloud stream established by an app-server in mode;
// waiting reads now get the established stream's bound.
func (s *chatgptShield) confirm(mode string) {
	s.bridge.noteReadMode(mode)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.confirmed = true
	s.mode = mode
	if s.closed {
		return
	}
	for id, held := range s.held {
		s.scheduleLocked(id, held)
	}
}

func (s *chatgptShield) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, held := range s.held {
		if held.timer != nil {
			held.timer.Stop()
		}
	}
	clear(s.held)
	clear(s.answered)
	clear(s.pending)
}

// settled reports that every request ChatGPT sent has its answer and ChatGPT
// has sent nothing for quiet.
func (s *chatgptShield) settled(quiet time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) == 0 && time.Since(s.lastRequest) >= quiet
}

// answerRemembered answers every startup read still waiting for the cloud
// from its remembered answer at once. Initialize is left alone: answering it
// on a connection about to close would only start ChatGPT's clocks on reads
// that can no longer be answered.
func (s *chatgptShield) answerRemembered() {
	s.mu.Lock()
	waiting := make(map[string]*heldRequest, len(s.held))
	for id, held := range s.held {
		if !held.initialize {
			waiting[id] = held
		}
	}
	s.mu.Unlock()
	for id, held := range waiting {
		s.answerLocally(id, held)
	}
}

// unanswered counts ChatGPT's requests that still wait for the cloud.
func (s *chatgptShield) unanswered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
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
