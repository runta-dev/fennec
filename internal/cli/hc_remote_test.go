package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/commandstream"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHCRemoteQuotesArguments(t *testing.T) {
	got := hcRemote([]string{"put", "--json", "t7", "/a b/it's"})
	want := `core="$HOME/.local/bin/herdr-core"; [ -x "$core" ] || core="$(command -v herdr-core || command -v hc || printf %s "$HOME/.local/bin/hc")"; exec "$core" 'put' '--json' 't7' '/a b/it'\''s'`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

// The desktop window runs Contents/MacOS/fen as `hc` (Resources/fennec-hc):
// it must reach hcCommand, not the public fen client.
func TestHCIsAPrivateInvocation(t *testing.T) {
	if PublicInvocation([]string{"hc", "connect"}) {
		t.Fatal("fen hc must not be routed to the public client")
	}
}

func TestHCResidentStreamPreservesBinaryOutputAndFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "hc"), []byte("#!/bin/sh\ncat\nprintf core-error >&2\nexit 17\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload := []byte{0, 255, 13, 10, 27, '[', '3', '1', 'm'}
	var output, stderr bytes.Buffer
	err := runHCStream(ctx, commandstream.Dial(ctx), []string{"connect"}, bytes.NewReader(payload), &output, &stderr)
	if !bytes.Equal(output.Bytes(), payload) || stderr.String() != "core-error" {
		t.Fatalf("streams changed: %v %q", output.Bytes(), stderr.String())
	}
	var status transferExit
	if !errors.As(err, &status) || status.ExitCode() != 17 {
		t.Fatalf("remote failure was hidden: %v", err)
	}
}

func TestHCOfferUsesResidentScope(t *testing.T) {
	s, ctx, _ := managedTransferFixture(t)
	manager := account.New(ctx, serviceCredentials{}, "")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, epoch, _ := manager.Session()
	s.account = manager
	s.epoch = epoch
	t.Setenv("HOME", s.home)
	t.Setenv("SHELL", "/bin/sh")
	bin := filepath.Join(s.home, ".local/bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "herdr-core"), []byte("#!/bin/sh\ncat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	offered, err := s.openHC(ctx, json.RawMessage(`{"args":["connect"],"resident":true}`))
	if err != nil {
		t.Fatal(err)
	}
	offer := offered.(hcOffer)
	if !offer.Resident {
		t.Fatal("nonresident core offer")
	}
	conn, err := net.Dial("unix", offer.Socket)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runHCStream(ctx, conn, []string{"connect"}, strings.NewReader("core-bytes"), &out, io.Discard); err != nil || out.String() != "core-bytes" {
		t.Fatalf("resident core: %q %v", out.String(), err)
	}
	s.mu.Lock()
	s.epoch++
	s.mu.Unlock()
	if _, err := s.openHC(ctx, json.RawMessage(`{"args":["connect"],"resident":true}`)); err == nil {
		t.Fatal("stale account epoch was accepted")
	}
}
