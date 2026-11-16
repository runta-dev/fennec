package commandstream

import (
	"bytes"
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestLocalOfferIsSingleUseAndDrainsCommand(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fen-offer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path, done, err := OfferLocal(ctx, dir, func(context.Context) (net.Conn, error) { return Dial(ctx), nil })
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	session := NewStream(ctx, conn)
	payload := bytes.Repeat([]byte{0, 255, 1, 19}, 32768)
	session.Stdin = bytes.NewReader(payload)
	got, err := session.Output("cat")
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("local command offer truncated bytes", err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("offer did not retire")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("offer socket retained", err)
	}
	if second, err := net.DialTimeout("unix", path, time.Second); err == nil {
		second.Close()
		t.Fatal("offer reused")
	}
}

func TestLocalOfferCancellationRetiresUnclaimedSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "fen-offer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	path, done, err := OfferLocal(ctx, dir, func(context.Context) (net.Conn, error) {
		t.Error("unclaimed offer dialed remote")
		return nil, net.ErrClosed
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unclaimed offer retained")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("canceled socket retained", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OfferLocal(context.Background(), dir, nil); err == nil {
		t.Fatal("public offer directory accepted")
	}
}
