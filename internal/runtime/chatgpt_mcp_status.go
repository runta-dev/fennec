package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ChatGPT asks for MCP server status (mcpServerStatus/list without a thread)
// whenever a window, the settings page or the composer's tool list opens. The
// app-server answers by starting every configured server and listing its
// tools; ChatGPT's desktop tools start on the Mac over SSH, which takes seconds
// per call. The filter answers a repeated question from the last result at
// once and lets the app-server's fresh answer replace the stored one for the
// next call (stale-while-revalidate). A stored result is used only while
// config.toml is unchanged; ChatGPT rewrites the same MCP entries whenever it
// connects, which must not cost the next status request its stored answer.
// Plugin and login changes clear the store.
//
// Requests naming a thread reuse that thread's MCP connections and are already
// fast; they pass through untouched.

const mcpStatusMethod = "mcpServerStatus/list"

// mcpStatusRefreshID marks requests the filter sent to refresh its store; their
// responses never reach ChatGPT.
const mcpStatusRefreshID = "fennec-mcp-status:"

// Methods after which a stored status may be wrong although config.toml is
// unchanged.
var mcpStatusInvalidating = map[string]bool{
	"mcpServer/oauth/login": true, "mcpServer/refresh": true, "plugin/install": true,
	"plugin/uninstall": true, "plugin/reconcile": true, "account/login/start": true,
	"account/logout": true,
}

// mcpStatusEntry is one stored answer and the config.toml it was given for.
type mcpStatusEntry struct {
	Result json.RawMessage `json:"result"`
	Config string          `json:"config"`
	// At is when the app-server last confirmed this answer (Unix ms).
	At int64 `json:"at,omitempty"`
}

// mcpStatusFresh is how long a confirmed answer is served without asking the
// app-server again. Every app-server answer starts all MCP servers, and
// ChatGPT's desktop tools start on the Mac over SSH only to be stopped once
// their tools are listed; refreshing on every status request started node_repl
// on the Mac dozens of times a minute. Config, plugin and login changes still
// replace the answer at once.
const mcpStatusFresh = 10 * time.Minute

var mcpStatusNow = time.Now

// statusWait is a status request awaiting the app-server's answer.
type statusWait struct {
	key, config string
}

// mcpConfigVersion fingerprints the configuration the app-server reads MCP
// servers from.
func mcpConfigVersion(codexHome string) string {
	data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type mcpStatusStore struct {
	mu         sync.Mutex
	loaded     bool
	results    map[string]mcpStatusEntry // CODEX_HOME + params -> answer
	refreshing map[string]bool
}

var mcpStatuses = &mcpStatusStore{}

func mcpStatusFile(home string) string {
	return filepath.Join(home, ".local/fennec/chatgpt/mcp-status.json")
}

// loadLocked reads stored results once per process, so a restarted cloud
// service answers ChatGPT's first status request at once too.
func (store *mcpStatusStore) loadLocked(home string) {
	if store.loaded {
		return
	}
	store.loaded = true
	store.results = map[string]mcpStatusEntry{}
	store.refreshing = map[string]bool{}
	if data, err := os.ReadFile(mcpStatusFile(home)); err == nil {
		json.Unmarshal(data, &store.results)
	}
}

func (store *mcpStatusStore) saveLocked(home string) {
	data, err := json.Marshal(store.results)
	if err == nil && os.MkdirAll(filepath.Dir(mcpStatusFile(home)), 0700) == nil {
		writeFileAtomic(mcpStatusFile(home), data)
	}
}

func (store *mcpStatusStore) clear(home, codexHome string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loadLocked(home)
	changed := false
	for key := range store.results {
		if strings.HasPrefix(key, codexHome+"\x00") {
			delete(store.results, key)
			changed = true
		}
	}
	if changed {
		store.saveLocked(home)
	}
}

type jsonRPCRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// mcpStatusKey identifies a thread-less status request, independent of key
// order; ok is false for requests the store does not answer.
func mcpStatusKey(codexHome string, params json.RawMessage) (string, bool) {
	var fields map[string]any
	if len(params) > 0 && json.Unmarshal(params, &fields) != nil {
		return "", false
	}
	if thread, _ := fields["threadId"].(string); thread != "" {
		return "", false
	}
	if cursor, _ := fields["cursor"].(string); cursor != "" {
		return "", false // later pages are rare and cheap to fetch
	}
	delete(fields, "threadId")
	canonical, _ := json.Marshal(fields) // map keys marshal sorted
	return codexHome + "\x00" + string(canonical), true
}

// mentionsStatusMethod is a cheap check that spares decoding large messages,
// such as turns with attachments, that cannot concern MCP status.
func mentionsStatusMethod(message []byte) bool {
	if bytes.Contains(message, []byte(`"`+mcpStatusMethod+`"`)) {
		return true
	}
	for method := range mcpStatusInvalidating {
		if bytes.Contains(message, []byte(`"`+method+`"`)) {
			return true
		}
	}
	return false
}

// statusRequest handles a request on its way to the app-server. It returns the
// message to forward (nil to send nothing) and a response to give ChatGPT now.
func (filter *chatgptPathFilter) statusRequest(message []byte) (forward, answer []byte) {
	if filter.codexHome == "" {
		return message, nil
	}
	if !mentionsStatusMethod(message) {
		return message, nil
	}
	var request jsonRPCRequest
	if json.Unmarshal(message, &request) != nil {
		return message, nil
	}
	if mcpStatusInvalidating[request.Method] {
		mcpStatuses.clear(filter.home, filter.codexHome)
		return message, nil
	}
	if request.Method != mcpStatusMethod || len(request.ID) == 0 {
		return message, nil
	}
	key, ok := mcpStatusKey(filter.codexHome, request.Params)
	if !ok {
		return message, nil
	}
	store := mcpStatuses
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loadLocked(filter.home)
	config := mcpConfigVersion(filter.codexHome)
	stored := store.results[key]
	if len(stored.Result) == 0 || stored.Config != config {
		// The first answer passes through and is remembered on its way back.
		filter.statusMu.Lock()
		filter.statusPending[string(request.ID)] = statusWait{key, config}
		filter.statusMu.Unlock()
		return message, nil
	}
	answer, _ = json.Marshal(map[string]any{"id": request.ID, "result": stored.Result})
	if store.refreshing[key] || mcpStatusNow().Sub(time.UnixMilli(stored.At)) < mcpStatusFresh {
		return nil, answer
	}
	store.refreshing[key] = true
	digest := sha256.Sum256([]byte(key))
	refreshID, _ := json.Marshal(mcpStatusRefreshID + hex.EncodeToString(digest[:8]))
	filter.statusMu.Lock()
	filter.statusPending[string(refreshID)] = statusWait{key, config}
	filter.statusMu.Unlock()
	refresh, err := json.Marshal(map[string]any{"id": json.RawMessage(refreshID), "method": request.Method, "params": request.Params})
	if err != nil {
		store.refreshing[key] = false
		return message, nil
	}
	return refresh, answer
}

// statusResponse handles a message from the app-server. It stores status
// results and reports whether ChatGPT should receive the message.
func (filter *chatgptPathFilter) statusResponse(message []byte) bool {
	filter.statusMu.Lock()
	waiting := len(filter.statusPending) > 0
	filter.statusMu.Unlock()
	if !waiting || !bytes.Contains(message, []byte(`"id"`)) {
		return true
	}
	var response jsonRPCResponse
	if json.Unmarshal(message, &response) != nil || response.Method != "" || len(response.ID) == 0 {
		return true
	}
	filter.statusMu.Lock()
	wait, ok := filter.statusPending[string(response.ID)]
	delete(filter.statusPending, string(response.ID))
	filter.statusMu.Unlock()
	if !ok {
		return true
	}
	refresh := bytes.HasPrefix(response.ID, []byte(`"`+mcpStatusRefreshID))
	store := mcpStatuses
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loadLocked(filter.home)
	if refresh {
		store.refreshing[wait.key] = false
	}
	entry := mcpStatusEntry{Result: append(json.RawMessage(nil), response.Result...), Config: wait.config, At: mcpStatusNow().UnixMilli()}
	if len(response.Error) == 0 && len(response.Result) > 0 {
		store.results[wait.key] = entry
		store.saveLocked(filter.home)
	}
	return !refresh
}

// forgetStatusRefreshes releases refreshes this connection started, so the
// next connection refreshes again.
func (filter *chatgptPathFilter) forgetStatusRefreshes() {
	filter.statusMu.Lock()
	keys := []string{}
	for id, wait := range filter.statusPending {
		if strings.HasPrefix(id, `"`+mcpStatusRefreshID) {
			keys = append(keys, wait.key)
		}
	}
	filter.statusPending = map[string]statusWait{}
	filter.statusMu.Unlock()
	store := mcpStatuses
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loadLocked(filter.home)
	for _, key := range keys {
		store.refreshing[key] = false
	}
}
