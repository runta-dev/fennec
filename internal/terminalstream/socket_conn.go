package terminalstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
)

// DialSocket adapts a whitelisted resident byte stream to net.Conn. It never
// opens an SSH channel. A dedicated frame reader handles input credits even
// while the application has stopped reading output; the unread data budget is
// bounded by the protocol window. The returned connection owns only its stream.
// As with net.Dialer, ctx bounds opening, not the returned connection's lifetime.
func (t *Transport) DialSocket(ctx context.Context, target, device string) (net.Conn, error) {
	if err := t.Ready(ctx); err != nil {
		return nil, err
	}
	if target == SocketDeviceRelay && !t.Supports(CapabilityDeviceRelay) {
		return nil, fmt.Errorf("resident device relay unavailable")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	session, err := t.Launch(Frame{Type: "socket", ID: hex.EncodeToString(nonce[:]), Target: target, Message: device})
	if err != nil {
		return nil, err
	}
	frame, err := session.Next(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil || frame.Type != "receipt" {
		session.Close()
		if err == nil {
			err = fmt.Errorf("resident socket open failed")
		}
		return nil, err
	}
	local, remote := net.Pipe()
	life, cancel := context.WithCancel(context.Background())
	window := NewCreditWindow()
	received := newSocketBuffer()
	stop := context.AfterFunc(life, func() { window.Close(); received.close(net.ErrClosed); remote.Close() })
	conn := &socketConn{Conn: local, cancel: cancel}
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		for {
			frame, err := session.Next(life)
			if err != nil {
				received.close(err)
				window.Close()
				return
			}
			switch frame.Type {
			case "data":
				if err = received.push(frame.Data); err != nil {
					cancel()
					return
				}
			case "input_credit":
				if !window.Add(frame.Bytes) {
					cancel()
					return
				}
			case "exit":
				received.close(io.EOF)
				window.Close()
				return
			default:
				cancel()
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		defer cancel()
		data := make([]byte, OutputChunk)
		for {
			n, err := remote.Read(data)
			if n > 0 {
				for offset := 0; offset < n; {
					count := window.Take(n - offset)
					if count == 0 {
						return
					}
					if session.Send(Frame{Type: "input", Data: data[offset : offset+count]}) != nil {
						return
					}
					offset += count
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		defer cancel()
		for {
			data, err := received.next(life)
			if err != nil {
				return
			}
			n, err := remote.Write(data)
			if n > 0 {
				if session.Send(Frame{Type: "credit", Bytes: n}) != nil {
					return
				}
			}
			if err != nil || n != len(data) {
				return
			}
		}
	}()
	go func() { workers.Wait(); stop(); session.Close() }()
	return conn, nil
}

type socketConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *socketConn) Close() error { c.cancel(); return c.Conn.Close() }

// Copy payloads into one bounded byte buffer rather than retaining arbitrarily
// many tiny frame structures. Credits are returned only after the caller reads.
type socketBuffer struct {
	mu      sync.Mutex
	data    bytes.Buffer
	changed chan struct{}
	err     error
}

func newSocketBuffer() *socketBuffer { return &socketBuffer{changed: make(chan struct{})} }
func (b *socketBuffer) notify()      { close(b.changed); b.changed = make(chan struct{}) }
func (b *socketBuffer) push(data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if len(data) > Window-b.data.Len() {
		return fmt.Errorf("socket receive window exceeded")
	}
	b.data.Write(data)
	b.notify()
	return nil
}
func (b *socketBuffer) close(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		b.err = err
		b.notify()
	}
}
func (b *socketBuffer) next(ctx context.Context) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.data.Len() > 0 {
			data := append([]byte(nil), b.data.Next(min(b.data.Len(), OutputChunk))...)
			b.mu.Unlock()
			return data, nil
		}
		if b.err != nil {
			err := b.err
			b.mu.Unlock()
			return nil, err
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}
