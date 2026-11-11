package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"errors"
	"testing"
	"time"
)

func TestServiceReplacementWaitsForRetiringRegistration(t *testing.T) {
	calls := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitServiceRemoval(ctx, func() bool { calls++; return calls < 3 }); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("accepted stale registration", calls)
	}
}
func TestServiceReplacementStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitServiceRemoval(ctx, func() bool { return true }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDaemonReuseRequiresInstalledLocationAndRegistration(t *testing.T) {
	for _, tt := range []struct {
		name, actual, running string
		registered, want      bool
	}{
		{"same build and path", "build", "/Applications/Fennec.app/Contents/MacOS/fen", true, true},
		{"same build moved app", "build", "/Users/test/Downloads/Fennec.app/Contents/MacOS/fen", true, false},
		{"same path missing registration", "build", "/Applications/Fennec.app/Contents/MacOS/fen", false, false},
		{"new build", "older", "/Applications/Fennec.app/Contents/MacOS/fen", true, false},
		{"legacy daemon without location", "build", "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := reusableDaemon("build", tt.actual, "/Applications/Fennec.app/Contents/MacOS/fen", tt.running, tt.registered); got != tt.want {
				t.Fatalf("reusable=%v want %v", got, tt.want)
			}
		})
	}
}
func TestDaemonReuseRecognizesCompatibilitySymlink(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "fen")
	alias := filepath.Join(root, "fennec-daemon")
	if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, alias); err != nil {
		t.Fatal(err)
	}
	if !reusableDaemon("same", "same", binary, alias, true) {
		t.Fatal("compatibility symlink restarted a healthy daemon")
	}
}
func TestMovedApplicationRegistrationUsesNewEscapedPath(t *testing.T) {
	old := daemonPlist("/Volumes/Fennec/Fennec.app/Contents/MacOS/fen", "/tmp/log")
	current := daemonPlist("/Users/test/Applications/Work & AI/Fennec.app/Contents/MacOS/fen", "/tmp/log")
	if bytes.Equal(old, current) || !bytes.Contains(current, []byte("Work &amp; AI")) {
		t.Fatal("move did not update the registration safely")
	}
}

func TestDaemonRegistrationDoesNotBypassLoginPreference(t *testing.T) {
	plist := daemonPlist("/Applications/Fennec.app/Contents/MacOS/fen", "/fixture/log")
	if !bytes.Contains(plist, []byte("<key>RunAtLoad</key><false/>")) || !bytes.Contains(plist, []byte("<key>AfterInitialDemand</key><true/>")) {
		t.Fatal("daemon implicitly starts at login")
	}
}
