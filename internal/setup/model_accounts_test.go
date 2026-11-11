package setup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelAccountsBatchOrdersOperationsWithoutReturningSecrets(t *testing.T) {
	home := t.TempDir()
	request := ModelAccountsRequest{Version: 1, Files: map[string]string{"providers.json": base64.StdEncoding.EncodeToString([]byte(`{"source":"fixture-secret"}`))}}
	pluginsDone := false
	result, err := reconcileModelAccounts(context.Background(), home, request, func(_ context.Context, gotHome string, _ []ModelPlugin) error {
		data, err := os.ReadFile(filepath.Join(gotHome, ".config/magpie/providers.json"))
		if err != nil || !bytes.Contains(data, []byte("fixture-secret")) {
			t.Fatal("plugins ran before credential synchronization")
		}
		pluginsDone = true
		return nil
	}, func(context.Context, string) error {
		if !pluginsDone {
			t.Fatal("identity restored before plugins")
		}
		return nil
	})
	if err != nil || result.Migrated != 1 || result.Version != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	data, _ := json.Marshal(result)
	if bytes.Contains(data, []byte("fixture-secret")) || bytes.Contains(data, []byte("providers.json")) {
		t.Fatal("credential material escaped in reply")
	}
}

func TestDeferredCatalogDoesNotQueryBeforeAccountReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { t.Error("catalog query remained on account critical path"); cancel() }})
	result, err := ApplyModelAccounts(ctx, t.TempDir(), ModelAccountsRequest{Version: 1, DeferCatalog: true})
	if err != nil || !result.CatalogDeferred || result.CatalogAvailable || len(result.Models) != 0 {
		t.Fatalf("unexpected deferred result: %+v %v", result, err)
	}
	if _, present := result.Timings["catalog"]; present {
		t.Fatal("deferred catalog reported as executed")
	}
}

func TestInvalidBatchRejectedBeforeCredentialMutation(t *testing.T) {
	home := t.TempDir()
	request := ModelAccountsRequest{Version: 1, Files: map[string]string{"providers.json": base64.StdEncoding.EncodeToString([]byte(`{}`))}, Plugins: []ModelPlugin{{Package: "unapproved", Version: "1.0.0"}}}
	_, err := reconcileModelAccounts(context.Background(), home, request, func(context.Context, string, []ModelPlugin) error { t.Fatal("invalid plugin executed"); return nil }, func(context.Context, string) error { t.Fatal("invalid batch restored identity"); return nil })
	if err == nil {
		t.Fatal("invalid plugin accepted")
	}
	if _, err := os.Stat(filepath.Join(home, ".config/magpie/providers.json")); !os.IsNotExist(err) {
		t.Fatal("credentials changed before validation")
	}
	for _, input := range []string{`{"version":2}`, `{"version":1,"unexpected":true}`, `{"version":1} {}`} {
		if _, err := ReceiveModelAccounts(context.Background(), home, strings.NewReader(input)); err == nil {
			t.Fatal("invalid envelope accepted", input)
		}
	}
}
