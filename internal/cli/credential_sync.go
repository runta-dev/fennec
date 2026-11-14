package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fennec/internal/launchprofile"
	r "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Live native credential synchronization (see setup/native_credential_sync.go).
// The Mac and the cloud share one OAuth refresh grant per agent; whichever
// side refreshes first revokes the other's copy. The daemon therefore pushes
// every Mac refresh to the cloud within about a second and pulls every cloud
// refresh back, between the full syncs that preparation runs.
//
// Both directions run over the resident channel only: one native_credential_set
// operation per changed file, and one long-lived native_credential_watch. When
// the channel is down, changed names stay queued and are pushed once it is
// back, and the watch reports what changed meanwhile. Nothing here blocks
// preparation, sessions or the UI, and nothing prompts. Diagnostics carry
// event names and agents only, never credential values.

type credentialLink interface {
	Push(context.Context, setup.NativeCredentialPush) (setup.NativeCredentialPushResult, error)
	Watch(context.Context, setup.NativeCredentialWatch) (setup.NativeCredentialChanges, error)
}

type credentialMac interface {
	Gather(context.Context, string) ([]byte, error)
	Read(context.Context, string) ([]byte, setup.NativeCredentialLocation, error)
	Write(context.Context, setup.NativeCredentialLocation, []byte, []byte) error
}

type credentialSync struct {
	mac     credentialMac
	link    func() credentialLink // nil while the channel is down or the cloud predates the operations
	changes <-chan []string       // Mac credential files that may have changed
	save    func(setup.NativeCredentialSyncState)
	record  func(event, name string)
	retry   time.Duration
	wait    time.Duration // how long one cloud watch stays open

	mu      sync.Mutex
	state   setup.NativeCredentialSyncState
	pending map[string]bool
	denied  map[string]bool // Keychain items macOS refused to update; never retried
	kick    chan struct{}
	apply   sync.Mutex // one Mac read-decide-write or push at a time
}

func newCredentialSync(mac credentialMac, link func() credentialLink, changes <-chan []string, state setup.NativeCredentialSyncState) *credentialSync {
	if state.Mac == nil {
		state.Mac = map[string]string{}
	}
	if state.Cloud == nil {
		state.Cloud = map[string]string{}
	}
	return &credentialSync{mac: mac, link: link, changes: changes, state: state, pending: map[string]bool{}, denied: map[string]bool{}, kick: make(chan struct{}, 1), retry: 2 * time.Second, wait: 10 * time.Minute, save: func(setup.NativeCredentialSyncState) {}, record: func(string, string) {}}
}

func (c *credentialSync) run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); c.pushLoop(ctx) }()
	go func() { defer workers.Done(); c.pullLoop(ctx) }()
	workers.Wait()
}

func (c *credentialSync) queue(names ...string) {
	c.mu.Lock()
	for _, name := range names {
		if setup.NativeCredentialAgent(name) != "" {
			c.pending[name] = true
		}
	}
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *credentialSync) pushLoop(ctx context.Context) {
	ticker := time.NewTicker(c.retry)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case names, ok := <-c.changes:
			if !ok {
				c.changes = nil
			}
			c.queue(names...)
			continue
		case <-c.kick:
		case <-ticker.C:
		}
		c.flush(ctx)
	}
}

// flush pushes every queued file. A file stays queued while the channel is
// down or its push fails, and is pushed again (gathered afresh) later.
func (c *credentialSync) flush(ctx context.Context) {
	c.mu.Lock()
	names := make([]string, 0, len(c.pending))
	for name := range c.pending {
		names = append(names, name)
	}
	c.mu.Unlock()
	if len(names) == 0 {
		return
	}
	link := c.link()
	if link == nil {
		return
	}
	sort.Strings(names)
	for _, name := range names {
		if err := c.push(ctx, link, name); err != nil {
			c.record("push_failed", name)
			return
		}
		c.mu.Lock()
		delete(c.pending, name)
		c.mu.Unlock()
	}
}

func (c *credentialSync) push(ctx context.Context, link credentialLink, name string) error {
	c.apply.Lock()
	defer c.apply.Unlock()
	data, err := c.mac.Gather(ctx, name)
	if err != nil {
		// Unreadable now (Keychain access not granted, invalid file): the next
		// change or preparation sync tries again. Never a prompt or a retry loop.
		c.record("push_unavailable", name)
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	digest := setup.NativeCredentialDigest(data)
	c.mu.Lock()
	unchanged := c.state.Mac[name] == digest
	c.mu.Unlock()
	if unchanged {
		return nil
	}
	started := time.Now()
	result, err := link.Push(ctx, setup.NativeCredentialPush{Name: name, Data: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return err
	}
	launchprofile.Record("credential_sync.push."+setup.NativeCredentialAgent(name), started, time.Since(started))
	switch {
	case result.Deferred:
		// Changed on both computers and neither copy confirmed: each keeps its
		// own and the next preparation sync decides (same rule as 68cd158).
		c.record("push_deferred", name)
		return nil
	case result.Skipped:
		c.record("push_skipped", name)
	case result.Written:
		c.record("push_written", name)
	}
	c.mu.Lock()
	c.state.Mac[name] = digest
	if result.Remote != "" {
		c.state.Cloud[name] = result.Remote
	}
	state := c.snapshotLocked()
	c.mu.Unlock()
	c.save(state)
	return nil
}

func (c *credentialSync) pullLoop(ctx context.Context) {
	backoff := c.retry
	for ctx.Err() == nil {
		link := c.link()
		if link == nil {
			if !sleepContext(ctx, c.retry) {
				return
			}
			continue
		}
		c.mu.Lock()
		known := map[string]string{}
		for name, digest := range c.state.Cloud {
			known[name] = digest
		}
		c.mu.Unlock()
		watch, cancel := context.WithTimeout(ctx, c.wait+time.Minute)
		changes, err := link.Watch(watch, setup.NativeCredentialWatch{Known: known, WaitMS: int(c.wait / time.Millisecond)})
		cancel()
		if err != nil {
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = c.retry
		names := make([]string, 0, len(changes.Files))
		for name := range changes.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			data, err := base64.StdEncoding.DecodeString(changes.Files[name])
			if err != nil || setup.NativeCredentialAgent(name) == "" {
				continue
			}
			c.pull(ctx, name, data)
		}
		c.mu.Lock()
		for name, digest := range changes.Digests {
			if setup.NativeCredentialAgent(name) != "" && changes.Files[name] == "" {
				c.state.Cloud[name] = digest
			}
		}
		c.mu.Unlock()
	}
}

// pull applies one cloud copy to the Mac with ResolveMacCredential. When the
// cloud copy wins it is written where the Mac agent reads it and then pushed
// back, so the cloud's baseline records that both computers agree.
func (c *credentialSync) pull(ctx context.Context, name string, cloud []byte) {
	c.apply.Lock()
	defer c.apply.Unlock()
	defer func() {
		c.mu.Lock()
		c.state.Cloud[name] = setup.NativeCredentialDigest(cloud)
		state := c.snapshotLocked()
		c.mu.Unlock()
		c.save(state)
	}()
	mac, location, err := c.mac.Read(ctx, name)
	if err != nil {
		if !os.IsNotExist(err) {
			c.record("pull_unavailable", name)
		}
		return // the Mac agent keeps no credential here; never create one
	}
	if location.ReadOnly {
		return
	}
	macDigest := setup.NativeCredentialDigest(mac)
	c.mu.Lock()
	agreed := c.state.Mac[name]
	denied := c.denied[location.Service+"\x00"+location.Account+"\x00"+location.File]
	c.mu.Unlock()
	update, ok := setup.ResolveMacCredential(ctx, name, mac, cloud, agreed != "" && agreed == macDigest)
	if !ok {
		if agreed != macDigest {
			c.queue(name) // the Mac copy is the one to keep; make sure the cloud has it
		}
		return
	}
	if denied {
		return
	}
	started := time.Now()
	if err := c.mac.Write(ctx, location, mac, update); err != nil {
		switch {
		case errors.Is(err, setup.ErrNativeCredentialChanged):
			c.record("pull_raced", name)
			c.queue(name)
		case errors.Is(err, setup.ErrNativeKeychainDenied):
			c.record("pull_denied", name)
			c.mu.Lock()
			c.denied[location.Service+"\x00"+location.Account+"\x00"+location.File] = true
			c.mu.Unlock()
		default:
			c.record("pull_failed", name)
		}
		return
	}
	launchprofile.Record("credential_sync.pull."+setup.NativeCredentialAgent(name), started, time.Since(started))
	c.record("pull_written", name)
	c.mu.Lock()
	delete(c.state.Mac, name)
	c.mu.Unlock()
	c.queue(name)
}

func (c *credentialSync) snapshotLocked() setup.NativeCredentialSyncState {
	state := setup.NativeCredentialSyncState{Scope: c.state.Scope, Mac: map[string]string{}, Cloud: map[string]string{}}
	for name, digest := range c.state.Mac {
		state.Mac[name] = digest
	}
	for name, digest := range c.state.Cloud {
		state.Cloud[name] = digest
	}
	return state
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// credentialStamps is the Mac change detector's view of one location.
type credentialStamps interface {
	Locations([]string) []setup.NativeCredentialLocation
	WatchPaths([]setup.NativeCredentialLocation) []string
	Modified(context.Context, setup.NativeCredentialLocation) string
}

// detectMacCredentialChanges reports credential files whose stamp changed.
// File changes arrive through kqueue; Keychain items are checked by their
// modification date, a read of attributes only, whenever the login keychain
// database changes and every poll. No secret is read here.
func detectMacCredentialChanges(ctx context.Context, store credentialStamps, agents []string, settleFor, poll time.Duration, watch func(context.Context, []string, time.Duration) <-chan struct{}) <-chan []string {
	out := make(chan []string, 4)
	locations := store.Locations(agents)
	events := watch(ctx, store.WatchPaths(locations), poll)
	stamps := map[string]string{}
	changedAt := map[string]time.Time{}
	for _, location := range locations {
		stamps[location.Name] = store.Modified(ctx, location)
	}
	go func() {
		defer close(out)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-events:
				settleEvents(ctx, events, settleFor)
			case <-ticker.C:
			}
			var changed []string
			now := time.Now()
			for _, location := range store.Locations(agents) {
				stamp := store.Modified(ctx, location)
				// A Keychain modification date has one-second resolution, so a
				// second write within that second keeps the stamp; recheck
				// recently changed items once more.
				if stamp != stamps[location.Name] || now.Sub(changedAt[location.Name]) < 2*time.Second {
					if stamp != stamps[location.Name] {
						changedAt[location.Name] = now
					}
					stamps[location.Name] = stamp
					changed = append(changed, location.Name)
				}
			}
			if len(changed) > 0 {
				select {
				case out <- changed:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func settleEvents(ctx context.Context, events <-chan struct{}, quiet time.Duration) {
	limit := time.NewTimer(8 * quiet)
	defer limit.Stop()
	for {
		timer := time.NewTimer(quiet)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-limit.C:
			timer.Stop()
			return
		case <-events:
			timer.Stop()
		case <-timer.C:
			return
		}
	}
}

type transportCredentialLink struct{ transport *terminalstream.Transport }

func (l transportCredentialLink) Push(ctx context.Context, push setup.NativeCredentialPush) (setup.NativeCredentialPushResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := l.transport.Operate(ctx, r.LocalOperation{Kind: "native_credential_set", Credential: &push})
	if err != nil {
		return setup.NativeCredentialPushResult{}, err
	}
	var result setup.NativeCredentialPushResult
	if json.Unmarshal(raw, &result) != nil || result.Version != 1 {
		return result, fmt.Errorf("invalid credential push result")
	}
	return result, nil
}

func (l transportCredentialLink) Watch(ctx context.Context, watch setup.NativeCredentialWatch) (setup.NativeCredentialChanges, error) {
	raw, err := l.transport.Operate(ctx, r.LocalOperation{Kind: "native_credential_watch", CredentialWatch: &watch})
	if err != nil {
		return setup.NativeCredentialChanges{}, err
	}
	var changes setup.NativeCredentialChanges
	if json.Unmarshal(raw, &changes) != nil || changes.Version != 1 {
		return changes, fmt.Errorf("invalid credential watch result")
	}
	return changes, nil
}

// credentialSyncAgents are the agents preparation synchronizes natively.
var credentialSyncAgents = []string{"claude", "codex", "pi", "kimi", "cursor", "grok"}

// runCredentialSync starts once the runtime is known and runs for the epoch.
// It only uses the resident channel after preparation has made the shared
// services ready, so it never races the first full sync.
func (s *daemonServices) runCredentialSync(ctx context.Context, epoch uint64) {
	var user, runtimeID string
	for {
		s.mu.Lock()
		current := s.epoch
		user, runtimeID = s.user, s.config.RuntimeID
		s.mu.Unlock()
		if current != epoch {
			return
		}
		if runtimeID != "" {
			break
		}
		if !sleepContext(ctx, 2*time.Second) {
			return
		}
	}
	store := setup.NewNativeCredentialStore(ctx, s.home)
	link := func() credentialLink {
		s.mu.Lock()
		ready, channel, current := s.brokerReady, s.channel, s.epoch
		s.mu.Unlock()
		if !ready || channel == nil || current != epoch {
			return nil
		}
		transport := channel.peekAlive()
		if transport == nil || !transport.Supports("native_credential_set") || !transport.Supports("native_credential_watch") {
			return nil
		}
		return transportCredentialLink{transport: transport}
	}
	changes := detectMacCredentialChanges(ctx, store, credentialSyncAgents, 250*time.Millisecond, time.Minute, setup.WatchFiles)
	scope := user + "/" + runtimeID
	syncer := newCredentialSync(store, link, changes, setup.LoadNativeCredentialSyncState(s.home, scope))
	syncer.save = func(state setup.NativeCredentialSyncState) { _ = setup.SaveNativeCredentialSyncState(s.home, state) }
	syncer.record = func(event, name string) {
		launchprofile.Record("credential_sync."+event+"."+setup.NativeCredentialAgent(name), time.Now(), 0)
	}
	syncer.run(ctx)
}
