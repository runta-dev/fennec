//go:build darwin

package installation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPreparedSealCoversDetachedSignatureAttributes(t *testing.T) {
	root := t.TempDir()
	source, dest := filepath.Join(root, "source.app"), filepath.Join(root, "installed.app")
	bundle(t, source, "com.runta.fennec", "new")
	p, err := Prepare(context.Background(), source, dest, false, operations())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := unix.Setxattr(filepath.Join(p.incoming, "Contents/MacOS/fen"), "com.apple.cs.CodeDirectory", []byte("changed"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Apply(context.Background()); err == nil {
		t.Fatal("changed detached signature accepted")
	}
}

// This optional probe clones a real signed bundle and publishes only beneath
// t.TempDir. It never stops, updates or starts the user's installed application.
func TestRealPreparedBundlePublication(t *testing.T) {
	source := os.Getenv("FENNEC_LIVE_PREPARED_BUNDLE")
	if source == "" {
		t.Skip("explicit signed-bundle preflight probe")
	}
	if !filepath.IsAbs(source) {
		t.Fatal("absolute signed bundle required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ops := SystemOperations()
	ops.StopUI, ops.OpenUI = nil, nil
	ops.Warm = func(ctx context.Context, app string) error {
		start := time.Now()
		err := exec.CommandContext(ctx, filepath.Join(app, "Contents/MacOS/fen"), "fen", "--help").Run()
		t.Logf("verified cold startup: %s", time.Since(start))
		return err
	}
	dest := filepath.Join(t.TempDir(), "Fennec.app")
	start := time.Now()
	p, err := Prepare(ctx, source, dest, false, ops)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	t.Logf("background prepare: %s", time.Since(start))
	start = time.Now()
	if _, err := p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("click to publication: %s", time.Since(start))
	start = time.Now()
	if err := ops.Warm(ctx, dest); err != nil {
		t.Fatal(err)
	}
	t.Logf("published startup: %s", time.Since(start))
	if err := ops.Verify(ctx, dest); err != nil {
		t.Fatal(err)
	}
}
