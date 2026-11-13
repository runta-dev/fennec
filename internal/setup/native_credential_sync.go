package setup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Live credential synchronization keeps the Mac and cloud copies of a shared
// OAuth refresh grant in step between full preparation syncs. Whichever
// computer refreshes first rotates the grant; the other copy is revoked from
// that moment, so the new copy has to reach the other computer before that
// computer's agent tries to refresh.
//
//   - Mac to cloud: the Mac daemon notices a changed Keychain item or auth
//     file and sends that one file with native_credential_set. The cloud
//     applies it exactly like a preparation sync applies the same file
//     (reconcileNativeFile, same baseline, same lock, same conflict rules).
//   - Cloud to Mac: the Mac daemon keeps one native_credential_watch request
//     open. The cloud answers when a credential file differs from the copy the
//     Mac last saw, and the Mac applies ResolveMacCredential.
//
// Credential values travel only inside resident-channel frames; results,
// errors, logs and diagnostics carry names and SHA-256 digests only.

// nativeCredentialFiles maps each credential file that carries a refresh grant
// to its agent.
var nativeCredentialFiles = map[string]string{
	"claude/.credentials.json":         "claude",
	"codex/auth.json":                  "codex",
	"pi/auth.json":                     "pi",
	"opencode/data/opencode/auth.json": "opencode",
}

// NativeCredentialNames lists the credential files in a stable order.
func NativeCredentialNames() []string {
	names := make([]string, 0, len(nativeCredentialFiles))
	for name := range nativeCredentialFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NativeCredentialAgent returns the agent a credential file belongs to, or "".
func NativeCredentialAgent(name string) string { return nativeCredentialFiles[name] }

// NativeCredentialDigest is the digest both computers use to name a copy.
func NativeCredentialDigest(data []byte) string { return credentialHash(data) }

const maxNativeCredentialBytes = 256 << 10

var nativeDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// NativeCredentialPush carries one Mac copy to the cloud.
type NativeCredentialPush struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64
}

func (p NativeCredentialPush) Validate() error {
	if nativeCredentialFiles[p.Name] == "" {
		return fmt.Errorf("unsupported native credential file")
	}
	if p.Data == "" || len(p.Data) > maxNativeCredentialBytes*4/3+4 {
		return fmt.Errorf("invalid native credential envelope")
	}
	return nil
}

// NativeCredentialPushResult never contains the credential itself.
type NativeCredentialPushResult struct {
	Version  int    `json:"version"`
	Written  bool   `json:"written,omitempty"`
	Deferred bool   `json:"deferred,omitempty"`
	Skipped  bool   `json:"skipped,omitempty"` // the cloud agent has its own cloud sign-in
	Remote   string `json:"remote,omitempty"`  // digest of the cloud copy afterwards
}

// ApplyNativeCredential applies one Mac copy under the shared native lock with
// the same decision and baseline as a preparation sync, without touching any
// other file. A cloud agent that refreshes during the call wins the race: the
// write is dropped and the result is deferred, so the watch reports the
// cloud's new copy to the Mac instead.
func ApplyNativeCredential(ctx context.Context, home string, push NativeCredentialPush) (NativeCredentialPushResult, error) {
	result := NativeCredentialPushResult{Version: 1}
	if err := push.Validate(); err != nil {
		return result, err
	}
	root, unlock, err := lockNativeRoot(ctx, home)
	if err != nil {
		return result, err
	}
	defer unlock()
	logins, err := readCloudLogins(home)
	if err != nil {
		return result, err
	}
	if logins[nativeCredentialFiles[push.Name]] != "" {
		result.Skipped = true
		return result, nil
	}
	baseline, err := readNativeBaseline(root)
	if err != nil {
		return result, err
	}
	before := baseline[push.Name]
	write, deferred, err := reconcileNativeFile(ctx, home, root, push.Name, push.Data, map[string]string{push.Name: push.Data}, baseline)
	if err != nil {
		return result, err
	}
	if deferred {
		result.Deferred = true
		return result, nil
	}
	if write != nil {
		if err := atomicCredential(write.path, write.data, *write); err != nil {
			if errors.Is(err, errCredentialChanged) {
				result.Deferred = true
				return result, nil
			}
			return result, err
		}
		result.Written = true
	}
	if baseline[push.Name] != before {
		if err := writeNativeBaseline(root, baseline); err != nil {
			return result, err
		}
	}
	if current, err := readNativeFile(filepath.Join(root, filepath.FromSlash(push.Name))); err == nil {
		result.Remote = credentialHash(current)
	}
	return result, nil
}

// NativeCredentialWatch asks the cloud to answer once any credential file
// differs from the digest the Mac last saw ("" or missing: never seen).
type NativeCredentialWatch struct {
	Known  map[string]string `json:"known,omitempty"`
	WaitMS int               `json:"wait_ms,omitempty"`
}

func (w NativeCredentialWatch) Validate() error {
	if len(w.Known) > len(nativeCredentialFiles) || w.WaitMS < 0 || w.WaitMS > int(maxNativeCredentialWait/time.Millisecond) {
		return fmt.Errorf("invalid native credential watch")
	}
	for name, digest := range w.Known {
		if nativeCredentialFiles[name] == "" || (digest != "" && !nativeDigestPattern.MatchString(digest)) {
			return fmt.Errorf("invalid native credential watch")
		}
	}
	return nil
}

// NativeCredentialChanges lists cloud copies the Mac has not seen (Files, base64)
// and the digest of every cloud credential file present (Digests).
type NativeCredentialChanges struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files,omitempty"`
	Digests map[string]string `json:"digests,omitempty"`
}

const (
	defaultNativeCredentialWait = 10 * time.Minute
	maxNativeCredentialWait     = 30 * time.Minute
)

// nativeCredentialSettle lets an agent finish a burst of writes before its
// file is read; nativeCredentialCloudPoll backs up the kernel notification.
var (
	nativeCredentialSettle    = 150 * time.Millisecond
	nativeCredentialCloudPoll = 15 * time.Second
)

// WatchNativeCredentials blocks until a cloud credential differs from the
// Mac's digest, then returns it; it returns no files when the wait ends. The
// watch is established before the first comparison, so a change made while
// the Mac was disconnected is answered at once and none is missed.
func WatchNativeCredentials(ctx context.Context, home string, request NativeCredentialWatch) (NativeCredentialChanges, error) {
	if err := request.Validate(); err != nil {
		return NativeCredentialChanges{}, err
	}
	wait := defaultNativeCredentialWait
	if request.WaitMS > 0 {
		wait = time.Duration(request.WaitMS) * time.Millisecond
	}
	root := filepath.Join(home, ".local/fennec/native")
	var paths []string
	for _, name := range NativeCredentialNames() {
		paths = append(paths, filepath.Join(root, filepath.FromSlash(name)))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := WatchFiles(ctx, paths, nativeCredentialCloudPoll)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		changes, err := nativeCredentialChanges(home, request.Known)
		if err != nil || len(changes.Files) > 0 {
			return changes, err
		}
		select {
		case <-ctx.Done():
			return NativeCredentialChanges{}, ctx.Err()
		case <-deadline.C:
			return changes, nil
		case <-events:
			settle(ctx, events, nativeCredentialSettle)
		}
	}
}

// settle waits until a burst of change notifications has been quiet for
// quiet, but never longer than eight quiet periods in all.
func settle(ctx context.Context, events <-chan struct{}, quiet time.Duration) {
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

func nativeCredentialChanges(home string, known map[string]string) (NativeCredentialChanges, error) {
	changes := NativeCredentialChanges{Version: 1, Files: map[string]string{}, Digests: map[string]string{}}
	logins, err := readCloudLogins(home)
	if err != nil {
		return changes, err
	}
	root := filepath.Join(home, ".local/fennec/native")
	for _, name := range NativeCredentialNames() {
		if logins[nativeCredentialFiles[name]] != "" {
			continue // a separate cloud sign-in shares no grant with the Mac
		}
		data, err := readNativeFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			continue
		}
		if !json.Valid(data) {
			continue // mid-write; the next notification reads it again
		}
		digest := credentialHash(data)
		changes.Digests[name] = digest
		if known[name] != digest {
			changes.Files[name] = base64.StdEncoding.EncodeToString(data)
		}
	}
	return changes, nil
}

// ResolveMacCredential decides whether a cloud copy replaces the Mac copy and
// returns the Mac file to write. It applies the same rules as a preparation
// sync (chooseNativeCredential), from the Mac's side:
//
//   - The Mac copy unchanged since both computers last agreed (macUnchanged):
//     the cloud copy is newer and is kept, unless it holds no credential.
//   - Otherwise the copies are compared like a conflict on the cloud: a copy
//     with no credential never wins, the same grant keeps the later expiry,
//     otherwise only a copy confirmed usable wins, and nothing is written when
//     neither can be confirmed.
//
// Only the refresh grant moves: Claude's claudeAiOauth, Codex's tokens and
// last_refresh, and Pi/OpenCode OAuth providers present on both computers.
// Every other Mac field (MCP OAuth entries, API keys, other providers) stays
// as it is. A Mac that uses an API key, or a cloud copy without an OAuth
// grant, is never changed.
func ResolveMacCredential(ctx context.Context, name string, mac, cloud []byte, macUnchanged bool) ([]byte, bool) {
	var m, c map[string]json.RawMessage
	if json.Unmarshal(mac, &m) != nil || json.Unmarshal(cloud, &c) != nil || c == nil {
		return nil, false
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	decide := func(cloudEntry, macEntry nativeOAuthEntry) bool {
		if cloudEntry.signedOut() || cloudEntry.refresh == "" {
			return false
		}
		if macEntry.refresh == "" && !macEntry.signedOut() {
			return false // the Mac signs in with an API key
		}
		if macEntry.refresh == cloudEntry.refresh && macEntry.access == cloudEntry.access {
			return false
		}
		if macUnchanged {
			return true
		}
		return chooseNativeCredential(ctx, cloudEntry, macEntry) == keepCloudCredential
	}
	whole := func(raw map[string]json.RawMessage) map[string]any {
		out := map[string]any{}
		for key, value := range raw {
			var v any
			_ = json.Unmarshal(value, &v)
			out[key] = v
		}
		return out
	}
	out := map[string]json.RawMessage{}
	for key, value := range m {
		out[key] = value
	}
	replace := func(keys ...string) {
		for _, key := range keys {
			if value, ok := c[key]; ok {
				out[key] = value
			} else {
				delete(out, key)
			}
		}
	}
	changed := false
	switch name {
	case "claude/.credentials.json":
		if decide(claudeEntry(whole(c)), claudeEntry(whole(m))) {
			replace("claudeAiOauth")
			changed = true
		}
	case "codex/auth.json":
		if decide(codexEntry(whole(c)), codexEntry(whole(m))) {
			replace("tokens", "last_refresh")
			changed = true
		}
	case "pi/auth.json", "opencode/data/opencode/auth.json":
		for key, cv := range c {
			mv, ok := m[key]
			if !ok || bytes.Equal(cv, mv) {
				continue // a provider signed in only on the cloud stays there
			}
			var cAny, mAny any
			_ = json.Unmarshal(cv, &cAny)
			_ = json.Unmarshal(mv, &mAny)
			ce, me := providerEntry(key, cAny), providerEntry(key, mAny)
			if me.refresh == "" {
				continue // only OAuth providers share a grant
			}
			if decide(ce, me) {
				out[key] = cv
				changed = true
			}
		}
	default:
		return nil, false
	}
	if !changed {
		return nil, false
	}
	data, err := marshalCredentialLike(out, mac)
	if err != nil || bytes.Equal(data, mac) {
		return nil, false
	}
	return data, true
}

// marshalCredentialLike keeps the Mac file's indentation style so the agent's
// own file stays recognizable; the Keychain JSON stays compact.
func marshalCredentialLike(value map[string]json.RawMessage, original []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(original)
	if len(trimmed) > 1 && trimmed[0] == '{' && (trimmed[1] == '\n' || trimmed[1] == '\r') {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return nil, err
		}
		if bytes.HasSuffix(original, []byte("\n")) {
			data = append(data, '\n')
		}
		return data, nil
	}
	return json.Marshal(value)
}

// NativeCredentialSyncState records, per credential file, the digest of the
// Mac copy and of the cloud copy at the last point both computers agreed.
// It holds digests only and is scoped to one account's runtime.
type NativeCredentialSyncState struct {
	Scope string            `json:"scope"`
	Mac   map[string]string `json:"mac"`
	Cloud map[string]string `json:"cloud"`
}

func nativeCredentialStatePath(home string) string {
	return filepath.Join(home, ".config/fennec/native-credential-sync.json")
}

// LoadNativeCredentialSyncState returns the state for scope, or an empty
// state when it belongs to another account or runtime.
func LoadNativeCredentialSyncState(home, scope string) NativeCredentialSyncState {
	state := NativeCredentialSyncState{Scope: scope, Mac: map[string]string{}, Cloud: map[string]string{}}
	data, err := readNativeFile(nativeCredentialStatePath(home))
	if err != nil {
		return state
	}
	var saved NativeCredentialSyncState
	if json.Unmarshal(data, &saved) != nil || saved.Scope != scope {
		return state
	}
	for name, digest := range saved.Mac {
		if nativeCredentialFiles[name] != "" && nativeDigestPattern.MatchString(digest) {
			state.Mac[name] = digest
		}
	}
	for name, digest := range saved.Cloud {
		if nativeCredentialFiles[name] != "" && nativeDigestPattern.MatchString(digest) {
			state.Cloud[name] = digest
		}
	}
	return state
}

func SaveNativeCredentialSyncState(home string, state NativeCredentialSyncState) error {
	directory := filepath.Dir(nativeCredentialStatePath(home))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicCredential(nativeCredentialStatePath(home), data)
}
