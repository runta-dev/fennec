package terminalstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/launchprofile"
	cloudruntime "fennec/internal/runtime"
	"fmt"
	"time"
)

func (t *Transport) Stop(ctx context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, error) {
	if err := ctx.Err(); err != nil {
		return cloudruntime.LocalTerminal{}, err
	}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now()
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
	launchprofile.Record("operation."+operation.Kind+".roundtrip", started, time.Since(started))
	if elapsed, ok := frame.Timings["operation"]; ok {
		launchprofile.Record("operation."+operation.Kind+".remote", time.Now(), time.Duration(elapsed*float64(time.Millisecond)))
	}
	return frame.Result, nil
}
