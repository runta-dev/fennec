package cli

import (
	"context"
	"fmt"
	"time"
)

// quiesceCloud closes this account's resident services without removing login,
// preferences, the app, or remote session data. The caller first drains prepare.
func (s *daemonServices) quiesceCloud(ctx context.Context, owner, runtimeID string, epoch uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for !s.initializeMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	if err := ctx.Err(); err != nil {
		s.initializeMu.Unlock()
		return err
	}
	s.mu.Lock()
	if s.ctx == nil {
		s.mu.Unlock()
		s.initializeMu.Unlock()
		return nil
	}
	if s.user != owner || s.epoch != epoch || (s.config.RuntimeID != "" && s.config.RuntimeID != runtimeID) {
		s.mu.Unlock()
		s.initializeMu.Unlock()
		return fmt.Errorf("cloud service identity changed")
	}
	cancel := s.cancel
	s.brokerReady = false
	s.ready = map[string]bool{}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		// Keep initialize excluded even when the caller times out, until every old
		// worker has stopped. No replacement worker may join this WaitGroup early.
		s.workers.Wait()
		s.mu.Lock()
		s.ctx = nil
		s.cancel = nil
		s.channel = nil
		s.brokerRunning = false
		s.brokerReady = false
		s.connectorReady = false
		s.connectorAddress = ""
		s.connectorInput = nil
		s.config.PreparedSSH = nil
		s.config.PreparedTerminal = nil
		s.config.PreparedRuntimeID = ""
		s.mu.Unlock()
		s.initializeMu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *daemonServices) validateCloudIdentity(ctx context.Context, owner, runtimeID string, epoch uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == "" || runtimeID == "" {
		return fmt.Errorf("cloud service identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil && (s.user != owner || s.epoch != epoch || (s.config.RuntimeID != "" && s.config.RuntimeID != runtimeID)) {
		return fmt.Errorf("cloud service identity changed")
	}
	return nil
}
