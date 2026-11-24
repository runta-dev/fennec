package cli

import (
	"context"
	"errors"
	"fennec/internal/account"
	"io"
	"testing"
	"time"
)

// Until a self-hosted backend exists, a paired computer must fail preparation
// explicitly instead of sending an empty credential to the Runta API.
func TestSelfHostedPrincipalNeverReachesRuntaPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := account.New(ctx, serviceCredentials{}, "http://127.0.0.1:1")
	if _, err := manager.Pair("vps-1"); err != nil {
		t.Fatal(err)
	}
	_, principal, _, ok := manager.Session()
	if !ok {
		t.Fatal("paired principal unavailable")
	}
	s := newDaemonServices(t.TempDir(), manager)
	if err := s.prepare(ctx, principal, io.Discard); !errors.Is(err, errSelfHostedUnavailable) {
		t.Fatalf("unexpected preparation result: %v", err)
	}
}
