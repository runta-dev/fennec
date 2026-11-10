package terminalstream

import (
	"context"
	"encoding/json"
	r "fennec/internal/runtime"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStopOperationsReuseOneStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer send.Close()
	defer receive.Close()
	done := make(chan error, 1)
	go func() {
		done <- ServeWithStop(ctx, input, output, nil, nil, func(_ context.Context, reference r.LocalTerminal) (r.LocalTerminal, error) {
			reference.State = "finished"
			return reference, nil
		})
	}()
	encoder, decoder := json.NewEncoder(send), json.NewDecoder(receive)
	var ready Frame
	if err := decoder.Decode(&ready); err != nil || ready.Type != "ready" {
		t.Fatal(ready, err)
	}
	for _, char := range []string{"a", "b", "c"} {
		id := strings.Repeat(char, 32)
		if err := encoder.Encode(Frame{Type: "stop", ID: id, Terminal: &r.LocalTerminal{ID: strings.Repeat("d", 32), Address: "t1"}}); err != nil {
			t.Fatal(err)
		}
		var reply Frame
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if reply.Type != "result" || reply.ID != id || reply.Terminal == nil || reply.Terminal.State != "finished" {
			t.Fatal(reply)
		}
	}
	send.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server failed to close")
	}
}
