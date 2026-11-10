package cli

import (
	"context"
	"fennec/internal/agent"
	"fennec/internal/launcher"
	"os"
	"path/filepath"
	"time"
)

func installManagedEntry(ctx context.Context, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return installManagedEntryAt(ctx, executable, id)
}
func installManagedEntryAt(ctx context.Context, executable, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	entry, err := agent.EntryPath(id)
	if err != nil {
		return "new_shells", err
	}
	if err := launcher.ReconcileEntry(entry, executable, id); err != nil {
		return "new_shells", err
	}
	// Verify the managed entry still resolves to an executable original rather
	// than deriving readiness from whether a startup event happened to arrive.
	if _, err := launcher.OriginalEntry(entry); err != nil {
		return "new_shells", err
	}
	return "entry_and_new_shells", nil
}
func (s *daemonServices) queueEntryLocked(id string) {
	if s.entryQueue == nil || s.entryPending[id] || s.coverage[id] == "entry_and_new_shells" {
		return
	}
	select {
	case s.entryQueue <- id:
		s.entryPending[id] = true
	default:
	}
}
func (s *daemonServices) runEntryRepair(ctx context.Context, epoch uint64) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.entryQueue:
			s.mu.Lock()
			selected := s.selected[id] && s.epoch == epoch
			s.mu.Unlock()
			if !selected {
				s.mu.Lock()
				delete(s.entryPending, id)
				s.mu.Unlock()
				continue
			}
			coverage, err := s.entryInstaller(ctx, id)
			s.mu.Lock()
			if s.epoch == epoch && ctx.Err() == nil {
				s.coverage[id] = coverage
				delete(s.entryPending, id)
				diagnostic := map[string]any{"agent": id, "coverage": coverage}
				if err != nil {
					diagnostic["error"] = err.Error()
				}
				_ = writePublicJSON(filepath.Join(s.home, ".config/fennec/diagnostics/entry-"+id+".json"), diagnostic)
			}
			s.mu.Unlock()
		case <-ticker.C:
			s.mu.Lock()
			for id := range s.selected {
				if !s.entryPending[id] {
					select {
					case s.entryQueue <- id:
						s.entryPending[id] = true
					default:
					}
				}
			}
			s.mu.Unlock()
		}
	}
}
