package cli

import (
	"context"
	"encoding/json"
	"errors"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"strings"
	"testing"
)

func TestAccountSyncDoesNotReplayUncertainResidentMutation(t *testing.T) {
	lost := errors.New("response lost")
	_, err := syncPreparedAccounts(context.Background(), setup.ModelAccountsRequest{Version: 1}, true, func(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error) { return nil, lost }, func(context.Context, setup.ModelAccountsRequest) (setup.ModelAccountsResult, error) {
		t.Fatal("mutation replayed on another path")
		return setup.ModelAccountsResult{}, nil
	})
	if !errors.Is(err, lost) {
		t.Fatal(err)
	}
}

func TestLargeAccountStoreSelectsStdinBeforeSendingAnyFrame(t *testing.T) {
	request := setup.ModelAccountsRequest{Version: 1, Files: map[string]string{"providers.json": strings.Repeat("x", 210<<10)}}
	called := false
	_, err := syncPreparedAccounts(context.Background(), request, true, func(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error) {
		t.Fatal("oversized credential frame sent")
		return nil, nil
	}, func(_ context.Context, got setup.ModelAccountsRequest) (setup.ModelAccountsResult, error) {
		called = true
		if got.Files["providers.json"] != request.Files["providers.json"] {
			t.Fatal("credential payload truncated")
		}
		return setup.ModelAccountsResult{}, nil
	})
	if err != nil || !called {
		t.Fatalf("stdin selection failed: %v", err)
	}
}
