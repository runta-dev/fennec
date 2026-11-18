package fennecmobile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"

	"fennec/internal/runta"

	"golang.org/x/crypto/ssh"
)

// Error kinds the app treats differently. An error that crosses the binding
// with a kind reads "[kind] message"; the app shows its own words for the
// kind and keeps the message only as a detail (ErrorKind splits them).
const (
	// The link to the cloud computer dropped or timed out; trying again
	// usually works, so the app shows "Reconnecting…", not the error.
	kindNetwork = "network"
	// The Runta sign-in is missing or no longer accepted.
	kindSignedOut = "signed_out"
	// The cloud computer exists but is not running.
	kindCloudStopped = "cloud_stopped"
	// The Mac has not created the shared cloud computer yet.
	kindNoComputer = "no_computer"
)

var (
	errSignedOut      = errors.New("sign in first")
	errSessionExpired = errors.New("your session expired; sign in again")
)

type cloudStoppedError struct{ status string }

func (e cloudStoppedError) Error() string { return fmt.Sprintf("the cloud computer is %s", e.status) }

type kindError struct {
	kind string
	err  error
}

func (e *kindError) Error() string { return "[" + e.kind + "] " + e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

// classify tags err with its kind when it has one.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var tagged *kindError
	if errors.As(err, &tagged) {
		return err
	}
	if kind := errorKind(err); kind != "" {
		return &kindError{kind: kind, err: err}
	}
	return err
}

func errorKind(err error) string {
	var stopped cloudStoppedError
	switch {
	case errors.Is(err, errNoComputer):
		return kindNoComputer
	case errors.Is(err, errSignedOut), errors.Is(err, errSessionExpired):
		return kindSignedOut
	case errors.As(err, &stopped):
		return kindCloudStopped
	case transient(err):
		return kindNetwork
	}
	return ""
}

// transient reports a dropped or unreachable connection. SSH and remote
// stderr often lose the error's type, so known messages count too.
func transient(err error) bool {
	for _, target := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe, net.ErrClosed, context.DeadlineExceeded,
		syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ECONNREFUSED, syscall.EPIPE, syscall.ETIMEDOUT,
		syscall.ENETDOWN, syscall.ENETUNREACH, syscall.EHOSTUNREACH} {
		if errors.Is(err, target) {
			return true
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return true
	}
	var apiErr *runta.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 429 || apiErr.Status >= 500
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"eof", "connection reset", "broken pipe", "closed pipe", "use of closed network connection",
		"i/o timeout", "timed out", "timeout", "no route to host", "network is unreachable", "network is down",
		"connection refused", "connection aborted", "connection lost", "handshake failed", "no such host",
		"server misbehaving", "tls handshake", "remote command exited without exit status"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

// ErrorKind returns the kind and the original message of an error the
// binding reported ("" kind when it has none).
func ErrorKind(message string) string {
	if strings.HasPrefix(message, "[") {
		if end := strings.Index(message, "] "); end > 1 {
			return message[1:end]
		}
	}
	return ""
}

// ErrorDetail returns the message without its kind.
func ErrorDetail(message string) string {
	if ErrorKind(message) != "" {
		return message[strings.Index(message, "] ")+2:]
	}
	return message
}
