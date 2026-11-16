package commandstream

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// ErrUnsupported permits a legacy SSH channel only before a command has been
// sent. A failed or disconnected resident command must never be replayed.
var ErrUnsupported = errors.New("resident command streams unavailable")

type Dialer func(context.Context) (net.Conn, error)
type binding struct{ dial Dialer }

var bindings = struct {
	sync.Mutex
	byClient map[*ssh.Client]*binding
}{byClient: make(map[*ssh.Client]*binding)}

// Register associates an existing authenticated SSH connection with its resident
// transport. Cleanup cannot remove a newer registration after reconnection.
func Register(client *ssh.Client, dial Dialer) func() {
	b := &binding{dial}
	bindings.Lock()
	bindings.byClient[client] = b
	bindings.Unlock()
	return func() {
		bindings.Lock()
		defer bindings.Unlock()
		if bindings.byClient[client] == b {
			delete(bindings.byClient, client)
		}
	}
}

type Session struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	ctx            context.Context
	cancel         context.CancelFunc
	conn           net.Conn
	legacy         *ssh.Session
	mu             sync.Mutex
	started        bool
}

func NewSession(ctx context.Context, client *ssh.Client) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bindings.Lock()
	b := bindings.byClient[client]
	bindings.Unlock()
	life, cancel := context.WithCancel(ctx)
	s := &Session{ctx: life, cancel: cancel}
	var err error
	if b != nil {
		s.conn, err = b.dial(life)
	} else {
		err = ErrUnsupported
	}
	if errors.Is(err, ErrUnsupported) {
		if client == nil {
			cancel()
			return nil, ErrUnsupported
		}
		s.legacy, err = client.NewSession()
	}
	if err != nil {
		cancel()
		return nil, err
	}
	context.AfterFunc(life, func() {
		if s.conn != nil {
			s.conn.Close()
		}
		if s.legacy != nil {
			s.legacy.Close()
		}
	})
	return s, nil
}
func (s *Session) Close() error {
	s.cancel()
	if s.conn != nil {
		return s.conn.Close()
	}
	if s.legacy != nil {
		return s.legacy.Close()
	}
	return nil
}

// A resident stream owns one process group. Cancellation terminates that group
// rather than delivering an SSH channel signal to an unrelated shared service.
func (s *Session) Signal(signal ssh.Signal) error {
	if s.legacy != nil {
		return s.legacy.Signal(signal)
	}
	if signal == ssh.SIGTERM || signal == ssh.SIGKILL {
		return s.Close()
	}
	return fmt.Errorf("unsupported command signal")
}
func (s *Session) Output(command string) ([]byte, error) {
	if s.Stdout != nil {
		return nil, fmt.Errorf("stdout already set")
	}
	var out bytes.Buffer
	s.Stdout = &out
	err := s.Run(command)
	return out.Bytes(), err
}
func (s *Session) Run(command string) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("command already started")
	}
	s.started = true
	s.mu.Unlock()
	defer s.Close()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.legacy != nil {
		s.legacy.Stdin = s.Stdin
		s.legacy.Stdout = s.Stdout
		s.legacy.Stderr = s.Stderr
		return s.legacy.Run(command)
	}
	if len(command) == 0 || len(command) > maxRecord || strings.ContainsRune(command, 0) {
		return fmt.Errorf("invalid command")
	}
	wire := &records{out: s.conn}
	if err := wire.send(request, []byte(command)); err != nil {
		return err
	}
	inputError := make(chan error, 1)
	go func() {
		var err error
		if s.Stdin != nil {
			_, err = io.Copy(outputWriter{wire, stdinData}, s.Stdin)
		}
		if err == nil {
			err = wire.send(stdinEOF, nil)
		}
		inputError <- err
		if err != nil {
			s.conn.Close()
		}
	}()
	for {
		kind, data, err := readRecord(s.conn)
		if err != nil {
			if e := s.ctx.Err(); e != nil {
				return e
			}
			select {
			case e := <-inputError:
				if e != nil {
					return e
				}
			default:
			}
			return fmt.Errorf("remote command outcome unknown: %w", err)
		}
		var out io.Writer
		switch kind {
		case stdoutData:
			out = s.Stdout
		case stderrData:
			out = s.Stderr
		case exitStatus:
			if len(data) != 4 {
				return fmt.Errorf("invalid command exit status")
			}
			code := int(binary.BigEndian.Uint32(data))
			if code != 0 {
				return &ExitError{Code: code}
			}
			return nil
		default:
			return fmt.Errorf("unexpected command record")
		}
		if len(data) > chunk {
			return fmt.Errorf("oversized command output")
		}
		if out != nil {
			if err = writeAll(out, data); err != nil {
				return err
			}
		}
	}
}
