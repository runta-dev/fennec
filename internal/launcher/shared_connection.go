package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fennec/internal/connectivity"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type SharedConnection struct {
	Online        func() bool
	WaitOnline    func(context.Context) error
	Changes       func() <-chan struct{}
	startedUnixMS atomic.Int64
	ctx           context.Context
	mu            sync.Mutex
	pending       chan struct{}
	ready         bool
	err           error
	control       string
	initialize    func(context.Context) error
	check         func(context.Context) error
	stop          func()
}

// ControlPath is immutable; callers still obtain the resident transport before
// use. Reading this path must not fork a local ssh check on each hot operation.
func (manager *SharedConnection) ControlPath() string  { return manager.control }
func (manager *SharedConnection) StartedUnixMS() int64 { return manager.startedUnixMS.Load() }

// Close releases a fallback master after its owner has cancelled the lifecycle.
func (manager *SharedConnection) Close() {
	manager.mu.Lock()
	pending := manager.pending
	manager.mu.Unlock()
	if pending != nil {
		<-pending
	}
	manager.stop()
}

func NewSharedConnection(ctx context.Context, directory, runtimeID string, authorize func(context.Context) error) (*SharedConnection, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	control, err := SessionControlPath(directory, runtimeID, hex.EncodeToString(random[:]))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(control), 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Dir(control))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("SSH control directory must be private")
	}
	manager := &SharedConnection{ctx: ctx, control: control}
	controlOperation := func(ctx context.Context, operation string) error {
		return exec.CommandContext(ctx, "/usr/bin/ssh", "-S", control, "-O", operation, runtimeID+"@ssh.runta.com").Run()
	}
	manager.check = func(ctx context.Context) error {
		checkCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return controlOperation(checkCtx, "check")
	}
	manager.stop = func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		controlOperation(stopCtx, "stop")
	}
	connect := func(ctx context.Context) error {
		args := []string{"-N", "-f", "-M", "-S", control, "-o", "ControlPersist=yes", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8", "-o", "UserKnownHostsFile=" + filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), runtimeID + "@ssh.runta.com"}
		err := exec.CommandContext(ctx, "/usr/bin/ssh", args...).Run()
		if ctx.Err() != nil {
			manager.stop()
			return ctx.Err()
		}
		return err
	}
	manager.initialize = func(ctx context.Context) error {
		manager.startedUnixMS.Store(time.Now().UnixMilli())
		identity, err := connectionIdentity(directory)
		if err != nil {
			if err := authorize(ctx); err != nil {
				return err
			}
			return connect(ctx)
		}
		return prepareConnection(ctx, authorize, connect, func() bool { current, err := connectionIdentity(directory); return err == nil && current == identity }, manager.stop)
	}
	return manager, nil
}

func (manager *SharedConnection) Start() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			manager.mu.Lock()
			pending := manager.pending
			manager.mu.Unlock()
			if pending != nil {
				<-pending
			}
			manager.stop()
		}()
		for {
			if manager.WaitOnline != nil {
				if manager.WaitOnline(manager.ctx) != nil {
					return
				}
			}
			var netChanged <-chan struct{}
			if manager.Changes != nil {
				netChanged = manager.Changes()
			}
			manager.Get(manager.ctx)
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-manager.ctx.Done():
				timer.Stop()
				return
			case <-netChanged:
				timer.Stop()
				if manager.Online != nil && !manager.Online() {
					manager.mu.Lock()
					wasReady := manager.ready
					manager.ready = false
					manager.mu.Unlock()
					if wasReady {
						manager.stop()
					}
				}
			case <-timer.C:
			}
		}
	}()
	return done
}

func (manager *SharedConnection) Get(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if manager.Online != nil && !manager.Online() {
		manager.mu.Lock()
		manager.ready = false
		manager.mu.Unlock()
		return "", connectivity.ErrOffline
	}
	manager.mu.Lock()
	if manager.ctx.Err() != nil {
		manager.mu.Unlock()
		return "", manager.ctx.Err()
	}
	if manager.ready && manager.check(ctx) == nil {
		manager.mu.Unlock()
		return manager.control, nil
	}
	if err := ctx.Err(); err != nil {
		manager.mu.Unlock()
		return "", err
	}
	if manager.pending == nil {
		manager.ready = false
		manager.pending = make(chan struct{})
		pending := manager.pending
		go func() {
			operation, cancel := context.WithTimeout(manager.ctx, 35*time.Second)
			err := manager.initialize(operation)
			cancel()
			manager.mu.Lock()
			manager.err = err
			manager.ready = err == nil
			manager.pending = nil
			close(pending)
			manager.mu.Unlock()
		}()
	}
	pending := manager.pending
	manager.mu.Unlock()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-pending:
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.err != nil {
		return "", manager.err
	}
	return manager.control, nil
}
