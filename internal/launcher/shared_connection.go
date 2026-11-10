package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type SharedConnection struct {
	ctx        context.Context
	mu         sync.Mutex
	pending    chan struct{}
	ready      bool
	err        error
	control    string
	initialize func(context.Context) error
	check      func(context.Context) error
	stop       func()
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
	manager.initialize = func(ctx context.Context) error {
		if err := authorize(ctx); err != nil {
			return err
		}
		args := []string{"-N", "-f", "-M", "-S", control, "-o", "ControlPersist=yes", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8", "-o", "UserKnownHostsFile=" + filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), runtimeID + "@ssh.runta.com"}
		err := exec.CommandContext(ctx, "/usr/bin/ssh", args...).Run()
		if ctx.Err() != nil {
			manager.stop()
			return ctx.Err()
		}
		return err
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
			manager.Get(manager.ctx)
			select {
			case <-manager.ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return done
}

func (manager *SharedConnection) Get(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
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
