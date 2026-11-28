package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/board"
	"fennec/internal/control"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var boardScopeID = regexp.MustCompile(`^(local|[a-f0-9]{64})$`)

// Each snapshot names one immutable replica, including during an in-flight
// request or scheduler tick. Never repoint ~/.fen/board at another computer:
// legacy data stays untouched and cannot leak into a newly provisioned runtime.
// localBoard is the board the window and the menu bar read and write. Until
// the cloud verifies the account (cloud computer preparation, 1–3 s after the
// daemon starts), it is the board this same credential verified to last time
// (FEN-231), so the window opens on its projects instead of "Loading…".
func (s *daemonServices) localBoard() board.Store {
	store, _, _ := s.boardSnapshot()
	return store
}

// verifiedBoard is localBoard without the remembered scope: replication and
// the queue scheduler only ever run on a board the cloud verified.
func (s *daemonServices) verifiedBoard() board.Store {
	store, _, _, _ := s.boardSnapshotFor(false)
	return store
}

func (s *daemonServices) boardSnapshot() (board.Store, *terminalChannel, context.Context) {
	store, channel, ctx, _ := s.boardSnapshotFor(true)
	return store, channel, ctx
}

func (s *daemonServices) boardSnapshotFor(remembered bool) (board.Store, *terminalChannel, context.Context, bool) {
	endpoint := ""
	var epoch uint64
	var principal account.Principal
	authorized := true
	if s.account != nil {
		_, principal, epoch, authorized = s.account.Session()
		endpoint = s.account.Endpoint()
	}
	s.mu.Lock()
	scope := "local"
	verified := authorized && (s.account == nil || s.epoch == epoch) && s.user != "" && s.config.RuntimeID != "" && (s.ctx == nil || s.ctx.Err() == nil)
	if verified {
		scope = boardNamespace(endpoint, s.user, s.config.RuntimeID)
	}
	channel, ctx := s.channel, s.ctx
	s.mu.Unlock()
	if s.account != nil && authorized {
		key := credentialScopeKey(endpoint, principal)
		if verified {
			s.boardScopes.remember(s.home, key, scope)
		} else if remembered {
			if last := s.boardScopes.lookup(s.home, key); last != "" {
				scope = last
			}
		}
	}
	return board.Store{Home: s.home, Role: "local", Namespace: scope}, channel, ctx, verified
}

// credentialScopeKey names the signed-in credential without keeping it: a
// new sign-in (new token) or another account never matches an old entry.
func credentialScopeKey(endpoint string, p account.Principal) string {
	if p.Token == "" && p.Machine == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("fennec-board-scope\x00" + endpoint + "\x00" + string(p.Kind) + "\x00" + p.Machine + "\x00" + p.Token))
	return hex.EncodeToString(sum[:])
}

// rememberedBoardScopes is ~/.config/fennec/board-scope.json: the board the
// current credential last verified to (FEN-231). One entry; signing in again
// replaces it.
type rememberedBoardScopes struct {
	mu        sync.Mutex
	loaded    bool
	key       string
	namespace string
}

func boardScopePath(home string) string {
	return filepath.Join(home, ".config/fennec/board-scope.json")
}

type boardScopeFile struct {
	Credential string `json:"credential"`
	Namespace  string `json:"namespace"`
}

func (r *rememberedBoardScopes) loadLocked(home string) {
	if r.loaded {
		return
	}
	r.loaded = true
	var saved boardScopeFile
	if data, err := os.ReadFile(boardScopePath(home)); err == nil && json.Unmarshal(data, &saved) == nil && boardScopeID.MatchString(saved.Namespace) && saved.Namespace != "local" {
		r.key, r.namespace = saved.Credential, saved.Namespace
	}
}

func (r *rememberedBoardScopes) lookup(home, key string) string {
	if key == "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked(home)
	if r.key != key {
		return ""
	}
	return r.namespace
}

func (r *rememberedBoardScopes) remember(home, key, namespace string) {
	if key == "" || namespace == "local" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked(home)
	if r.key == key && r.namespace == namespace {
		return
	}
	r.key, r.namespace = key, namespace
	if err := writePublicJSON(boardScopePath(home), boardScopeFile{Credential: key, Namespace: namespace}); err != nil {
		log.Printf("remembering the board scope: %v", err)
	}
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
