package terminalstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
)

func configurationFixture() cloudruntime.LocalOperation {
	return cloudruntime.LocalOperation{Kind: "reconfigure_runtime", Configuration: &setup.RuntimeConfiguration{Accounts: setup.ModelAccountsRequest{Version: 1, Native: true, Agents: []string{"pi"}, Files: map[string]string{"pi/models.json": strings.Repeat("synthetic", 8000)}}}}
}
func TestConfigurationCompressionRequiresCapabilityAndPreservesPayload(t *testing.T) {
	for _, supported := range []bool{false, true} {
		op := configurationFixture()
		var wire Frame
		transport := &Transport{capabilities: map[string]bool{"configuration_gzip": supported}, input: operationSink{write: func(raw []byte) (int, error) {
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			return len(raw), nil
		}}}
		if err := transport.Send(Frame{Type: "operation", ID: "quiet-river", Operation: &op}); err != nil {
			t.Fatal(err)
		}
		if (len(wire.ConfigurationGZIP) > 0) != supported {
			t.Fatal("capability negotiation ignored")
		}
		if err := expandConfiguration(&wire); err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(op)
		after, _ := json.Marshal(wire.Operation)
		if !bytes.Equal(before, after) {
			t.Fatal("configuration changed in transit")
		}
	}
}
func TestCompressedConfigurationRejectsBombCorruptionAndOtherOperations(t *testing.T) {
	zip := func(raw []byte) []byte {
		var out bytes.Buffer
		w := gzip.NewWriter(&out)
		w.Write(raw)
		w.Close()
		return out.Bytes()
	}
	for _, raw := range [][]byte{[]byte("not gzip"), zip([]byte(strings.Repeat("x", configurationLimit+1))), zip([]byte(`{"kind":"stop","address":"t1","session_id":"quiet-river"}`)), zip([]byte(`null`))} {
		frame := Frame{Type: "operation", ConfigurationGZIP: raw}
		if expandConfiguration(&frame) == nil {
			t.Fatal("invalid compressed operation accepted")
		}
	}
	op := configurationFixture()
	frame, err := compressConfiguration(Frame{Type: "operation", Operation: &op})
	if err != nil {
		t.Fatal(err)
	}
	frame.ConfigurationGZIP = frame.ConfigurationGZIP[:len(frame.ConfigurationGZIP)-3]
	if expandConfiguration(&frame) == nil {
		t.Fatal("truncated checksum accepted")
	}
	frame = Frame{Type: "operation", Operation: &op, ConfigurationGZIP: zip([]byte(`{}`))}
	if expandConfiguration(&frame) == nil {
		t.Fatal("ambiguous encoding accepted")
	}
}
func TestCompressedConfigurationReusesResidentProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	done := make(chan error, 1)
	var calls atomic.Int32
	op := configurationFixture()
	go func() {
		done <- ServeWithOperations(ctx, server, server, nil, nil, nil, func(_ context.Context, received cloudruntime.LocalOperation) (json.RawMessage, error) {
			calls.Add(1)
			if received.Configuration.Accounts.Files["pi/models.json"] != op.Configuration.Accounts.Files["pi/models.json"] {
				t.Error("payload not preserved")
			}
			return json.RawMessage(`{"ok":true}`), nil
		})
	}()
	transport := startTransport(client, client, func() error { return <-done }, client.Close, nil)
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.Supports("configuration_gzip") {
		t.Fatal("compression not advertised")
	}
	for i := 0; i < 2; i++ {
		if _, err := transport.Operate(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 || !transport.Alive() {
		t.Fatal("operations did not reuse the resident protocol")
	}
}
