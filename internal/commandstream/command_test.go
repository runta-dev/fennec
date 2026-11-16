package commandstream

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func localSession(t *testing.T, ctx context.Context) *Session {
	t.Helper()
	client := new(ssh.Client)
	unregister := Register(client, func(ctx context.Context) (net.Conn, error) { return Dial(ctx), nil })
	t.Cleanup(unregister)
	s, err := NewSession(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestCommandBinaryStreamsEOFAndStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, code := range []int{0, 37} {
		s := localSession(t, ctx)
		payload := bytes.Repeat([]byte{0, 255, 1, '\n', 128}, 40000)
		s.Stdin = bytes.NewReader(payload)
		var stderr bytes.Buffer
		s.Stderr = &stderr
		out, err := s.Output("cat; printf diagnostic >&2; exit " + strconv.Itoa(code))
		if !bytes.Equal(out, payload) || stderr.String() != "diagnostic" {
			t.Fatalf("streams changed: %d %q", len(out), stderr.String())
		}
		var exit *ExitError
		if code == 0 && err != nil || code != 0 && (!errors.As(err, &exit) || exit.Code != code) {
			t.Fatalf("exit %d: %v", code, err)
		}
		if err = s.Run("exit 0"); err == nil {
			t.Fatal("session reused after execution")
		}
	}
	s := localSession(t, ctx)
	out, err := s.Output("cat; printf empty-input-eof")
	if err != nil || string(out) != "empty-input-eof" {
		t.Fatal(string(out), err)
	}
}
func TestCommandEarlyExitDoesNotWaitForInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s := localSession(t, ctx)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	s.Stdin = reader
	if err := s.Run("exit 17"); err == nil {
		t.Fatal("lost early exit")
	} else {
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != 17 {
			t.Fatal(err)
		}
	}
}

type pidWriter struct{ value chan int }

func (w pidWriter) Write(b []byte) (int, error) {
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
		select {
		case w.value <- pid:
		default:
		}
	}
	return len(b), nil
}
func TestCommandCancellationKillsBlockedProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := localSession(t, ctx)
	s.Stdin = strings.NewReader(strings.Repeat("blocked input", 1<<17))
	pid := make(chan int, 1)
	s.Stdout = pidWriter{pid}
	done := make(chan error, 1)
	go func() { done <- s.Run("printf '%s\\n' $$; exec sleep 60") }()
	var process int
	select {
	case process = <-pid:
	case <-ctx.Done():
		t.Fatal("command did not start")
	}
	s.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-ctx.Done():
		t.Fatal("cancel blocked")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(process, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancel left the process running")
}
func TestRegistryReplacementAndNoFailureFallback(t *testing.T) {
	client := new(ssh.Client)
	first := Register(client, func(context.Context) (net.Conn, error) { return nil, errors.New("old") })
	expected := errors.New("connection interrupted")
	second := Register(client, func(context.Context) (net.Conn, error) { return nil, expected })
	defer second()
	first()
	if _, err := NewSession(context.Background(), client); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	// A zero SSH client would panic if a resident failure opened a legacy channel.
}
func TestCommandRejectsOversizedRecord(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn := Dial(ctx)
	defer conn.Close()
	var header [5]byte
	header[0] = request
	binary.BigEndian.PutUint32(header[1:], maxRecord+1)
	if _, err := conn.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("oversized command accepted")
	}
}
