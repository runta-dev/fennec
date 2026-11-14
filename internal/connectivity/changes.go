package connectivity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

// WatchChanges observes local network topology, not remote reachability. It
// never sends traffic or opens SSH. Notifications are coalesced and bounded;
// callers keep their normal backoff as the fallback when events are unavailable.
func WatchChanges(ctx context.Context) <-chan struct{} {
	return watchChanges(ctx, watchRouteChanges(ctx), networkFingerprint, time.Second, 300*time.Millisecond, time.Second)
}

func networkFingerprint() string {
	var parts []string
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, item := range interfaces {
		if item.Flags&net.FlagLoopback != 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d:%s:%d", item.Index, item.Name, item.Flags))
		addresses, _ := item.Addrs()
		for _, address := range addresses {
			parts = append(parts, fmt.Sprintf("%d:%s", item.Index, address))
		}
	}
	sort.Strings(parts)
	// The resolver configuration supplements routing events; scoped VPN routes
	// are observed by the platform watcher even when this file stays unchanged.
	resolver, _ := os.ReadFile("/etc/resolv.conf")
	digest := sha256.Sum256(append([]byte(strings.Join(parts, "\n")), resolver...))
	return string(digest[:])
}

func watchChanges(ctx context.Context, events <-chan struct{}, fingerprint func() string, interval, debounce, minimum time.Duration) <-chan struct{} {
	out := make(chan struct{}, 1)
	initial := fingerprint()
	go func() {
		defer close(out)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var timer *time.Timer
		var pending <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		previous, lastCheck, lastSent := initial, time.Now(), time.Time{}
		schedule := func() {
			if pending != nil {
				return
			}
			delay := max(debounce, time.Until(lastSent.Add(minimum)))
			timer = time.NewTimer(delay)
			pending = timer.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if current := fingerprint(); current != "" {
					previous = current
				}
				schedule()
			case <-ticker.C:
				now := time.Now()
				// A delayed tick after sleep is useful even if addresses are the
				// same. Compare wall time because monotonic clocks may pause asleep.
				resumed := now.UnixNano()-lastCheck.UnixNano() > int64(max(5*time.Second, 3*interval))
				lastCheck = now
				current := fingerprint()
				if resumed || (current != "" && previous != "" && current != previous) {
					schedule()
				}
				if current != "" {
					previous = current
				}
			case <-pending:
				pending = nil
				lastSent = time.Now()
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
