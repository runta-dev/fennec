package clipboardbridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var pngQuery = []string{"--type", "image/png", "--no-newline"}

func fixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	for _, id := range []string{"image-first", "image-second"} {
		if err := Prepare(home, id, "/test/fennec-terminal"); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(path, []byte("validated PNG bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func TestDeliveryIsPrivateIsolatedAndConsumed(t *testing.T) {
	home, path := fixture(t)
	for _, query := range [][]string{pngQuery, XclipArgs([]string{"-selection", "clipboard", "-t", "image/png", "-o"})} {
		err := Deliver(context.Background(), home, "image-first", path, func() error {
			if err := Read(home, "image-second", pngQuery, io.Discard); err == nil {
				t.Error("image crossed session boundary")
			}
			for i := 0; i < 2; i++ {
				var list bytes.Buffer
				if err := Read(home, "image-first", []string{"--list-types"}, &list); err != nil || list.String() != "image/png\n" {
					t.Fatalf("list: %q %v", list.String(), err)
				}
			}
			dir, _ := directory(home, "image-first")
			name, err := os.ReadFile(filepath.Join(dir, "current"))
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(dir, string(name)))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("nonprivate image", err)
			}
			var got bytes.Buffer
			if err := Read(home, "image-first", query, &got); err != nil {
				return err
			}
			want, _ := os.ReadFile(path)
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatal("image corrupted")
			}
			if err := Read(home, "image-first", pngQuery, io.Discard); err == nil {
				t.Fatal("image consumed twice")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancelledPasteDoesNotLeaveStaleImage(t *testing.T) {
	home, path := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := Deliver(ctx, home, "image-first", path, func() error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Read(home, "image-first", pngQuery, io.Discard); err == nil {
		t.Fatal("stale clipboard survived cancellation")
	}
	dir, _ := directory(home, "image-first")
	files, _ := os.ReadDir(dir)
	for _, file := range files {
		if file.Name() != "bin" && file.Name() != "paste.lock" {
			t.Fatal("leaked image", file.Name())
		}
	}
}

type heldWriter struct{ entered, release chan struct{} }

func (w heldWriter) Write(p []byte) (int, error) { close(w.entered); <-w.release; return len(p), nil }

func TestExpiredReaderCannotAcknowledgeNextImage(t *testing.T) {
	home, path := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := heldWriter{make(chan struct{}), make(chan struct{})}
	firstRead := make(chan error, 1)
	err := Deliver(ctx, home, "image-first", path, func() error {
		go func() { firstRead <- Read(home, "image-first", pngQuery, writer) }()
		<-writer.entered
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	err = Deliver(context.Background(), home, "image-first", path, func() error {
		close(writer.release)
		if err := <-firstRead; err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := Read(home, "image-first", pngQuery, &got); err != nil {
			t.Fatal("old reader removed new image", err)
		}
		if got.Len() == 0 {
			t.Fatal("missing next image")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeliverySerializesOnlySameSession(t *testing.T) {
	home, path := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- Deliver(ctx, home, "image-first", path, func() error { close(entered); <-release; return Read(home, "image-first", pngQuery, io.Discard) })
	}()
	<-entered
	defer close(release)
	blocked, stop := context.WithCancel(ctx)
	stop()
	if err := Deliver(blocked, home, "image-first", path, func() error { t.Error("overwrote in-flight image"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Deliver(ctx, home, "image-second", path, func() error { return Read(home, "image-second", pngQuery, io.Discard) }); err != nil {
		t.Fatal("other session blocked", err)
	}
	// Cleanup releases the first delivery and waits before TempDir is removed.
	t.Cleanup(func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}

func TestReaderRejectsEscapesAndSymlinks(t *testing.T) {
	home, path := fixture(t)
	if err := Prepare(home, "../escape", "/test/binary"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := Read(home, "image-first", []string{"--type", "text/plain", "--no-newline"}, io.Discard); err == nil {
		t.Fatal("accepted text request")
	}
	dir, _ := directory(home, "image-first")
	current := filepath.Join(dir, "current")
	if err := os.WriteFile(current, []byte("../../secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Read(home, "image-first", pngQuery, io.Discard); err == nil {
		t.Fatal("accepted escaped reference")
	}
	os.WriteFile(current, []byte("image-123"), 0600)
	if err := os.Symlink(path, filepath.Join(dir, "image-123")); err != nil {
		t.Fatal(err)
	}
	if err := Read(home, "image-first", pngQuery, io.Discard); err == nil {
		t.Fatal("followed image symlink")
	}
	os.Remove(current)
	os.Symlink(path, current)
	if err := Read(home, "image-first", pngQuery, io.Discard); err == nil {
		t.Fatal("followed reference symlink")
	}
}
