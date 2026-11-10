package terminalstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fmt"
)

func (t *Transport) Stop(ctx context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return cloudruntime.LocalTerminal{}, err
	}
	session, err := t.Launch(Frame{Type: "stop", ID: hex.EncodeToString(nonce[:]), Terminal: &reference})
	if err != nil {
		return cloudruntime.LocalTerminal{}, err
	}
	defer session.Close()
	frame, err := session.Next(ctx)
	if err != nil {
		return cloudruntime.LocalTerminal{}, fmt.Errorf("stop outcome unknown; inspect before retrying: %w", err)
	}
	if frame.Type != "result" || frame.Terminal == nil || frame.Terminal.ID != reference.ID || frame.Terminal.State != "finished" {
		return cloudruntime.LocalTerminal{}, fmt.Errorf("stop not confirmed; inspect before retrying")
	}
	return *frame.Terminal, nil
}

func (t *Transport) Operate(ctx context.Context, operation cloudruntime.LocalOperation) (json.RawMessage, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	session, err := t.Launch(Frame{Type: "operation", ID: hex.EncodeToString(nonce[:]), Operation: &operation})
	if err != nil {
		return nil, err
	}
	defer session.Close()
	frame, err := session.Next(ctx)
	if err != nil {
		return nil, fmt.Errorf("operation outcome unknown; inspect before retrying: %w", err)
	}
	if frame.Type != "result" || !json.Valid(frame.Result) {
		return nil, fmt.Errorf("operation not confirmed; inspect before retrying")
	}
	return frame.Result, nil
}
