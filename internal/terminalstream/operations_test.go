package terminalstream

import (
	"context"
	"encoding/json"
	"errors"
	cloudruntime "fennec/internal/runtime"
	"io"
	"testing"
	"time"
)

type operationSink struct{ write func([]byte) (int, error) }

func (s operationSink) Write(data []byte) (int, error) { return s.write(data) }
func (s operationSink) Close() error                   { return nil }

func TestOperationCompletionDoesNotSendClose(t *testing.T) {
	for _, kind := range []string{"read", "stop", "error", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			transport := &Transport{sessions: map[string]*mailbox{}}
			var sent []string
			transport.input = operationSink{write: func(data []byte) (int, error) {
				var request Frame
				if err := json.Unmarshal(data, &request); err != nil {
					return 0, err
				}
				sent = append(sent, request.Type)
				if request.Type == "close" || kind == "timeout" {
					return len(data), nil
				}
				result := Frame{Type: "result", ID: request.ID, Result: json.RawMessage(`{"screen":"ok"}`)}
				if kind == "stop" {
					result.Terminal = &cloudruntime.LocalTerminal{ID: "quiet-river", Address: "t1", State: "finished"}
				}
				if kind == "error" {
					result.Type = "error"
				}
				if err := transport.sessions[request.ID].push(result); err != nil {
					return 0, err
				}
				return len(data), nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			var err error
			if kind == "stop" {
				_, err = transport.Stop(ctx, cloudruntime.LocalTerminal{ID: "quiet-river", Address: "t1"})
			} else {
				_, err = transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "read", SessionID: "quiet-river", Address: "t1"})
			}
			if (err != nil) != (kind == "error" || kind == "timeout") {
				t.Fatalf("unexpected result: %v", err)
			}
			want := 1
			if kind == "timeout" {
				want = 2
			}
			if len(sent) != want || (want == 2 && sent[1] != "close") {
				t.Fatalf("unexpected wire frames: %v", sent)
			}
			if len(transport.sessions) != 0 {
				t.Fatal("completed mailbox retained")
			}
		})
	}
}

func TestCancelledOperationNeverSendsMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &Transport{input: operationSink{write: func([]byte) (int, error) { t.Fatal("cancelled operation wrote to transport"); return 0, nil }}}
	if _, err := transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "list"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := transport.Stop(ctx, cloudruntime.LocalTerminal{ID: "quiet-river", Address: "t1"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTerminalExitDrainsOutputWithoutClosePacket(t *testing.T) {
	box := newMailbox()
	transport := &Transport{sessions: map[string]*mailbox{"quiet-river": box}, input: operationSink{write: func([]byte) (int, error) {
		t.Fatal("final exit caused an extra wire write")
		return 0, nil
	}}}
	session := &Session{transport: transport, id: "quiet-river", events: box}
	box.push(Frame{Type: "data", Data: []byte("final output")})
	box.push(Frame{Type: "exit", Code: 7})
	frame, err := session.Next(context.Background())
	if err != nil || string(frame.Data) != "final output" || len(transport.sessions) != 1 {
		t.Fatalf("output lost before exit: %+v %v", frame, err)
	}
	frame, err = session.Next(context.Background())
	if err != nil || frame.Type != "exit" || frame.Code != 7 {
		t.Fatalf("exit lost: %+v %v", frame, err)
	}
	session.Close()
	if len(transport.sessions) != 0 {
		t.Fatal("finished stream retained")
	}
	if _, err := session.Next(context.Background()); err != io.EOF {
		t.Fatalf("stream did not finish: %v", err)
	}
}
