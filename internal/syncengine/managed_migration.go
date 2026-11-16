package syncengine

import (
	"context"
	"fmt"
)

const ManagedTransport = "resident-v1"
const managedResume = "resident-v1-resuming"
const managedPause = "resident-v1-pausing"

type sessionControl interface {
	Inspect(context.Context, string) ([]Session, error)
	SetPaused(context.Context, string, bool) (Session, error)
}

// MigrateManagedSessions reconnects only registrations in this exact private
// daemon/wrapper scope. Paused sessions remain paused. A persisted intent allows
// an interrupted pause/resume to recover without silently leaving an active
// session paused. Explicit user pause/resume shares the same registry lock.
func (m Manager) MigrateManagedSessions(ctx context.Context, registry, executable, owner, runtimeID string) error {
	return m.migrateManagedSessions(ctx, registry, executable, owner, runtimeID, m)
}

func (m Manager) migrateManagedSessions(ctx context.Context, registry, executable, owner, runtimeID string, control sessionControl) error {
	return WithRegistryLock(ctx, registry, func() error {
		entries, err := ListProjects(registry)
		if err != nil {
			return err
		}
		configured := false
		for _, entry := range entries {
			if entry.DataDirectory != m.DataDirectory || entry.SSHDirectory != m.SSHDirectory {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !configured {
				if _, err = ConfigureManagedSSH(m.SSHDirectory, executable, owner, runtimeID); err != nil {
					return err
				}
				configured = true
			}
			if err = migrateManagedSession(ctx, registry, entry, control); err != nil {
				return err
			}
		}
		return nil
	})
}

func migrateManagedSession(ctx context.Context, registry string, entry Registration, control sessionControl) error {
	if entry.Transport == ManagedTransport {
		return nil
	}
	if entry.Transport != "" && entry.Transport != managedResume && entry.Transport != managedPause {
		return fmt.Errorf("unsupported sync transport state")
	}
	sessions, err := control.Inspect(ctx, entry.Session)
	if err != nil {
		return err
	}
	if len(sessions) != 1 || sessions[0].Mode != "two-way-safe" {
		return fmt.Errorf("one safe registered synchronization session required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.Transport == managedPause {
		if _, err = control.SetPaused(ctx, entry.Session, true); err != nil {
			return err
		}
	} else if entry.Transport == managedResume || !sessions[0].Paused {
		entry.Transport = managedResume
		if err = Register(registry, entry); err != nil {
			return err
		}
		if _, err = control.SetPaused(ctx, entry.Session, true); err != nil {
			return err
		}
		if _, err = control.SetPaused(ctx, entry.Session, false); err != nil {
			return err
		}
	}
	entry.Transport = ManagedTransport
	return Register(registry, entry)
}

// EnsureManaged is called under the registry lock after the scoped wrappers
// have been installed. New sessions already use them; existing sessions must
// release their previous SSH connection before being marked managed.
func (m Manager) EnsureManaged(ctx context.Context, registry, user, root, beta string) (Session, error) {
	session, created, err := m.ensure(ctx, user, root, beta)
	if err != nil {
		return Session{}, err
	}
	entry := Registration{Root: session.Alpha.Path, Session: session.Name, DataDirectory: m.DataDirectory, SSHDirectory: m.SSHDirectory}
	if created {
		entry.Transport = ManagedTransport
		return session, Register(registry, entry)
	}
	entries, err := ListProjects(registry)
	if err != nil {
		return Session{}, err
	}
	for _, previous := range entries {
		if previous.DataDirectory == entry.DataDirectory && previous.Session == entry.Session {
			entry.Transport = previous.Transport
			break
		}
	}
	if err = migrateManagedSession(ctx, registry, entry, m); err != nil {
		return Session{}, err
	}
	sessions, err := m.Inspect(ctx, entry.Session)
	if err != nil {
		return Session{}, err
	}
	if len(sessions) != 1 {
		return Session{}, fmt.Errorf("sync session disappeared during migration")
	}
	return sessions[0], nil
}

// SetRegisteredPaused fences explicit user intent against a pending automatic
// migration. Its marker is written before the command, preserving that intent
// even if the command's completion cannot be observed.
func (m Manager) SetRegisteredPaused(ctx context.Context, registry, name string, paused bool) (Session, error) {
	return setRegisteredPaused(ctx, registry, m.DataDirectory, name, paused, m)
}

func setRegisteredPaused(ctx context.Context, registry, state, name string, paused bool, control sessionControl) (Session, error) {
	var result Session
	err := WithRegistryLock(ctx, registry, func() error {
		entries, err := ListProjects(registry)
		if err != nil {
			return err
		}
		var pending *Registration
		for _, entry := range entries {
			if entry.DataDirectory == state && entry.Session == name && (entry.Transport == managedResume || entry.Transport == managedPause) {
				entry.Transport = managedResume
				if paused {
					entry.Transport = managedPause
				}
				if err = Register(registry, entry); err != nil {
					return err
				}
				pending = &entry
				break
			}
		}
		result, err = control.SetPaused(ctx, name, paused)
		if err != nil {
			return err
		}
		if pending != nil {
			pending.Transport = ManagedTransport
			return Register(registry, *pending)
		}
		return nil
	})
	return result, err
}
