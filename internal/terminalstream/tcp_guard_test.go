package terminalstream

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

type guardedTestOutput struct {
	bytes.Buffer
	guarded bool
}

func (w *guardedTestOutput) TCPQueueGuarded() bool { return w.guarded }

func TestTCPGuardCapabilityReflectsActualWriter(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		output := &guardedTestOutput{guarded: guarded}
		if err := Serve(context.Background(), strings.NewReader(""), output, nil); err != nil {
			t.Fatal(err)
		}
		var ready Frame
		if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &ready); err != nil {
			t.Fatal(err)
		}
		if ready.Type != "ready" || slices.Contains(ready.Capabilities, CapabilityTCPOutputGuard) != guarded {
			t.Fatal("incorrect guard capability", ready)
		}
	}
}
