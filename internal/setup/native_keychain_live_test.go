package setup

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLiveKeychainRoundTrip exercises the real /usr/bin/security path on a
// throwaway login-keychain item created by the test itself, never an agent's
// item. FENNEC_LIVE_KEYCHAIN=1 go test -run TestLiveKeychainRoundTrip ./internal/setup
func TestLiveKeychainRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("FENNEC_LIVE_KEYCHAIN") != "1" {
		t.Skip("set FENNEC_LIVE_KEYCHAIN=1 on macOS")
	}
	service, account := "Fennec Probe-credentials", "fennec-probe"
	cleanup := func() {
		exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", account).Run()
	}
	cleanup()
	t.Cleanup(cleanup)
	if err := exec.Command("/usr/bin/security", "add-generic-password", "-a", account, "-s", service, "-w", `{"claudeAiOauth":{"accessToken":"probe-1"}}`).Run(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := nativeKeychainModified(ctx, service, account)
	if err != nil || before == "" {
		t.Fatalf("modification date %q %v", before, err)
	}
	time.Sleep(1100 * time.Millisecond) // the date has one-second resolution
	updated := []byte(`{"claudeAiOauth":{"accessToken":"probe-2","refreshToken":"r"},"mcpOAuth":{}}`)
	started := time.Now()
	if err := writeNativeKeychain(ctx, service, account, updated); err != nil {
		t.Fatal(err)
	}
	t.Logf("Keychain update with read-back verification took %v", time.Since(started))
	got, err := readNativeKeychain(ctx, service, account)
	if err != nil || string(got) != string(updated) {
		t.Fatalf("read back %q %v", got, err)
	}
	after, _ := nativeKeychainModified(ctx, service, account)
	if after == before {
		t.Fatal("modification date unchanged after an update")
	}
	if err := writeNativeKeychain(ctx, service, account, []byte(`{"k":"`+strings.Repeat("a", 2100)+`"}`)); err == nil {
		t.Fatal("an over-long update was sent; security -i would truncate it")
	}
	if got, _ := readNativeKeychain(ctx, service, account); string(got) != string(updated) {
		t.Fatal("a refused update changed the item")
	}
}
