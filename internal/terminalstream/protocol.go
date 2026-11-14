package terminalstream

import (
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"regexp"
)

type Frame struct {
	HookBridge        bool                         `json:"hook_bridge,omitempty"`
	HookID            string                       `json:"hook_id,omitempty"`
	HookRef           string                       `json:"hook_ref,omitempty"`
	Capabilities      []string                     `json:"capabilities,omitempty"`
	Focused           bool                         `json:"focused,omitempty"`
	Timings           map[string]float64           `json:"timings_ms,omitempty"`
	Display           *DisplayFacts                `json:"display,omitempty"`
	Operation         *cloudruntime.LocalOperation `json:"operation,omitempty"`
	ConfigurationGZIP []byte                       `json:"configuration_gzip,omitempty"`
	Result            json.RawMessage              `json:"result,omitempty"`
	Version           int                          `json:"version,omitempty"`
	Type              string                       `json:"type"`
	Term              string                       `json:"term,omitempty"`
	ID                string                       `json:"id,omitempty"`
	Launch            *cloudruntime.LocalLaunch    `json:"launch,omitempty"`
	Terminal          *cloudruntime.LocalTerminal  `json:"terminal,omitempty"`
	Data              []byte                       `json:"data,omitempty"`
	Rows              int                          `json:"rows,omitempty"`
	Columns           int                          `json:"columns,omitempty"`
	Bytes             int                          `json:"bytes,omitempty"`
	Code              int                          `json:"code,omitempty"`
	Message           string                       `json:"message,omitempty"`
	Target            string                       `json:"target,omitempty"`
}

const Window = 65536
const Chunk = 16384
const ProtocolVersion = 10

// Socket targets are a fixed allowlist: the owner's Codex app-server and the
// bounded conversation import service. Peers advertise each capability.
const (
	SocketCodexAppServer     = "codex-app-server"
	SocketSessionImport      = "session-import"
	CapabilitySessionImport  = "session_import"
	CapabilityCodexAppServer = "codex_app_server"
	// CapabilityCodexAppServerEarlyInput: the service holds client bytes that
	// follow the WebSocket upgrade request until the app-server has answered
	// it (codex app-server drops a connection whose request arrives together
	// with frames), so the Mac may complete the handshake for ChatGPT itself.
	CapabilityCodexAppServerEarlyInput = "codex_app_server_early_input"
)

// A socket stream opened with Term SocketCompressionZstd receives its output as
// a zstd stream when the receipt echoes the same Term; older services ignore
// the request and send plain bytes.
const (
	SocketCompressionZstd   = "zstd"
	SocketCompressionWindow = 8 << 20
)

func (frame Frame) validateOpen() error {
	if !regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`).MatchString(frame.ID) {
		return fmt.Errorf("invalid stream identity")
	}
	if frame.Type == "socket" {
		if frame.Target == SocketSessionImport && frame.Message == "" && frame.Term == "" {
			return nil
		}
		if frame.Target == SocketCodexAppServer && (frame.Message == "" || regexp.MustCompile(`^[a-f0-9]{8,64}$`).MatchString(frame.Message)) {
			return nil
		}
		return fmt.Errorf("invalid socket target")
	}
	if frame.Type == "operation" && frame.Operation != nil {
		return frame.Operation.Validate()
	}
	if frame.Type == "launch" && frame.Launch != nil && frame.Launch.ID == frame.ID {
		return frame.Launch.Validate()
	}
	if (frame.Type == "attach" || frame.Type == "stop") && frame.Terminal != nil && regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`).MatchString(frame.Terminal.ID) && regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(frame.Terminal.Address) {
		return nil
	}
	return fmt.Errorf("invalid terminal operation")
}
