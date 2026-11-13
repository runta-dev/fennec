package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func cloudCredentialPath(home, name string) string {
	return filepath.Join(home, ".local/fennec/native", filepath.FromSlash(name))
}

// seedCloud runs one preparation sync so the cloud holds first and a baseline.
func seedCloud(t *testing.T, home, name, first string) {
	t.Helper()
	request := ModelAccountsRequest{Version: 1, Native: true, Files: map[string]string{name: encodeNative(first)}}
	if _, err := ApplyNativeAccounts(context.Background(), home, request); err != nil {
		t.Fatal(err)
	}
}

func pushCredential(t *testing.T, home, name, data string) NativeCredentialPushResult {
	t.Helper()
	result, err := ApplyNativeCredential(context.Background(), home, NativeCredentialPush{Name: name, Data: encodeNative(data)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestApplyNativeCredentialFollowsPreparationRules(t *testing.T) {
	name := "claude/.credentials.json"
	future, later := credentialTestNow.Add(4*time.Hour).UnixMilli(), credentialTestNow.Add(6*time.Hour).UnixMilli()

	t.Run("mac refresh replaces the cloud copy and updates the baseline", func(t *testing.T) {
		stubCredentialChecks(t)
		home := t.TempDir()
		seedCloud(t, home, name, claudeCredential("first", future))
		refreshed := claudeCredential("second", later)
		result := pushCredential(t, home, name, refreshed)
		got, _ := os.ReadFile(cloudCredentialPath(home, name))
		if !result.Written || result.Deferred || string(got) != refreshed || result.Remote != credentialHash(got) {
			t.Fatalf("result %+v, cloud %s", result, got)
		}
		// A later full sync with the same Mac copy is a no-op, like after a sync.
		before, _ := os.ReadFile(filepath.Join(home, ".local/fennec/native/.baseline.json"))
		again := pushCredential(t, home, name, refreshed)
		after, _ := os.ReadFile(filepath.Join(home, ".local/fennec/native/.baseline.json"))
		if again.Written || again.Remote != result.Remote || string(before) != string(after) {
			t.Fatalf("identical push changed something: %+v", again)
		}
		var baseline map[string]credentialPair
		_ = json.Unmarshal(after, &baseline)
		if baseline[name].Source != credentialHash([]byte(refreshed)) || baseline[name].Remote != credentialHash([]byte(refreshed)) {
			t.Fatalf("baseline not updated like a sync: %+v", baseline[name])
		}
	})

	t.Run("an unchanged mac copy never undoes a cloud refresh", func(t *testing.T) {
		fake := stubCredentialChecks(t)
		home := t.TempDir()
		first := claudeCredential("first", future)
		seedCloud(t, home, name, first)
		cloudRefresh := claudeCredential("cloud", later)
		os.WriteFile(cloudCredentialPath(home, name), []byte(cloudRefresh), 0o600)
		result := pushCredential(t, home, name, first)
		got, _ := os.ReadFile(cloudCredentialPath(home, name))
		if result.Written || string(got) != cloudRefresh || fake.callCount() != 0 {
			t.Fatalf("cloud refresh lost: %+v %s", result, got)
		}
	})

	t.Run("conflicts reuse the preparation decision", func(t *testing.T) {
		for _, tc := range []struct {
			label, cloud, mac string
			accepted          []string
			want              string
		}{
			{"cloud signed out", signedOutClaude, claudeCredential("mac", future), nil, "mac"},
			{"same grant keeps later expiry", claudeCredentialWith("a-c", "r-same", future), claudeCredentialWith("a-m", "r-same", later), nil, "mac"},
			{"only cloud usable", claudeCredential("cloud", future), claudeCredential("mac", later), []string{"a-cloud"}, "cloud"},
			{"nothing confirmed", claudeCredential("cloud", future), claudeCredential("mac", later), nil, "deferred"},
		} {
			t.Run(tc.label, func(t *testing.T) {
				stubCredentialChecks(t, tc.accepted...)
				home := t.TempDir()
				seedCloud(t, home, name, claudeCredential("first", 1000))
				os.WriteFile(cloudCredentialPath(home, name), []byte(tc.cloud), 0o600)
				baselineBefore, _ := os.ReadFile(filepath.Join(home, ".local/fennec/native/.baseline.json"))
				result := pushCredential(t, home, name, tc.mac)
				got, _ := os.ReadFile(cloudCredentialPath(home, name))
				want := map[string]string{"cloud": tc.cloud, "mac": tc.mac, "deferred": tc.cloud}[tc.want]
				if string(got) != want || result.Deferred != (tc.want == "deferred") {
					t.Fatalf("result %+v kept %s, want %s", result, got, tc.want)
				}
				if tc.want == "deferred" {
					after, _ := os.ReadFile(filepath.Join(home, ".local/fennec/native/.baseline.json"))
					if string(after) != string(baselineBefore) {
						t.Fatal("a deferred push must leave the baseline for the next sync")
					}
				}
			})
		}
	})

	t.Run("a separate cloud sign-in is left alone", func(t *testing.T) {
		home := t.TempDir()
		seedCloud(t, home, name, claudeCredential("first", future))
		os.WriteFile(filepath.Join(home, ".local/fennec/native/.cloud-logins.json"), []byte(`{"claude":"able-acorn"}`), 0o600)
		result := pushCredential(t, home, name, claudeCredential("mac", later))
		got, _ := os.ReadFile(cloudCredentialPath(home, name))
		if !result.Skipped || result.Written || string(got) != claudeCredential("first", future) {
			t.Fatalf("cloud sign-in overwritten: %+v", result)
		}
	})

	t.Run("only credential files are accepted", func(t *testing.T) {
		for _, bad := range []NativeCredentialPush{{Name: "claude/settings.json", Data: encodeNative("{}")}, {Name: "../x", Data: encodeNative("{}")}, {Name: name}} {
			if _, err := ApplyNativeCredential(context.Background(), t.TempDir(), bad); err == nil {
				t.Fatalf("accepted %+v", bad.Name)
			}
		}
	})
}

func TestNativeCredentialSecretsStayOutOfResultsAndErrors(t *testing.T) {
	stubCredentialChecks(t)
	home := t.TempDir()
	name := "claude/.credentials.json"
	seedCloud(t, home, name, claudeCredential("first", 1000))
	secret := claudeCredentialWith("a-SECRET-ACCESS", "r-SECRET-REFRESH", credentialTestNow.Add(time.Hour).UnixMilli())
	result := pushCredential(t, home, name, secret)
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatalf("result leaks the credential: %s", encoded)
	}
	_, err := ApplyNativeCredential(context.Background(), home, NativeCredentialPush{Name: name, Data: encodeNative(`{"claudeAiOauth":{"accessToken":"a-SECRET-ACCESS"`)})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("invalid push error %v", err)
	}
	_, err = ApplyNativeCredential(context.Background(), home, NativeCredentialPush{Name: "pi/settings.json", Data: encodeNative(secret)})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("rejected push error %v", err)
	}
	if err := (NativeCredentialWatch{Known: map[string]string{name: "SECRET"}}).Validate(); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("watch validation %v", err)
	}
}

func TestWatchNativeCredentialsAnswersCloudRefreshes(t *testing.T) {
	previous := nativeCredentialSettle
	nativeCredentialSettle = 20 * time.Millisecond
	t.Cleanup(func() { nativeCredentialSettle = previous })
	home := t.TempDir()
	name := "codex/auth.json"
	first := `{"tokens":{"access_token":"a1","refresh_token":"r1"}}`
	seedCloud(t, home, name, first)
	digest := credentialHash([]byte(first))

	// Unknown to the Mac (for example changed while it was disconnected):
	// answered at once.
	changes, err := WatchNativeCredentials(context.Background(), home, NativeCredentialWatch{WaitMS: 1000})
	if err != nil || changes.Files[name] != encodeNative(first) || changes.Digests[name] != digest {
		t.Fatalf("pending change not answered: %+v %v", changes, err)
	}

	// Already seen: the watch waits for the cloud agent's refresh.
	refreshed := `{"tokens":{"access_token":"a2","refresh_token":"r2"}}`
	type answer struct {
		changes NativeCredentialChanges
		err     error
		at      time.Time
	}
	done := make(chan answer, 1)
	go func() {
		changes, err := WatchNativeCredentials(context.Background(), home, NativeCredentialWatch{Known: map[string]string{name: digest}, WaitMS: 10000})
		done <- answer{changes, err, time.Now()}
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case got := <-done:
		t.Fatalf("answered without a change: %+v", got.changes)
	default:
	}
	// The agent writes in two steps; the half-written file is never sent.
	path := cloudCredentialPath(home, name)
	os.WriteFile(path, []byte(`{"tokens":{"access_token":"a2"`), 0o600)
	time.Sleep(5 * time.Millisecond)
	tmp := path + ".tmp"
	os.WriteFile(tmp, []byte(refreshed), 0o600)
	wrote := time.Now()
	os.Rename(tmp, path)
	select {
	case got := <-done:
		if got.err != nil || got.changes.Files[name] != encodeNative(refreshed) {
			t.Fatalf("watch answer %+v %v", got.changes, got.err)
		}
		t.Logf("cloud refresh reported %v after the write", got.at.Sub(wrote))
		if got.at.Sub(wrote) > 2*time.Second {
			t.Fatalf("watch answered after %v", got.at.Sub(wrote))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cloud refresh not reported")
	}

	// A quiet watch ends with no files, and cloud sign-ins are never reported.
	os.WriteFile(filepath.Join(home, ".local/fennec/native/.cloud-logins.json"), []byte(`{"codex":"able-acorn"}`), 0o600)
	changes, err = WatchNativeCredentials(context.Background(), home, NativeCredentialWatch{WaitMS: 50})
	if err != nil || len(changes.Files) != 0 || changes.Digests[name] != "" {
		t.Fatalf("quiet watch %+v %v", changes, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if _, err := WatchNativeCredentials(ctx, home, NativeCredentialWatch{Known: map[string]string{}, WaitMS: 10000}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled watch %v", err)
	}
}

func TestResolveMacCredential(t *testing.T) {
	future, later := credentialTestNow.Add(4*time.Hour).UnixMilli(), credentialTestNow.Add(6*time.Hour).UnixMilli()
	expired := credentialTestNow.Add(-time.Hour).UnixMilli()
	withMCP := func(credential string) string {
		var v map[string]any
		_ = json.Unmarshal([]byte(credential), &v)
		v["mcpOAuth"] = map[string]any{"server": map[string]any{"accessToken": "mcp-mac"}}
		b, _ := json.Marshal(v)
		return string(b)
	}
	name := "claude/.credentials.json"
	for _, tc := range []struct {
		label, mac, cloud string
		unchanged         bool
		accepted          []string
		wantCloud         bool
		maxChecks         int
	}{
		{"mac unchanged takes the cloud refresh", withMCP(claudeCredential("mac", expired)), claudeCredential("cloud", later), true, nil, true, 0},
		{"mac signed out takes the cloud copy", signedOutClaude, claudeCredential("cloud", later), false, nil, true, 0},
		{"cloud signed out never wins", claudeCredential("mac", future), signedOutClaude, true, nil, false, 0},
		{"same grant keeps later expiry (mac)", claudeCredentialWith("a-m", "r-same", later), claudeCredentialWith("a-c", "r-same", future), false, nil, false, 0},
		{"same grant keeps later expiry (cloud)", claudeCredentialWith("a-m", "r-same", future), claudeCredentialWith("a-c", "r-same", later), false, nil, true, 0},
		{"both changed, only cloud usable", claudeCredential("mac", later), claudeCredential("cloud", future), false, []string{"a-cloud"}, true, 2},
		{"both changed, only mac usable", claudeCredential("mac", future), claudeCredential("cloud", later), false, []string{"a-mac"}, false, 2},
		{"both changed, nothing confirmed", claudeCredential("mac", future), claudeCredential("cloud", later), false, nil, false, 2},
		{"identical grant is a no-op", claudeCredential("same", future), claudeCredential("same", future), true, nil, false, 0},
	} {
		t.Run(tc.label, func(t *testing.T) {
			fake := stubCredentialChecks(t, tc.accepted...)
			out, ok := ResolveMacCredential(context.Background(), name, []byte(tc.mac), []byte(tc.cloud), tc.unchanged)
			if ok != tc.wantCloud {
				t.Fatalf("write = %v, want %v", ok, tc.wantCloud)
			}
			if fake.callCount() > tc.maxChecks {
				t.Fatalf("%d checks", fake.callCount())
			}
			if !ok {
				return
			}
			var got, mac, cloud map[string]json.RawMessage
			json.Unmarshal(out, &got)
			json.Unmarshal([]byte(tc.mac), &mac)
			json.Unmarshal([]byte(tc.cloud), &cloud)
			if string(got["claudeAiOauth"]) != string(cloud["claudeAiOauth"]) {
				t.Fatalf("grant not moved: %s", out)
			}
			for key, value := range mac {
				if key != "claudeAiOauth" && string(got[key]) != string(value) {
					t.Fatalf("mac field %s changed: %s", key, out)
				}
			}
		})
	}

	t.Run("codex keeps the mac API key and file layout", func(t *testing.T) {
		stubCredentialChecks(t)
		mac := "{\n  \"OPENAI_API_KEY\": null,\n  \"tokens\": {\"access_token\": \"a1\", \"refresh_token\": \"r1\", \"account_id\": \"acct\"},\n  \"last_refresh\": \"2026-11-13T00:00:00Z\"\n}\n"
		cloud := `{"OPENAI_API_KEY":null,"tokens":{"access_token":"a2","refresh_token":"r2","account_id":"acct"},"last_refresh":"2026-11-14T00:00:00Z"}`
		out, ok := ResolveMacCredential(context.Background(), "codex/auth.json", []byte(mac), []byte(cloud), true)
		if !ok || !strings.HasPrefix(string(out), "{\n  ") || !strings.HasSuffix(string(out), "}\n") || !strings.Contains(string(out), `"a2"`) || !strings.Contains(string(out), "2026-11-14") {
			t.Fatalf("codex update %q", out)
		}
		apiKey := `{"OPENAI_API_KEY":"sk-mac"}`
		if _, ok := ResolveMacCredential(context.Background(), "codex/auth.json", []byte(apiKey), []byte(cloud), true); ok {
			t.Fatal("an API-key Mac was switched to the cloud's OAuth grant")
		}
	})

	t.Run("pi moves only shared OAuth providers", func(t *testing.T) {
		stubCredentialChecks(t)
		mac := `{"anthropic":{"type":"oauth","access":"a1","refresh":"r1","expires":1},"openai":{"type":"api_key","key":"sk-mac"}}`
		cloud := `{"anthropic":{"type":"oauth","access":"a2","refresh":"r2","expires":2},"openai":{"type":"api_key","key":"sk-cloud"},"google":{"type":"oauth","access":"g","refresh":"g"}}`
		out, ok := ResolveMacCredential(context.Background(), "pi/auth.json", []byte(mac), []byte(cloud), true)
		var got map[string]map[string]any
		json.Unmarshal(out, &got)
		if !ok || got["anthropic"]["access"] != "a2" || got["openai"]["key"] != "sk-mac" || got["google"] != nil {
			t.Fatalf("pi update %s", out)
		}
	})
}

// fakeKeychain stands in for the login keychain.
type fakeKeychain struct {
	mu     sync.Mutex
	items  map[string][]byte
	writes int
	deny   bool
}

func (k *fakeKeychain) read(_ context.Context, service, account string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	data, ok := k.items[service+"/"+account]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (k *fakeKeychain) write(_ context.Context, service, account string, data []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.deny {
		return ErrNativeKeychainDenied
	}
	k.writes++
	k.items[service+"/"+account] = append([]byte(nil), data...)
	return nil
}

func TestNativeCredentialStoreReadsAndWritesWhereTheAgentDoes(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"USER": "tester"}
	keychain := &fakeKeychain{items: map[string][]byte{"Claude Code-credentials/tester": []byte(`{"claudeAiOauth":{"accessToken":"a"}}`)}}
	store := NativeCredentialStore{Source: NativeSource{Home: home, Env: func(k string) string { return env[k] }, Keychain: keychain.read}, KeychainWrite: keychain.write}

	data, location, err := store.Read(context.Background(), "claude/.credentials.json")
	if err != nil || location.Service != "Claude Code-credentials" || location.Account != "tester" || location.File != "" || string(data) != `{"claudeAiOauth":{"accessToken":"a"}}` {
		t.Fatalf("claude read %+v %v", location, err)
	}
	if err := store.Write(context.Background(), location, []byte(`{"other":1}`), []byte(`{"x":1}`)); !errors.Is(err, ErrNativeCredentialChanged) || keychain.writes != 0 {
		t.Fatalf("stale Keychain write %v", err)
	}
	if err := store.Write(context.Background(), location, data, []byte(`{"claudeAiOauth":{"accessToken":"b"}}`)); err != nil || keychain.writes != 1 {
		t.Fatalf("Keychain write %v", err)
	}
	keychain.deny = true
	current, _, _ := store.Read(context.Background(), "claude/.credentials.json")
	if err := store.Write(context.Background(), location, current, []byte(`{}`)); !errors.Is(err, ErrNativeKeychainDenied) {
		t.Fatalf("denied write %v", err)
	}

	// Codex in file mode: replaced atomically under Codex's own auth.lock.
	codexDir := filepath.Join(home, ".codex")
	os.MkdirAll(codexDir, 0o700)
	auth := filepath.Join(codexDir, "auth.json")
	os.WriteFile(auth, []byte(`{"tokens":{"access_token":"a1"}}`), 0o600)
	data, location, err = store.Read(context.Background(), "codex/auth.json")
	if err != nil || location.File != auth || location.Lock != filepath.Join(codexDir, "auth.lock") || location.ReadOnly {
		t.Fatalf("codex read %+v %v", location, err)
	}
	if err := store.Write(context.Background(), location, data, []byte(`{"tokens":{"access_token":"a2"}}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(auth); string(got) != `{"tokens":{"access_token":"a2"}}` {
		t.Fatalf("codex file %s", got)
	}
	if info, _ := os.Stat(auth); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := store.Write(context.Background(), location, data, []byte(`{}`)); !errors.Is(err, ErrNativeCredentialChanged) {
		t.Fatalf("stale file write %v", err)
	}

	// Codex in keyring mode is Codex's own item: read, never written.
	os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("cli_auth_credentials_store = \"keyring\"\n"), 0o600)
	keychain.items["Codex Auth/"+codexKeychainAccount(codexDir)] = []byte(`{"tokens":{}}`)
	_, location, err = store.Read(context.Background(), "codex/auth.json")
	if err != nil || !location.ReadOnly || store.Write(context.Background(), location, nil, []byte(`{}`)) == nil {
		t.Fatalf("keyring location %+v %v", location, err)
	}

	// No credential: nothing to read and nothing is ever created.
	if _, _, err := store.Read(context.Background(), "pi/auth.json"); !os.IsNotExist(err) {
		t.Fatalf("missing pi %v", err)
	}
}

func TestWatchFilesNoticesWritesReplacementsAndNewDirectories(t *testing.T) {
	dir := t.TempDir()
	inPlace := filepath.Join(dir, "a.json")
	replaced := filepath.Join(dir, "b.json")
	later := filepath.Join(dir, "later/c.json")
	os.WriteFile(inPlace, []byte("1"), 0o600)
	os.WriteFile(replaced, []byte("1"), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := WatchFiles(ctx, []string{inPlace, replaced, later}, 200*time.Millisecond)
	expect := func(label string, change func()) {
		t.Helper()
		started := time.Now()
		change()
		select {
		case <-events:
			t.Logf("%s noticed in %v", label, time.Since(started))
		case <-time.After(3 * time.Second):
			t.Fatalf("%s not noticed", label)
		}
	}
	expect("in-place write", func() {
		f, _ := os.OpenFile(inPlace, os.O_WRONLY|os.O_APPEND, 0)
		f.Write([]byte("2"))
		f.Close()
	})
	expect("atomic replacement", func() {
		os.WriteFile(replaced+".tmp", []byte("2"), 0o600)
		os.Rename(replaced+".tmp", replaced)
	})
	expect("file in a new directory", func() {
		os.MkdirAll(filepath.Dir(later), 0o700)
		os.WriteFile(later, []byte("1"), 0o600)
	})
	// Unrelated files never signal.
	os.WriteFile(filepath.Join(dir, "unrelated"), []byte("x"), 0o600)
	select {
	case <-events:
		t.Fatal("unrelated file signaled a change")
	case <-time.After(400 * time.Millisecond):
	}
}

func TestNativeCredentialSyncStateIsScopedAndHoldsDigestsOnly(t *testing.T) {
	home := t.TempDir()
	digest := credentialHash([]byte("x"))
	state := NativeCredentialSyncState{Scope: "user/runtime", Mac: map[string]string{"codex/auth.json": digest, "bad": digest}, Cloud: map[string]string{"codex/auth.json": "not-a-digest"}}
	if err := SaveNativeCredentialSyncState(home, state); err != nil {
		t.Fatal(err)
	}
	loaded := LoadNativeCredentialSyncState(home, "user/runtime")
	if loaded.Mac["codex/auth.json"] != digest || loaded.Mac["bad"] != "" || loaded.Cloud["codex/auth.json"] != "" {
		t.Fatalf("loaded %+v", loaded)
	}
	if other := LoadNativeCredentialSyncState(home, "user/other"); len(other.Mac) != 0 {
		t.Fatal("state leaked across runtimes")
	}
	info, _ := os.Stat(filepath.Join(home, ".config/fennec/native-credential-sync.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %v", info.Mode())
	}
	_ = base64.StdEncoding
}
