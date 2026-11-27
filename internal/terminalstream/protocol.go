package terminalstream

import (
	"encoding/json"
	"fennec/internal/cloudops"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"regexp"
)

type Frame struct {
	interactive       bool                        // Local scheduling hint; never accepted from a remote JSON frame.
	bulk              bool                        // Opaque stream payload, including client-to-cloud socket input.
	HookBridge        bool                        `json:"hook_bridge,omitempty"`
	HookID            string                      `json:"hook_id,omitempty"`
	HookRef           string                      `json:"hook_ref,omitempty"`
	Capabilities      []string                    `json:"capabilities,omitempty"`
	Focused           bool                        `json:"focused,omitempty"`
	Timings           map[string]float64          `json:"timings_ms,omitempty"`
	Display           *DisplayFacts               `json:"display,omitempty"`
	Operation         *cloudops.Operation         `json:"operation,omitempty"`
	ConfigurationGZIP []byte                      `json:"configuration_gzip,omitempty"`
	Result            json.RawMessage             `json:"result,omitempty"`
	Version           int                         `json:"version,omitempty"`
	Type              string                      `json:"type"`
	Term              string                      `json:"term,omitempty"`
	ID                string                      `json:"id,omitempty"`
	Launch            *cloudruntime.LocalLaunch   `json:"launch,omitempty"`
	Terminal          *cloudruntime.LocalTerminal `json:"terminal,omitempty"`
	Data              []byte                      `json:"data,omitempty"`
	DataGZIP          []byte                      `json:"data_gzip,omitempty"`
	OutputCompression string                      `json:"output_compression,omitempty"`
	Rows              int                         `json:"rows,omitempty"`
	Columns           int                         `json:"columns,omitempty"`
	Bytes             int                         `json:"bytes,omitempty"`
	Code              int                         `json:"code,omitempty"`
	Message           string                      `json:"message,omitempty"`
	ErrorCode         string                      `json:"error_code,omitempty"`
	Target            string                      `json:"target,omitempty"`
	// Seq is the client's input sequence number at the end of an input or
	// image frame; echo_ack frames return the highest processed Seq.
	Seq uint64 `json:"seq,omitempty"`
	// DelayMS is the gap the user left before this keystroke frame. Services
	// with input cadence keep at least min(DelayMS, 50) between PTY writes.
	DelayMS int `json:"delay_ms,omitempty"`
	// Features are optional stream behaviours the client requests on connect
	// and that the service confirms in the receipt's Capabilities.
	Features []string `json:"features,omitempty"`
}

// CapabilityEchoAck: after input up to Seq has been written to the PTY, the
// service sends echo_ack{Seq} once the agent's output has gone quiet for
// 15 ms, or after 250 ms without output (Code 1: the agent is busy).
// CapabilityInputCadence: the service paces bunched keystroke frames by
// DelayMS so agents with timing-based paste detection see typing.
const (
	CapabilityEchoAck      = "echo_ack_v1"
	CapabilityInputCadence = "input_cadence_v1"
)

const Window = 65536
const Chunk = 16384

// OutputChunk bounds a bulk writer's turn without changing the wire maximum
// accepted from older peers. At constrained link speeds a 16 KiB frame delays
// every following control reply; 1 KiB fits an ordinary paced wire packet.
const OutputChunk = 1024
const ProtocolVersion = 11

// Socket targets are a fixed allowlist: the owner's Codex app-server and the
// bounded conversation import service. Peers advertise each capability.
const (
	SocketCodexAppServer     = "codex-app-server"
	SocketSessionImport      = "session-import"
	CapabilitySessionImport  = "session_import"
	CapabilityCodexAppServer = "codex_app_server"
	SocketDeviceRelay        = "device-relay"
	CapabilityDeviceRelay    = "device_relay_v1"
	SocketMagpieWeb          = "magpie-web"
	CapabilityMagpieWeb      = "magpie_web_v1"
	SocketPreview            = "preview"
	CapabilityPreview        = "preview_http_v1"
	SocketBoard              = "board"
	CapabilityBoard          = "board_http_v1"
	CapabilityBoardImages    = "board_images_v1"
	CapabilityBoardWebP      = "board_webp_v1"
	CapabilityThreadSession  = "thread_session_v1"
	// CapabilityCloudArchive: `fennec-terminal archive` exports and imports
	// queues and sessions over the command stream (THI-165).
	CapabilityCloudArchive = "cloud_archive_v1"
	SocketCommand          = "command"
	CapabilityCommand      = "command_stream_v1"
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
		if (frame.Target == SocketCommand || frame.Target == SocketMagpieWeb || frame.Target == SocketPreview || frame.Target == SocketBoard) && frame.Message == "" && frame.Term == "" {
			return nil
		}
		if frame.Target == SocketDeviceRelay && regexp.MustCompile(`^[a-f0-9]{8,64}$`).MatchString(frame.Message) && frame.Term == "" {
			return nil
		}
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
