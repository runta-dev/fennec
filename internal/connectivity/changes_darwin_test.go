package connectivity

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinNetworkEventsExcludeRouteCacheNoise(t *testing.T) {
	for _, sample := range []struct {
		kind  byte
		flags uint32
		want  bool
	}{
		{unix.RTM_ADD, unix.RTF_GATEWAY, true},
		{unix.RTM_DELETE, unix.RTF_GATEWAY, true},
		{unix.RTM_CHANGE, unix.RTF_GATEWAY, true},
		{unix.RTM_IFINFO, 0, true}, {unix.RTM_NEWADDR, 0, true}, {unix.RTM_DELADDR, 0, true},
		{unix.RTM_ADD, unix.RTF_LLINFO, false}, {unix.RTM_ADD, unix.RTF_WASCLONED, false},
		{unix.RTM_GET, 0, false}, {unix.RTM_MISS, 0, false},
	} {
		data := make([]byte, unix.SizeofRtMsghdr)
		binary.NativeEndian.PutUint16(data[:2], uint16(len(data)))
		data[2], data[3] = unix.RTM_VERSION, sample.kind
		binary.NativeEndian.PutUint32(data[8:12], sample.flags)
		if got := relevantRouteChange(data); got != sample.want {
			t.Fatalf("type=%d flags=%d: %v", sample.kind, sample.flags, got)
		}
		if relevantRouteChange(data[:len(data)-1]) {
			t.Fatal("truncated routing message accepted")
		}
	}
}

func TestDarwinRouteSubscriptionStartsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := watchRouteChanges(ctx)
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
check:
	for {
		select {
		case _, ok := <-events:
			if !ok {
				t.Fatal("read-only route socket subscription failed")
			}
		case <-timer.C:
			break check
		}
	}
	cancel()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline.C:
			t.Fatal("route subscription did not stop")
		}
	}
}
