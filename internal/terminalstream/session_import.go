package terminalstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fmt"
)

// ImportSession shares the resident transport with terminals and operations.
// Flow credits bound in-flight bytes even for a large screenshot history.
func ImportSession(ctx context.Context, t *Transport, b *cloudruntime.SessionImport) error {
	if !t.Supports(CapabilitySessionImport) {
		return fmt.Errorf("cloud conversation transfer is unavailable")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	s, err := t.Launch(Frame{Type: "socket", ID: hex.EncodeToString(id[:]), Target: SocketSessionImport})
	if err != nil {
		return err
	}
	defer s.Close()
	window := NewCreditWindow()
	defer window.Close()
	query, _ := json.Marshal(cloudruntime.SessionImportQuery{Agent: b.Agent, Conversation: b.Conversation, Marker: b.Marker})
	query = append(query, '\n')
	if window.Take(len(query)) != len(query) {
		return fmt.Errorf("conversation stream closed")
	}
	if err = s.Send(Frame{Type: "input", Data: query}); err != nil {
		return err
	}
	var reply bytes.Buffer
	checking := true
	for {
		frame, err := s.Next(ctx)
		if err != nil {
			return err
		}
		switch frame.Type {
		case "receipt":
		case "input_credit":
			if !window.Add(frame.Bytes) {
				return fmt.Errorf("invalid conversation stream credit")
			}
		case "data":
			if reply.Len()+len(frame.Data) > 8192 {
				return fmt.Errorf("invalid conversation reply")
			}
			reply.Write(frame.Data)
			if err := s.Send(Frame{Type: "credit", Bytes: len(frame.Data)}); err != nil {
				return err
			}
			if !bytes.Contains(reply.Bytes(), []byte("\n")) {
				continue
			}
			var result cloudruntime.SessionImportReply
			if json.Unmarshal(reply.Bytes(), &result) != nil {
				return fmt.Errorf("invalid conversation reply")
			}
			if result.Error != "" {
				return fmt.Errorf("could not move conversation to the cloud computer")
			}
			if !checking || result.Present {
				return nil
			}
			checking = false
			reply.Reset()
			payload, err := cloudruntime.EncodeSessionImport(b)
			if err != nil {
				return err
			}
			length := make([]byte, 8)
			binary.BigEndian.PutUint64(length, uint64(len(payload)))
			go func() {
				for _, data := range [][]byte{length, payload} {
					for len(data) > 0 {
						n := window.Take(min(len(data), Chunk))
						if n == 0 {
							return
						}
						if s.Send(Frame{Type: "input", Data: data[:n]}) != nil {
							return
						}
						data = data[n:]
					}
				}
			}()
		case "error", "exit":
			return fmt.Errorf("conversation transfer ended before confirmation")
		default:
			return fmt.Errorf("unexpected conversation stream response")
		}
	}
}
