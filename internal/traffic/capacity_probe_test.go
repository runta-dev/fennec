package traffic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"testing"
	"time"
)

type capacityTransport func(*http.Request) (*http.Response, error)

func (f capacityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type capacityResponseBody struct{ read chan struct{} }

func (b capacityResponseBody) Read([]byte) (int, error) { close(b.read); return 0, io.EOF }
func (b capacityResponseBody) Close() error             { return nil }

func TestCapacityWaitsForDelayedWriteCompletion(t *testing.T) {
	for _, writeErr := range []error{nil, io.ErrClosedPipe} {
		t.Run(fmtError(writeErr), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			read := make(chan struct{})
			trace := make(chan *httptrace.ClientTrace, 1)
			client := &http.Client{Transport: capacityTransport(func(r *http.Request) (*http.Response, error) {
				trace <- httptrace.ContextClientTrace(r.Context())
				return &http.Response{StatusCode: 204, Header: make(http.Header), Body: capacityResponseBody{read}}, nil
			})}
			done := make(chan error, 1)
			go func() { _, err := uploadCapacity(ctx, client, DefaultCapacityConfig()); done <- err }()
			<-read
			select {
			case err := <-done:
				t.Fatalf("finished before write completion: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
			(<-trace).WroteRequest(httptrace.WroteRequestInfo{Err: writeErr})
			if err := <-done; !errors.Is(err, writeErr) {
				t.Fatalf("got %v, want %v", err, writeErr)
			}
		})
	}
}

func fmtError(err error) string {
	if err == nil {
		return "success"
	}
	return "write_failure"
}

func TestCapacityMissingWriteCompletionHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	client := &http.Client{Transport: capacityTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Header: make(http.Header), Body: http.NoBody}, nil
	})}
	if _, err := uploadCapacity(ctx, client, DefaultCapacityConfig()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}
