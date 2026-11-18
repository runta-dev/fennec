package fennecmobile

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"fennec/internal/runta"
)

func TestClassifyTagsErrorsTheAppShowsDifferently(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{io.EOF, kindNetwork},
		{fmt.Errorf("read: %w", syscall.ECONNRESET), kindNetwork},
		{&net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}, kindNetwork},
		{errors.New("read tcp 192.168.1.4:52311->34.82.1.9:443: read: connection reset by peer"), kindNetwork},
		{errors.New("write |1: exceed closed pipe"), kindNetwork},
		{errors.New("ssh: handshake failed: EOF"), kindNetwork},
		{&runta.APIError{Status: 503}, kindNetwork},
		{&runta.APIError{Status: 404}, ""},
		{errNoComputer, kindNoComputer},
		{errSignedOut, kindSignedOut},
		{errSessionExpired, kindSignedOut},
		{cloudStoppedError{"stopped"}, kindCloudStopped},
		{errors.New("session not found"), ""},
	} {
		got := classify(tc.err)
		if kind := ErrorKind(got.Error()); kind != tc.kind {
			t.Errorf("%v: kind %q, want %q", tc.err, kind, tc.kind)
		}
		if detail := ErrorDetail(got.Error()); detail != tc.err.Error() {
			t.Errorf("%v: detail %q", tc.err, detail)
		}
		if !errors.Is(got, tc.err) && !errors.As(got, new(*runta.APIError)) && !errors.As(got, new(cloudStoppedError)) {
			t.Errorf("%v: classify lost the original error", tc.err)
		}
	}
	if classify(nil) != nil {
		t.Fatal("classify(nil) must stay nil")
	}
	twice := classify(classify(io.EOF)).Error()
	if twice != "[network] EOF" {
		t.Fatalf("tagged twice: %q", twice)
	}
}

func TestErrorKindIgnoresMessagesWithoutAKind(t *testing.T) {
	for _, message := range []string{"", "EOF", "[not closed", "[] x", "a [network] b"} {
		if kind := ErrorKind(message); kind != "" {
			t.Errorf("%q: kind %q", message, kind)
		}
		if detail := ErrorDetail(message); detail != message {
			t.Errorf("%q: detail %q", message, detail)
		}
	}
}
