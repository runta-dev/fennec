package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fennec/internal/board"
	"fennec/internal/control"
	"regexp"
)

var boardScopeID = regexp.MustCompile(`^(local|[a-f0-9]{64})$`)

// Each snapshot names one immutable replica, including during an in-flight
// request or scheduler tick. Never repoint ~/.fen/board at another computer:
// legacy data stays untouched and cannot leak into a newly provisioned runtime.
func (s *daemonServices) localBoard() board.Store {
	store, _, _ := s.boardSnapshot()
	return store
}

func (s *daemonServices) boardSnapshot() (board.Store, *terminalChannel, context.Context) {
	endpoint := ""
	var epoch uint64
	authorized := true
	if s.account != nil {
		_, _, epoch, authorized = s.account.Session()
		endpoint = s.account.Endpoint()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := "local"
	if authorized && (s.account == nil || s.epoch == epoch) && s.user != "" && s.config.RuntimeID != "" && (s.ctx == nil || s.ctx.Err() == nil) {
		scope = boardNamespace(endpoint, s.user, s.config.RuntimeID)
	}
	return board.Store{Home: s.home, Role: "local", Namespace: scope}, s.channel, s.ctx
}

func boardNamespace(endpoint, owner, runtimeID string) string {
	sum := sha256.Sum256([]byte(endpoint + "\x00" + owner + "\x00" + runtimeID))
	return hex.EncodeToString(sum[:])
}

func (s *daemonServices) checkBoardScope(scope string) error {
	_, err := s.scopedBoard(scope)
	return err
}

func (s *daemonServices) scopedBoard(scope string) (board.Store, error) {
	store := s.localBoard()
	if scope != "" && scope != store.Namespace {
		return board.Store{}, &control.Fault{Code: "board_changed", Message: "The cloud computer changed; refresh the queue", Retryable: true}
	}
	return store, nil
}

func (s *daemonServices) boardLifetime(parent context.Context) (context.Context, context.CancelFunc) {
	s.mu.Lock()
	lifetime := s.ctx
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	if lifetime != nil {
		stop := context.AfterFunc(lifetime, cancel)
		return ctx, func() { stop(); cancel() }
	}
	return ctx, cancel
}
