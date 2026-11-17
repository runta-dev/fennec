package runtime

import (
	"context"
	"encoding/json"
	"fennec/internal/localssh"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicit acceptance against installed Herdr and the paired Mac. Only the
// synthetic receipt terminal is visible to this isolated outbox; user terminals
// are neither read nor sent messages. The fixture never executes input.
func TestLiveMacOnlineDelivery(t *testing.T) {
	device := os.Getenv("FENNEC_LIVE_MAC_RELAY_DEVICE")
	if device == "" {
		t.Skip("explicit live relay acceptance")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	real := NewLocalRuntime(home)
	script := `stty -echo; printf '\033]7501;state=working:app=codex\033\\'; while IFS= read -r line; do printf '\033]7501;state=working:app=codex\033\\'; printf 'receipt:%s\n' "$line"; printf '\033]7501;state=idle:app=codex\033\\'; done`
	raw, err := real.Run(ctx, "run", "--json", "--bg", "--no-parent", "--no-notify", "--label", "fennec.session=relay-notice", "--label", "fennec.agent=codex", "--", "/bin/sh", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	var terminal LocalTerminal
	if json.Unmarshal(raw, &terminal) != nil || terminal.Address == "" {
		t.Fatalf("invalid fixture: %s", raw)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, e := real.Run(cleanup, "kill", terminal.Address); e != nil {
			t.Errorf("fixture cleanup: %v", e)
		}
	}()
	isolated := &LocalRuntime{Home: t.TempDir(), Query: func(c context.Context, method string, params any) (json.RawMessage, error) {
		if method == "terminal.list" {
			raw, e := real.Run(c, "get", "--json", terminal.Address)
			if e != nil {
				return nil, e
			}
			var current LocalTerminal
			if e = json.Unmarshal(raw, &current); e != nil {
				return nil, e
			}
			return json.Marshal(map[string]any{"terminals": []LocalTerminal{current}})
		}
		p := params.(map[string]any)
		if method != "terminal.send" || p["terminal"] != terminal.Address {
			t.Fatal("non-fixture operation")
		}
		return real.Query(c, method, params)
	}}
	h := localssh.LinkHealth{Device: device, Generation: strings.Repeat("a", 32), Since: time.Now().UnixNano()}
	probe := func() error { return localssh.ProbeMac(ctx, home, localssh.LinkHealth{Device: device, Fresh: true}) }
	for {
		result, e := isolated.deliverMacOnline(ctx, h, probe)
		if e != nil {
			t.Fatal(e)
		}
		var counts map[string]int
		json.Unmarshal(result, &counts)
		if counts["delivered"] == 1 {
			break
		}
		if counts["uncertain"] > 0 {
			t.Fatalf("unconfirmed fixture receipt: %s", result)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	for range 3 {
		if _, e := isolated.deliverMacOnline(ctx, h, probe); e != nil {
			t.Fatal(e)
		}
	}
	output, err := real.Run(ctx, "read", "--json", terminal.Address)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(output), "Mac is back online."); count != 1 {
		t.Fatalf("expected one English notice, found %d: %s", count, output)
	}
	t.Log("actual alias probe, Herdr delivery and repeated-batch deduplication passed")
}
