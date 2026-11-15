package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"fennec/internal/setup"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMagpieCredentialWatchDeduplicatesAndRetriesAfterReconnect(t *testing.T) {
	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	files := map[string]string{"logins.json": encode(`[{"id":"fixture","seen":1,"auth":{"token":"first"}}]`)}
	calls := 0
	online := true
	syncer := magpieCredentialSync{gather: func() (map[string]string, error) { return files, nil }, push: func(context.Context, map[string]string) error {
		if !online {
			return errors.New("offline")
		}
		calls++
		return nil
	}}
	syncer.last, _ = magpieCredentialDigest(files)
	if err := syncer.step(context.Background()); err != nil || calls != 0 {
		t.Fatal("replayed startup snapshot", err, calls)
	}
	files["logins.json"] = encode(`[ {"seen":2,"auth":{"token":"first"},"id":"fixture"} ]`)
	if err := syncer.step(context.Background()); err != nil || calls != 0 {
		t.Fatal("timestamp/formatting triggered credential upload", err, calls)
	}
	files["logins.json"] = encode(`[{"id":"fixture","seen":2,"auth":{"token":"refreshed"}}]`)
	online = false
	if err := syncer.step(context.Background()); err == nil || calls != 0 {
		t.Fatal("offline mutation reported success", err, calls)
	}
	online = true
	if err := syncer.step(context.Background()); err != nil || calls != 1 {
		t.Fatal("pending refresh not delivered", err, calls)
	}
	if err := syncer.step(context.Background()); err != nil || calls != 1 {
		t.Fatal("same credential sent repeatedly", err, calls)
	}
}

func TestMagpieCredentialSyncKeepsEditsDuringAnInflightUpload(t *testing.T) {
	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	files := map[string]string{"providers.json": encode(`{"provider":{"token":"first"}}`)}
	var sent []string
	syncer := magpieCredentialSync{gather: func() (map[string]string, error) {
		snapshot := map[string]string{}
		for k, v := range files {
			snapshot[k] = v
		}
		return snapshot, nil
	}, push: func(_ context.Context, snapshot map[string]string) error {
		sent = append(sent, snapshot["providers.json"])
		files["providers.json"] = encode(`{"provider":{"token":"second"}}`)
		return nil
	}}
	if err := syncer.step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := syncer.step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0] == sent[1] {
		t.Fatal("inflight file change was lost")
	}
	if err := syncer.step(context.Background()); err != nil || len(sent) != 2 {
		t.Fatal("stable content retried")
	}
}

// Exercise real file replacement notifications and the production merge without
// touching the user's credentials or relying on the five-second retry ticker.
func TestMagpieCredentialWatcherPublishesAtomicReplacement(t *testing.T) {
	local, remote := t.TempDir(), t.TempDir()
	root := filepath.Join(local, ".config/magpie")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "providers.json")
	if err := os.WriteFile(path, []byte(`{"fixture":"before"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := setup.WatchFiles(ctx, []string{path}, time.Minute)
	delivered := make(chan error, 8)
	syncer := magpieCredentialSync{
		gather: func() (map[string]string, error) { return setup.GatherMagpieCredentials(local) },
		push: func(ctx context.Context, files map[string]string) error {
			_, err := setup.SyncMagpieCredentials(ctx, remote, files)
			delivered <- err
			return err
		},
	}
	files, err := syncer.gather()
	if err != nil {
		t.Fatal(err)
	}
	syncer.last, _ = magpieCredentialDigest(files)
	done := make(chan struct{})
	go func() { defer close(done); syncer.run(ctx, changes, func() bool { return true }) }()
	defer func() { cancel(); <-done }()
	start := time.Now()
	if err := os.WriteFile(path+".next", []byte(`{"fixture":"after"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".next", path); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-delivered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("atomic credential replacement did not trigger prompt sync")
	}
	data, err := os.ReadFile(filepath.Join(remote, ".config/magpie/providers.json"))
	if err != nil || string(data) != `{"fixture":"after"}` {
		t.Fatal("cloud did not receive replacement")
	}
	t.Logf("file replacement to confirmed cloud write: %s", time.Since(start))
}

func TestMagpieCredentialWatcherDisabledDoesNotReadCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{}, 1)
	checked := make(chan struct{}, 1)
	syncer := magpieCredentialSync{gather: func() (map[string]string, error) { t.Error("read Magpie credentials while disabled"); return nil, nil }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		syncer.run(ctx, changes, func() bool { checked <- struct{}{}; return false })
	}()
	changes <- struct{}{}
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("watch event was not handled")
	}
	cancel()
	<-done
}
