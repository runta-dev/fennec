package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/commandstream"
	"fennec/internal/control"
	"fennec/internal/syncengine"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func transferSSH(args []string, input io.Reader, output, stderr io.Writer) error {
	if len(args) < 4 || args[2] != "--" {
		return fmt.Errorf("transfer account, runtime and SSH command required")
	}
	command, err := syncengine.ManagedSSHCommand(args[3:], args[1])
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	session, err := openManagedCommand(ctx, home, args[0], args[1])
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin, session.Stdout, session.Stderr = input, output, stderr
	err = session.Run(command)
	var status *commandstream.ExitError
	if errors.As(err, &status) {
		return transferExit{code: status.Code}
	}
	return err
}

type transferExit struct{ code int }

func (e transferExit) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.code)
}
func (e transferExit) ExitCode() int { return e.code }

type transferScope struct {
	Owner     string `json:"owner"`
	RuntimeID string `json:"runtime_id"`
}
type transferOffer struct {
	Socket string `json:"socket"`
}

func (s *daemonServices) openTransfer(ctx context.Context, raw json.RawMessage) (any, error) {
	var request transferScope
	if json.Unmarshal(raw, &request) != nil || request.Owner == "" || request.RuntimeID == "" {
		return nil, fmt.Errorf("transfer account and runtime required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid := s.ctx != nil && s.ctx.Err() == nil && s.user == request.Owner && s.config.RuntimeID == request.RuntimeID && s.channel != nil
	if !valid {
		s.mu.Unlock()
		return nil, fmt.Errorf("transfer account or runtime unavailable")
	}
	life, epoch := s.ctx, s.epoch
	if s.transferSlots == nil {
		s.transferSlots = make(chan struct{}, 32)
	}
	slots := s.transferSlots
	select {
	case slots <- struct{}{}:
	default:
		s.mu.Unlock()
		return nil, fmt.Errorf("transfer capacity exhausted")
	}
	s.mu.Unlock()
	directory := filepath.Join(s.home, ".config/fennec/ipc")
	if err := os.MkdirAll(directory, 0700); err != nil {
		<-slots
		return nil, err
	}
	path, done, err := commandstream.OfferLocal(life, directory, func(ctx context.Context) (net.Conn, error) { return s.dialTransfer(ctx, epoch, request) })
	if err != nil {
		<-slots
		return nil, err
	}
	go func() { <-done; <-slots }()
	return transferOffer{Socket: path}, nil
}
func (s *daemonServices) dialTransfer(ctx context.Context, epoch uint64, scope transferScope) (net.Conn, error) {
	current := func() bool {
		return s.ctx != nil && s.ctx.Err() == nil && s.epoch == epoch && s.user == scope.Owner && s.config.RuntimeID == scope.RuntimeID
	}
	s.mu.Lock()
	valid := current()
	channel := s.channel
	s.mu.Unlock()
	if !valid || channel == nil {
		return nil, fmt.Errorf("transfer account or runtime changed")
	}
	transport, err := channel.get(ctx)
	if err != nil {
		return nil, err
	}
	if err = transport.Ready(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid = current() && s.channel == channel
	s.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("transfer account or runtime changed")
	}
	conn, err := transport.DialSocket(ctx, terminalstream.SocketCommand, "")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	valid = current() && s.channel == channel
	s.mu.Unlock()
	if !valid {
		conn.Close()
		return nil, fmt.Errorf("transfer account or runtime changed")
	}
	return conn, nil
}

func openManagedCommand(ctx context.Context, home, owner, runtimeID string) (*commandstream.Session, error) {
	var offer transferOffer
	opening, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
	if err := client.Call(opening, "transfer.open", transferScope{Owner: owner, RuntimeID: runtimeID}, &offer); err != nil {
		return nil, err
	}
	// Only the private daemon directory can supply this one-shot local endpoint.
	if filepath.Dir(offer.Socket) != filepath.Dir(client.Socket) {
		return nil, fmt.Errorf("invalid transfer socket")
	}
	conn, err := (&net.Dialer{}).DialContext(opening, "unix", offer.Socket)
	if err != nil {
		return nil, err
	}
	return commandstream.NewStream(ctx, conn), nil
}
