package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const claudeName = "claude/.credentials.json"

func claudeGrant(access, refresh string, expires int64) []byte {
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh, "expiresAt": expires}, "mcpOAuth": map[string]any{"server": "mac-only"}})
	return b
}

type fakeMacStore struct {
	mu      sync.Mutex
	data    map[string][]byte
	gathers int
	writes  int
	err     error // returned by Write
}

func (m *fakeMacStore) Gather(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gathers++
	return append([]byte(nil), m.data[name]...), nil
}

func (m *fakeMacStore) Read(_ context.Context, name string) ([]byte, setup.NativeCredentialLocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.data[name]
	if !ok {
		return nil, setup.NativeCredentialLocation{}, os.ErrNotExist
	}
	return append([]byte(nil), data...), setup.NativeCredentialLocation{Name: name, Service: "Claude Code-credentials", Account: "tester"}, nil
}

func (m *fakeMacStore) Write(_ context.Context, _ setup.NativeCredentialLocation, expected, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		m.writes++
		return m.err
	}
	for name, current := range m.data {
		if bytes.Equal(current, expected) {
			m.data[name] = append([]byte(nil), data...)
			m.writes++
			return nil
		}
	}
	return setup.ErrNativeCredentialChanged
}

func (m *fakeMacStore) set(name string, data []byte) {
	m.mu.Lock()
	m.data[name] = data
	m.mu.Unlock()
}

func (m *fakeMacStore) writeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes
}

func (m *fakeMacStore) get(name string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[name]
}

type fakeCloudLink struct {
	mu      sync.Mutex
	pushes  []setup.NativeCredentialPush
	watches []setup.NativeCredentialWatch
	answers chan setup.NativeCredentialChanges
	result  setup.NativeCredentialPushResult
}

func (l *fakeCloudLink) Push(_ context.Context, push setup.NativeCredentialPush) (setup.NativeCredentialPushResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pushes = append(l.pushes, push)
	result := l.result
	if result.Version == 0 {
		data, _ := base64.StdEncoding.DecodeString(push.Data)
		result = setup.NativeCredentialPushResult{Version: 1, Written: true, Remote: setup.NativeCredentialDigest(data)}
	}
	return result, nil
}

func (l *fakeCloudLink) Watch(ctx context.Context, watch setup.NativeCredentialWatch) (setup.NativeCredentialChanges, error) {
	l.mu.Lock()
	l.watches = append(l.watches, watch)
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return setup.NativeCredentialChanges{}, ctx.Err()
	case answer := <-l.answers:
		return answer, nil
	}
}

func (l *fakeCloudLink) pushed() []setup.NativeCredentialPush {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]setup.NativeCredentialPush(nil), l.pushes...)
}

func (l *fakeCloudLink) lastWatch() setup.NativeCredentialWatch {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.watches) == 0 {
		return setup.NativeCredentialWatch{}
	}
	return l.watches[len(l.watches)-1]
}

type syncHarness struct {
	mac     *fakeMacStore
	cloud   *fakeCloudLink
	sync    *credentialSync
	changes chan []string
	up      chan bool
	events  *eventLog
	saved   *eventLog
	cancel  context.CancelFunc
}

type eventLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *eventLog) add(line string) { l.mu.Lock(); l.lines = append(l.lines, line); l.mu.Unlock() }
func (l *eventLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func startSyncHarness(t *testing.T, mac map[string][]byte, state setup.NativeCredentialSyncState, online bool) *syncHarness {
	t.Helper()
	h := &syncHarness{mac: &fakeMacStore{data: mac}, cloud: &fakeCloudLink{answers: make(chan setup.NativeCredentialChanges, 4)}, changes: make(chan []string, 4), events: &eventLog{}, saved: &eventLog{}}
	var mu sync.Mutex
	connected := online
	h.up = make(chan bool)
	go func() {
		for value := range h.up {
			mu.Lock()
			connected = value
			mu.Unlock()
		}
	}()
	link := func() credentialLink {
		mu.Lock()
		defer mu.Unlock()
		if !connected {
			return nil
		}
		return h.cloud
	}
	h.sync = newCredentialSync(h.mac, link, h.changes, state)
	h.sync.retry = 20 * time.Millisecond
	h.sync.record = func(event, name string) { h.events.add(event + " " + name) }
	h.sync.save = func(state setup.NativeCredentialSyncState) { b, _ := json.Marshal(state); h.saved.add(string(b)) }
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	done := make(chan struct{})
	go func() { defer close(done); h.sync.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; close(h.up) })
	return h
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCredentialSyncPushesMacRefreshOnceAndSkipsNoOps(t *testing.T) {
	first := claudeGrant("a1", "r1", 1)
	h := startSyncHarness(t, map[string][]byte{claudeName: first}, setup.NativeCredentialSyncState{}, true)
	h.changes <- []string{claudeName}
	eventually(t, "push", func() bool { return len(h.cloud.pushed()) == 1 })
	if got := h.cloud.pushed()[0]; got.Name != claudeName || got.Data != base64.StdEncoding.EncodeToString(first) {
		t.Fatalf("pushed %+v", got.Name)
	}
	// The same copy again (a Keychain write that left the item unchanged).
	h.changes <- []string{claudeName}
	time.Sleep(100 * time.Millisecond)
	if n := len(h.cloud.pushed()); n != 1 {
		t.Fatalf("identical copy pushed %d times", n)
	}
	// The cloud's digest of the written copy becomes the watch's known copy,
	// so the echo of our own push is never pulled back.
	eventually(t, "known digest", func() bool {
		h.cloud.answers <- setup.NativeCredentialChanges{Version: 1}
		return h.cloud.lastWatch().Known[claudeName] == setup.NativeCredentialDigest(first)
	})
	second := claudeGrant("a2", "r2", 2)
	h.mac.set(claudeName, second)
	h.changes <- []string{claudeName}
	eventually(t, "second push", func() bool { return len(h.cloud.pushed()) == 2 })
}

func TestCredentialSyncQueuesWhileTheChannelIsDown(t *testing.T) {
	h := startSyncHarness(t, map[string][]byte{claudeName: claudeGrant("a1", "r1", 1)}, setup.NativeCredentialSyncState{}, false)
	h.changes <- []string{claudeName}
	time.Sleep(80 * time.Millisecond)
	latest := claudeGrant("a2", "r2", 2)
	h.mac.set(claudeName, latest)
	h.changes <- []string{claudeName}
	time.Sleep(80 * time.Millisecond)
	if n := len(h.cloud.pushed()); n != 0 {
		t.Fatalf("pushed %d while down", n)
	}
	h.up <- true
	eventually(t, "push after reconnect", func() bool { return len(h.cloud.pushed()) >= 1 })
	time.Sleep(60 * time.Millisecond)
	pushes := h.cloud.pushed()
	if len(pushes) != 1 || pushes[0].Data != base64.StdEncoding.EncodeToString(latest) {
		t.Fatalf("after reconnect pushed %d copies; want only the latest", len(pushes))
	}
}

func TestCredentialSyncPullsCloudRefreshAndPushesTheAgreement(t *testing.T) {
	mac := claudeGrant("a1", "r1", 1)
	state := setup.NativeCredentialSyncState{Mac: map[string]string{claudeName: setup.NativeCredentialDigest(mac)}, Cloud: map[string]string{claudeName: setup.NativeCredentialDigest(mac)}}
	h := startSyncHarness(t, map[string][]byte{claudeName: mac}, state, true)
	eventually(t, "watch", func() bool { return h.cloud.lastWatch().Known[claudeName] != "" })
	if h.cloud.lastWatch().WaitMS <= 0 {
		t.Fatal("watch must bound its wait")
	}
	cloud, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "a2", "refreshToken": "r2", "expiresAt": 2}})
	h.cloud.answers <- setup.NativeCredentialChanges{Version: 1, Files: map[string]string{claudeName: base64.StdEncoding.EncodeToString(cloud)}, Digests: map[string]string{claudeName: setup.NativeCredentialDigest(cloud)}}
	eventually(t, "mac write", func() bool { return h.mac.writeCount() == 1 })
	var got map[string]json.RawMessage
	json.Unmarshal(h.mac.get(claudeName), &got)
	if !strings.Contains(string(got["claudeAiOauth"]), `"a2"`) || string(got["mcpOAuth"]) != `{"server":"mac-only"}` {
		t.Fatalf("mac copy %s", h.mac.get(claudeName))
	}
	// The written copy goes back so the cloud's baseline records the agreement.
	eventually(t, "push back", func() bool { return len(h.cloud.pushed()) == 1 })
	if data, _ := base64.StdEncoding.DecodeString(h.cloud.pushed()[0].Data); !bytes.Equal(data, h.mac.get(claudeName)) {
		t.Fatal("pushed copy differs from the written Mac copy")
	}
	eventually(t, "next watch knows the cloud copy", func() bool {
		return h.cloud.lastWatch().Known[claudeName] == setup.NativeCredentialDigest(cloud) || h.cloud.lastWatch().Known[claudeName] == setup.NativeCredentialDigest(h.mac.get(claudeName))
	})
}

func TestCredentialSyncKeepsANewerMacCopyAndPushesIt(t *testing.T) {
	agreed := claudeGrant("a0", "r-same", 1)
	mac := claudeGrant("a-mac", "r-same", 3_000) // refreshed on the Mac too, later expiry
	state := setup.NativeCredentialSyncState{Mac: map[string]string{claudeName: setup.NativeCredentialDigest(agreed)}, Cloud: map[string]string{}}
	h := startSyncHarness(t, map[string][]byte{claudeName: mac}, state, true)
	cloud := claudeGrant("a-cloud", "r-same", 2_000)
	h.cloud.answers <- setup.NativeCredentialChanges{Version: 1, Files: map[string]string{claudeName: base64.StdEncoding.EncodeToString(cloud)}}
	eventually(t, "mac copy pushed", func() bool { return len(h.cloud.pushed()) == 1 })
	if h.mac.writeCount() != 0 || !bytes.Equal(h.mac.get(claudeName), mac) {
		t.Fatal("an older cloud copy replaced the Mac's later expiry")
	}
}

func TestCredentialSyncNeverRetriesADeniedKeychainUpdate(t *testing.T) {
	mac := claudeGrant("a1", "r1", 1)
	state := setup.NativeCredentialSyncState{Mac: map[string]string{claudeName: setup.NativeCredentialDigest(mac)}, Cloud: map[string]string{}}
	h := startSyncHarness(t, map[string][]byte{claudeName: mac}, state, true)
	h.mac.mu.Lock()
	h.mac.err = setup.ErrNativeKeychainDenied
	h.mac.mu.Unlock()
	for i, access := range []string{"a2", "a3"} {
		cloud := claudeGrant(access, "r"+access, int64(10+i))
		h.cloud.answers <- setup.NativeCredentialChanges{Version: 1, Files: map[string]string{claudeName: base64.StdEncoding.EncodeToString(cloud)}}
		time.Sleep(60 * time.Millisecond)
	}
	h.mac.mu.Lock()
	writes := h.mac.writes
	h.mac.mu.Unlock()
	if writes != 1 || !strings.Contains(h.events.all(), "pull_denied "+claudeName) {
		t.Fatalf("%d Keychain write attempts, events %q", writes, h.events.all())
	}
}

func TestCredentialSyncDiagnosticsAndStateCarryNoSecrets(t *testing.T) {
	secret := claudeGrant("a-SECRET-ACCESS", "r-SECRET-REFRESH", 1)
	h := startSyncHarness(t, map[string][]byte{claudeName: secret}, setup.NativeCredentialSyncState{Scope: "user/runtime"}, true)
	h.changes <- []string{claudeName}
	cloud := claudeGrant("a-SECRET-CLOUD", "r-SECRET-CLOUD", 2)
	h.cloud.answers <- setup.NativeCredentialChanges{Version: 1, Files: map[string]string{claudeName: base64.StdEncoding.EncodeToString(cloud)}}
	eventually(t, "push", func() bool { return len(h.cloud.pushed()) >= 1 })
	time.Sleep(100 * time.Millisecond)
	for label, text := range map[string]string{"events": h.events.all(), "state": h.saved.all()} {
		if strings.Contains(text, "SECRET") || strings.Contains(text, base64.StdEncoding.EncodeToString(secret)[:16]) {
			t.Fatalf("%s carries credential material: %s", label, text)
		}
	}
	if h.saved.all() == "" {
		t.Fatal("state never saved")
	}
}

type fakeStamps struct {
	mu     sync.Mutex
	stamps map[string]string
	checks int
}

func (f *fakeStamps) Locations(agents []string) []setup.NativeCredentialLocation {
	var out []setup.NativeCredentialLocation
	for _, agent := range agents {
		out = append(out, setup.NativeCredentialLocation{Name: map[string]string{"claude": claudeName, "codex": "codex/auth.json"}[agent]})
	}
	return out
}
func (f *fakeStamps) WatchPaths([]setup.NativeCredentialLocation) []string { return []string{"x"} }
func (f *fakeStamps) Modified(_ context.Context, location setup.NativeCredentialLocation) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.stamps[location.Name]
}

func TestDetectorDebouncesBurstsAndReportsOnlyChangedStamps(t *testing.T) {
	stamps := &fakeStamps{stamps: map[string]string{claudeName: "1", "codex/auth.json": "1"}}
	events := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := detectMacCredentialChanges(ctx, stamps, []string{"claude", "codex"}, 30*time.Millisecond, time.Hour, func(context.Context, []string, time.Duration) <-chan struct{} { return events })
	stamps.mu.Lock()
	stamps.stamps["codex/auth.json"] = "2"
	stamps.mu.Unlock()
	started := time.Now()
	for i := 0; i < 5; i++ { // a burst of writes
		select {
		case events <- struct{}{}:
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case names := <-out:
		if len(names) != 1 || names[0] != "codex/auth.json" {
			t.Fatalf("reported %v", names)
		}
		if elapsed := time.Since(started); elapsed < 30*time.Millisecond {
			t.Fatalf("reported before the burst settled (%v)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("change not reported")
	}
	select {
	case names := <-out:
		t.Fatalf("burst reported twice: %v", names)
	case <-time.After(100 * time.Millisecond):
	}
}

// delayedPipe delivers each write after a fixed one-way latency, in order,
// like the WAN link between the Mac and the cloud.
func delayedPipe(delay time.Duration) (io.ReadCloser, io.WriteCloser) {
	reader, writer := io.Pipe()
	if delay == 0 {
		return reader, writer
	}
	type chunk struct {
		data []byte
		at   time.Time
	}
	queue := make(chan chunk, 1024)
	go func() {
		for item := range queue {
			time.Sleep(time.Until(item.at))
			if _, err := writer.Write(item.data); err != nil {
				return
			}
		}
		writer.Close()
	}()
	return reader, &delayWriter{queue: func(data []byte) { queue <- chunk{append([]byte(nil), data...), time.Now().Add(delay)} }, close: func() { close(queue) }}
}

type delayWriter struct {
	mu     sync.Mutex
	closed bool
	queue  func([]byte)
	close  func()
}

func (w *delayWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	w.queue(data)
	return len(data), nil
}

func (w *delayWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		w.close()
	}
	return nil
}

func writeAtomically(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// TestCredentialSyncEndToEndPropagation runs both directions through the real
// pieces: the Mac file watcher and detector, the Mac credential store, the
// resident protocol (client transport and cloud server) over a link with WAN
// latency, and the cloud's apply and watch on its own home directory. It
// reports the propagation time of each sample.
func TestCredentialSyncEndToEndPropagation(t *testing.T) {
	for _, oneWay := range []time.Duration{0, 90 * time.Millisecond} {
		t.Run(fmt.Sprintf("one-way latency %v", oneWay), func(t *testing.T) {
			macHome, cloudHome := t.TempDir(), t.TempDir()
			now := time.Now().Add(4 * time.Hour).UnixMilli()
			grant := func(n int) []byte {
				b, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": fmt.Sprintf("a%d", n), "refreshToken": fmt.Sprintf("r%d", n), "expiresAt": now + int64(n)*1000}})
				return b
			}
			first := grant(0)
			if _, err := setup.ApplyNativeAccounts(context.Background(), cloudHome, setup.ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{claudeName: base64.StdEncoding.EncodeToString(first)}}); err != nil {
				t.Fatal(err)
			}
			macFile := filepath.Join(macHome, ".claude/.credentials.json")
			cloudFile := filepath.Join(cloudHome, ".local/fennec/native", claudeName)
			os.MkdirAll(filepath.Dir(macFile), 0o700)
			os.WriteFile(macFile, first, 0o600)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			toCloud, fromMac := delayedPipe(oneWay)
			toMac, fromCloud := delayedPipe(oneWay)
			runtime := &cloudruntime.LocalRuntime{Home: cloudHome}
			served := make(chan struct{})
			go func() {
				defer close(served)
				terminalstream.ServeWithOperations(ctx, toCloud, fromCloud, nil, nil, nil, runtime.Operation)
			}()
			transport := terminalstream.StartPipes(fromMac, toMac)
			defer func() { cancel(); transport.Close(); <-served }()
			if err := transport.Ready(ctx); err != nil {
				t.Fatal(err)
			}
			store := setup.NativeCredentialStore{Source: setup.NativeSource{Home: macHome, Env: func(k string) string { return map[string]string{"USER": "tester"}[k] }}}
			changes := detectMacCredentialChanges(ctx, store, []string{"claude"}, 250*time.Millisecond, time.Minute, setup.WatchFiles)
			syncer := newCredentialSync(store, func() credentialLink { return transportCredentialLink{transport: transport} }, changes, setup.NativeCredentialSyncState{Scope: "test"})
			var events eventLog
			syncer.record = func(event, name string) { events.add(event) }
			syncDone := make(chan struct{})
			go func() { defer close(syncDone); syncer.run(ctx) }()
			defer func() { cancel(); <-syncDone }()
			eventually(t, "initial watch", func() bool {
				syncer.mu.Lock()
				defer syncer.mu.Unlock()
				return syncer.state.Cloud[claudeName] != ""
			})

			measure := func(label string, write func(), done func() bool) time.Duration {
				started := time.Now()
				write()
				deadline := started.Add(5 * time.Second)
				for !done() {
					if time.Now().After(deadline) {
						t.Fatalf("%s did not propagate; events %q", label, events.all())
					}
					time.Sleep(2 * time.Millisecond)
				}
				return time.Since(started)
			}
			var macToCloud, cloudToMac []time.Duration
			for i := 1; i <= 3; i++ {
				macRefresh := grant(10 * i)
				macToCloud = append(macToCloud, measure("mac refresh", func() { writeAtomically(t, macFile, macRefresh) }, func() bool {
					got, _ := os.ReadFile(cloudFile)
					return bytes.Equal(got, macRefresh)
				}))
				cloudRefresh := grant(10*i + 5)
				cloudToMac = append(cloudToMac, measure("cloud refresh", func() { writeAtomically(t, cloudFile, cloudRefresh) }, func() bool {
					got, _ := os.ReadFile(macFile)
					return bytes.Contains(got, []byte(fmt.Sprintf(`"a%d"`, 10*i+5)))
				}))
				// The pulled copy is pushed back and settles without changing
				// the cloud copy or asking any provider.
				time.Sleep(600*time.Millisecond + 4*oneWay)
				if got, _ := os.ReadFile(cloudFile); !bytes.Equal(got, cloudRefresh) {
					t.Fatalf("cloud refresh overwritten after the pull: %s", got)
				}
			}
			var baseline map[string]struct{ Source, Remote string }
			raw, _ := os.ReadFile(filepath.Join(cloudHome, ".local/fennec/native/.baseline.json"))
			json.Unmarshal(raw, &baseline)
			mac, _ := os.ReadFile(macFile)
			if baseline[claudeName].Source != setup.NativeCredentialDigest(mac) {
				t.Fatalf("cloud baseline does not record the agreed Mac copy; events %q", events.all())
			}
			sort.Slice(macToCloud, func(i, j int) bool { return macToCloud[i] < macToCloud[j] })
			sort.Slice(cloudToMac, func(i, j int) bool { return cloudToMac[i] < cloudToMac[j] })
			t.Logf("Mac→cloud %v, cloud→Mac %v (one-way link latency %v)", macToCloud, cloudToMac, oneWay)
			if macToCloud[len(macToCloud)-1] > 2*time.Second || cloudToMac[len(cloudToMac)-1] > 2*time.Second {
				t.Fatal("propagation slower than two seconds")
			}
			if strings.Contains(events.all(), "denied") || strings.Contains(events.all(), "failed") {
				t.Fatalf("events %q", events.all())
			}
		})
	}
	_ = errors.New
}
