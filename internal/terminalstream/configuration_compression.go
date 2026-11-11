package terminalstream

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"

	cloudruntime "fennec/internal/runtime"
)

// Match the existing inbound frame budget; compression must not create an
// unbounded allocation path or permit a different operation through this field.
const configurationLimit = 256 << 10

func compressConfiguration(frame Frame) (Frame, error) {
	if frame.Type != "operation" || frame.Operation == nil || frame.Operation.Kind != "reconfigure_runtime" {
		return frame, nil
	}
	raw, err := json.Marshal(frame.Operation)
	if err != nil {
		return Frame{}, err
	}
	if len(raw) > configurationLimit {
		return Frame{}, fmt.Errorf("configuration exceeds transport limit")
	}
	if len(raw) < 4096 {
		return frame, nil
	}
	var encoded bytes.Buffer
	writer, err := gzip.NewWriterLevel(&encoded, gzip.BestSpeed)
	if err != nil {
		return Frame{}, err
	}
	if _, err = writer.Write(raw); err != nil {
		return Frame{}, err
	}
	if err = writer.Close(); err != nil {
		return Frame{}, err
	}
	// Account for JSON base64 expansion and the additional field name.
	if (encoded.Len()+2)/3*4+64 >= len(raw) {
		return frame, nil
	}
	frame.Operation = nil
	frame.ConfigurationGZIP = encoded.Bytes()
	return frame, nil
}
func expandConfiguration(frame *Frame) error {
	if len(frame.ConfigurationGZIP) == 0 {
		return nil
	}
	if frame.Type != "operation" || frame.Operation != nil {
		return fmt.Errorf("ambiguous compressed configuration")
	}
	reader, err := gzip.NewReader(bytes.NewReader(frame.ConfigurationGZIP))
	if err != nil {
		return fmt.Errorf("invalid compressed configuration")
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, configurationLimit+1))
	if err != nil || len(raw) > configurationLimit {
		return fmt.Errorf("invalid compressed configuration size or checksum")
	}
	var operation cloudruntime.LocalOperation
	if json.Unmarshal(raw, &operation) != nil || operation.Kind != "reconfigure_runtime" {
		return fmt.Errorf("invalid compressed operation")
	}
	frame.Operation = &operation
	frame.ConfigurationGZIP = nil
	return nil
}
