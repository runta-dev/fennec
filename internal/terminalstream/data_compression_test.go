package terminalstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestTerminalDataCompressionBounds(t *testing.T) {
	noise := make([]byte, Chunk)
	_, _ = rand.Read(noise)
	for _, data := range [][]byte{[]byte("small"), bytes.Repeat([]byte("\x1b[0mscreen "), 1000), noise} {
		frame := compressTerminalData(Frame{Type: "data", Data: data})
		if err := expandTerminalData(&frame); err != nil || !bytes.Equal(frame.Data, data) {
			t.Fatalf("terminal bytes changed: %v", err)
		}
	}
	zip := func(data []byte) []byte {
		var out bytes.Buffer
		w := gzip.NewWriter(&out)
		_, _ = w.Write(data)
		_ = w.Close()
		return out.Bytes()
	}
	valid := zip([]byte("hello"))
	corrupt := append([]byte{}, valid...)
	corrupt[len(corrupt)-5] ^= 1
	for name, frame := range map[string]Frame{
		"ambiguous": {Type: "data", Data: []byte("plain"), DataGZIP: valid},
		"operation": {Type: "result", DataGZIP: valid},
		"bomb":      {Type: "data", DataGZIP: zip(bytes.Repeat([]byte("x"), Chunk+1))},
		"empty":     {Type: "data", DataGZIP: zip(nil)},
		"corrupt":   {Type: "data", DataGZIP: corrupt},
		"truncated": {Type: "data", DataGZIP: valid[:len(valid)-1]},
	} {
		t.Run(name, func(t *testing.T) {
			if expandTerminalData(&frame) == nil {
				t.Fatal("accepted invalid compressed terminal data")
			}
		})
	}
}

// Exercise the actual PTY writer, byte-credit refunds, and the old-client
// representation across more than one window, without truncating the screen.
func TestTerminalCompressionNegotiationAndCredit(t *testing.T) {
	for _, encoding := range []string{"", "gzip"} {
		t.Run("encoding="+encoding, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			input, send := io.Pipe()
			receive, output := io.Pipe()
			defer send.Close()
			defer receive.Close()
			go Serve(ctx, input, output, func(_ context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
				executable, err := os.Executable()
				command := exec.Command(executable, "-test.run=^TestTerminalChild$")
				command.Env = append(os.Environ(), "FENNEC_TERMINAL_TEST_CHILD=1", "FENNEC_TERMINAL_TEST_NOISY=1")
				return cloudruntime.LocalTerminal{ID: request.ID, Agent: request.Agent, Address: "t1", State: "live"}, command, err
			})
			decoder, encoder := json.NewDecoder(receive), json.NewEncoder(send)
			var frame Frame
			if err := decoder.Decode(&frame); err != nil || frame.Type != "ready" {
				t.Fatal(frame, err)
			}
			request := cloudruntime.LocalLaunch{ID: "brisk-falcon", Agent: "codex", LocalDirectory: "/project", Device: "1234abcd"}
			if err := encoder.Encode(Frame{Type: "launch", ID: request.ID, Launch: &request, OutputCompression: encoding}); err != nil {
				t.Fatal(err)
			}
			// Credit writes must not block the sole output reader on exit.
			credits := make(chan int, 256)
			defer close(credits)
			go func() {
				for count := range credits {
					_ = encoder.Encode(Frame{Type: "credit", ID: request.ID, Bytes: count})
				}
			}()
			var received bytes.Buffer
			wire := 0
			for {
				frame = Frame{}
				if err := decoder.Decode(&frame); err != nil {
					t.Fatal(err)
				}
				if frame.Type == "error" {
					t.Fatal(frame.Message)
				}
				if frame.Type == "data" {
					wire += len(frame.Data) + len(frame.DataGZIP)
					if encoding == "" && len(frame.DataGZIP) != 0 {
						t.Fatal("sent compressed bytes to an old client")
					}
					if err := expandTerminalData(&frame); err != nil {
						t.Fatal(err)
					}
					received.Write(frame.Data)
					credits <- len(frame.Data)
				}
				if frame.Type == "exit" {
					break
				}
			}
			if !bytes.Equal(received.Bytes(), bytes.Repeat([]byte("X"), 3*Window)) {
				t.Fatalf("lost output or incorrect credit accounting: %d", received.Len())
			}
			if encoding == "gzip" && wire > received.Len()/10 {
				t.Fatalf("screen compression ineffective: %d / %d", wire, received.Len())
			}
		})
	}
}
