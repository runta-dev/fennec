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
}

const Window = 65536
const Chunk = 16384
const ProtocolVersion = 7

func (frame Frame) validateOpen() error {
	if !regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`).MatchString(frame.ID) {
		return fmt.Errorf("invalid stream identity")
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
