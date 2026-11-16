package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"fennec/internal/terminalstream"
	"fennec/internal/traffic"

	"golang.org/x/crypto/ssh"
)

var idleVariantOrders = [][3]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}, {0, 2, 1}, {2, 0, 1}, {1, 0, 2}}

// All variants reuse one authenticated connection and keep their channels warm.
// The Mac sender configuration stays fixed; only staged server variants differ.
func compareIdleTerminals(ctx context.Context) (resultErr error) {
	if *count%(3*len(idleVariantOrders)) != 0 {
		return fmt.Errorf("idle comparison sample count must be a multiple of eighteen to balance resident and probe order jointly")
	}
	paths := strings.Split(*idleTerminals, ",")
	if len(paths) != 3 || !safePath(*remote) {
		return fmt.Errorf("idle comparison requires exactly three staged terminals and a safe remote echo path")
	}
	for _, path := range paths {
		if !safePath(path) {
			return fmt.Errorf("unsafe staged terminal path")
		}
	}
	started := time.Now()
	samples := []comparisonSample{}
	defer func() {
		summary := map[string]map[string]distribution{}
		for _, path := range paths {
			p, e, g := []float64{}, []float64{}, []float64{}
			for _, s := range samples {
				if s.Variant == path {
					p, e, g = append(p, s.PingMS), append(e, s.EchoMS), append(g, s.GatewayMS)
				}
			}
			if len(p) > 0 {
				summary[path] = map[string]distribution{"ping": summarize(p), "echo": summarize(e), "gateway": summarize(g)}
			}
		}
		config := traffic.DefaultConfig()
		out := map[string]any{
			"boundary":    "One warm authenticated SSH connection, three persistent resident channels and one shared small JSON guest echo channel. Six permutations alternate resident order; gateway/echo/ping measurement order rotates. Three warm-up pings per resident are excluded. No generated bulk traffic or agent input; background traffic uncontrolled. Client pacing is fixed for every variant, so this compares staged cloud variants rather than changing both endpoints' budgets. Not a UI typing/rendering test.",
			"started_utc": started.UTC(), "finished_utc": time.Now().UTC(), "duration_seconds": time.Since(started).Seconds(),
			"terminals": paths, "samples_per_variant_requested": *count, "samples": samples, "summary": summary,
			"client_budget_bytes_per_second": config.BytesPerSecond, "client_bulk_bytes_per_second": config.BulkBytesPerSecond,
		}
		if resultErr != nil {
			out["error"] = resultErr.Error()
		}
		if err := json.NewEncoder(os.Stdout).Encode(out); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	client, raw, err := comparisonConnection(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	transports := make([]*terminalstream.Transport, 0, len(paths))
	for _, path := range paths {
		t, err := terminalstream.StartSSHContext(ctx, client, path+fmt.Sprintf(" serve --protocol=%d", terminalstream.ProtocolVersion))
		if err != nil {
			return err
		}
		defer t.Close()
		if err := t.Ready(ctx); err != nil {
			return err
		}
		transports = append(transports, t)
	}
	echo, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		return err
	}
	defer echo.Close()
	go ssh.DiscardRequests(requests)
	go io.Copy(io.Discard, echo.Stderr())
	if _, err := echo.SendRequest("exec", false, ssh.Marshal(struct{ Command string }{*remote + " -role echo -timeout " + deadline.String()})); err != nil {
		return err
	}
	for range 3 {
		for _, t := range transports {
			step, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := t.MeasurePing(step)
			cancel()
			if err != nil {
				return err
			}
		}
	}
	for round := 0; round < *count; round++ {
		for _, index := range idleVariantOrders[round%len(idleVariantOrders)] {
			s := comparisonSample{Phase: "idle", Block: round, Variant: paths[index], Started: time.Now().UTC()}
			for turn := 0; turn < 3; turn++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				raw.SetDeadline(time.Now().Add(5 * time.Second))
				switch idleProbeType(round, turn) {
				case 0:
					step, cancel := context.WithTimeout(ctx, 5*time.Second)
					measurement, err := transports[index].MeasurePing(step)
					cancel()
					if err != nil {
						return err
					}
					s.PingMS = ms(measurement.RTT)
				case 1:
					body, err := json.Marshal(terminalstream.Frame{Type: "ping", ID: fmt.Sprintf("measure-%d", round+1)})
					if err != nil {
						return err
					}
					body = append(body, '\n')
					got := make([]byte, len(body))
					begin := time.Now()
					if _, err := echo.Write(body); err != nil {
						return err
					}
					if _, err := io.ReadFull(echo, got); err != nil {
						return err
					}
					if !bytes.Equal(body, got) {
						return fmt.Errorf("echo content mismatch")
					}
					s.EchoMS = ms(time.Since(begin))
				case 2:
					begin := time.Now()
					if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
						return err
					}
					s.GatewayMS = ms(time.Since(begin))
				}
				raw.SetDeadline(time.Time{})
			}
			samples = append(samples, s)
		}
		if (round+1)%10 == 0 {
			fmt.Fprintf(os.Stderr, "idle same-connection rounds=%d\n", round+1)
		}
	}
	return nil
}

func idleProbeType(round, turn int) int {
	return (round/len(idleVariantOrders) + round + turn) % 3
}
