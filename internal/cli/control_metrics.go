package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/runta"
	r "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"time"
)

func copyMetricsMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		switch s := v.(type) {
		case []float64:
			out[k] = append([]float64(nil), s...)
		default:
			out[k] = v
		}
	}
	return out
}

func (s *daemonServices) cachedRuntimeMetrics(runtimeID string) map[string]any {
	if runtimeID == "" {
		return nil
	}
	s.metricsAPIMu.Lock()
	defer s.metricsAPIMu.Unlock()
	if s.metricsCache == nil || s.metricsCache["runtime_id"] != runtimeID || time.Since(s.metricsCacheAt) > 15*time.Second {
		return nil
	}
	return copyMetricsMap(s.metricsCache)
}

// runMetricsCollector keeps recent CPU/Memory/Network samples warm in the
// control plane whenever the resident channel is connected, so opening the UI
// or switching to Account renders a complete curve immediately without delay.
func (s *daemonServices) runMetricsCollector(ctx context.Context, epoch uint64) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var idleTicks int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.network != nil && !s.network.Online() {
				continue
			}
			_, token, curEpoch, ok := s.account.Session()
			if !ok || curEpoch != epoch {
				return
			}
			s.mu.Lock()
			ready, channel, runtimeID := s.brokerReady, s.channel, s.config.RuntimeID
			s.mu.Unlock()
			if !ready || channel == nil || runtimeID == "" {
				continue
			}
			transport := channel.peekAlive()
			if transport == nil || !transport.Supports("system_metrics") {
				continue
			}
			s.metricsAPIMu.Lock()
			activeUI := !s.metricsDemandAt.IsZero() && time.Since(s.metricsDemandAt) <= 8*time.Second
			hasCache := s.metricsCache != nil && s.metricsCache["runtime_id"] == runtimeID
			s.metricsAPIMu.Unlock()
			if !activeUI && hasCache {
				idleTicks++
				if idleTicks%2 != 0 {
					continue
				}
			} else {
				idleTicks = 0
			}
			opCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
			_, _ = s.fetchMetricsFromTransport(opCtx, transport, token, runtimeID)
			cancel()
		}
	}
}

func (s *daemonServices) fetchMetricsFromTransport(opCtx context.Context, transport *terminalstream.Transport, token, runtimeID string) (map[string]any, bool) {
	raw, err := transport.Operate(opCtx, r.LocalOperation{Kind: "system_metrics"})
	if err != nil {
		return nil, false
	}
	var metrics r.SystemMetrics
	if json.Unmarshal(raw, &metrics) != nil {
		return nil, false
	}
	metrics.MemoryMinMiB = runta.SharedRuntimeBaselineMemoryMiB
	if metrics.MemoryMaxMiB == 0 {
		metrics.MemoryMaxMiB = runta.SharedRuntimeMaxMemoryMiB
	}
	s.metricsAPIMu.Lock()
	needAPIRefresh := s.metricsRuntimeID != runtimeID || time.Since(s.metricsAPIAt) > 15*time.Second
	s.metricsAPIMu.Unlock()
	if needAPIRefresh {
		apiCtx, apiCancel := context.WithTimeout(opCtx, 1500*time.Millisecond)
		rt, apiErr := runta.NewClient(s.home, token).Get(apiCtx, runtimeID)
		apiCancel()
		if apiErr == nil && rt.Resources != nil {
			s.metricsAPIMu.Lock()
			s.metricsRuntimeID = runtimeID
			s.metricsAPIAt = time.Now()
			s.metricsMinMiB = rt.Resources.Requests.Memory
			s.metricsCurMiB = rt.Resources.Current.Memory
			s.metricsMaxMiB = rt.Resources.Limits.Memory
			// The card shows the machine's ceiling (e.g. 8 vCPU · 16 GiB): with
			// dynamic vCPU the limit is what a burst can use; fixed runtimes
			// report no vCPU limit, so fall back to the request.
			s.metricsVCPUs = int(rt.Resources.Limits.VCPUs)
			if s.metricsVCPUs <= 0 {
				s.metricsVCPUs = int(rt.Resources.Requests.VCPUs)
			}
			s.metricsAPIMu.Unlock()
		}
	}
	s.metricsAPIMu.Lock()
	if s.metricsRuntimeID == runtimeID {
		if s.metricsMinMiB > 0 {
			metrics.MemoryMinMiB = s.metricsMinMiB
		}
		if s.metricsCurMiB > 0 {
			metrics.MemoryCurrentMiB = s.metricsCurMiB
		}
		if s.metricsMaxMiB > 0 {
			metrics.MemoryMaxMiB = s.metricsMaxMiB
		}
		if s.metricsVCPUs > 0 {
			metrics.VCPUs = s.metricsVCPUs
		}
	}
	payload := map[string]any{
		"ready":              true,
		"runtime_id":         runtimeID,
		"cpu_percent":        metrics.CPUPercent,
		"vcpus":              metrics.VCPUs,
		"memory_used_mib":    metrics.MemoryUsedMiB,
		"memory_used_bytes":  metrics.MemoryUsedBytes,
		"memory_current_mib": metrics.MemoryCurrentMiB,
		"memory_min_mib":     metrics.MemoryMinMiB,
		"memory_max_mib":     metrics.MemoryMaxMiB,
		"disk_used_bytes":    metrics.DiskUsedBytes,
		"disk_total_bytes":   metrics.DiskTotalBytes,
		"net_rx_bytes":       metrics.NetRxBytes,
		"net_tx_bytes":       metrics.NetTxBytes,
		"net_rx_bps":         metrics.NetRxBPS,
		"net_tx_bps":         metrics.NetTxBPS,
		"sampled_unix_ms":    metrics.SampledUnixMS,
	}
	if len(metrics.CPUHistory) > 0 {
		payload["cpu_history"] = append([]float64(nil), metrics.CPUHistory...)
	}
	if len(metrics.MemHistory) > 0 {
		payload["mem_history"] = append([]float64(nil), metrics.MemHistory...)
	}
	s.metricsCache = payload
	s.metricsCacheAt = time.Now()
	out := copyMetricsMap(payload)
	s.metricsAPIMu.Unlock()
	return out, true
}

func (s *daemonServices) runtimeMetrics(ctx context.Context, _ json.RawMessage, controller *controlplane.Controller) (any, error) {
	lifecycle, token, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Runta first"}
	}
	s.metricsAPIMu.Lock()
	s.metricsDemandAt = time.Now()
	s.metricsAPIMu.Unlock()
	offline := s.network != nil && !s.network.Online()
	if offline {
		return map[string]any{"ready": false, "status": "offline"}, nil
	}
	s.mu.Lock()
	channel, runtimeID, current := s.channel, s.config.RuntimeID, s.epoch
	s.mu.Unlock()
	notReady := func() map[string]any {
		status := "unknown"
		if runtimeID != "" {
			status = "unavailable"
		}
		if controller != nil {
			if h, ok := controller.Handlers()["runtime.status"]; ok {
				if val, err := h(ctx, nil); err == nil {
					if rt, ok := val.(controlplane.RuntimeState); ok {
						if runtimeID == "" {
							runtimeID = rt.RuntimeID
							if runtimeID != "" {
								status = "unavailable"
							}
						}
						if rt.Stage == "preparing" || rt.Stage == "retrying" {
							status = "connecting"
						}
						if rt.Stage == "offline" {
							status = "offline"
						}
					}
				}
			}
		}
		if runtimeID == "" && status != "connecting" && status != "offline" {
			queryCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			client := runta.NewClient(s.home, token)
			owner, err := client.CurrentUser(queryCtx)
			if err == nil {
				runtimes, err := client.List(queryCtx)
				if err == nil {
					status, runtimeID = cloudPresenceStatus(runtimes, owner)
				}
			}
		}
		return map[string]any{"ready": false, "status": status, "runtime_id": runtimeID}
	}
	if channel == nil || runtimeID == "" || current != epoch {
		return notReady(), nil
	}
	s.metricsAPIMu.Lock()
	if s.metricsCache != nil && s.metricsCache["runtime_id"] == runtimeID && time.Since(s.metricsCacheAt) <= 950*time.Millisecond {
		fresh := copyMetricsMap(s.metricsCache)
		s.metricsAPIMu.Unlock()
		return fresh, nil
	}
	s.metricsAPIMu.Unlock()

	opCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()

	transport, err := channel.get(opCtx)
	if err != nil || transport == nil || !transport.Supports("system_metrics") {
		return notReady(), nil
	}
	if out, ok := s.fetchMetricsFromTransport(opCtx, transport, token, runtimeID); ok {
		return out, nil
	}
	return notReady(), nil
}

// Only a complete authenticated listing can establish absence. Failed and
// paused machines still exist and must never offer creation as recovery.
func cloudPresenceStatus(runtimes []runta.Runtime, owner string) (string, string) {
	for _, rt := range runtimes {
		if rt.Owner == owner && rt.Environment["FENNEC_PURPOSE"] == "shared-v1" && rt.Status != "deleted" {
			return "unavailable", rt.ID
		}
	}
	return "not_created", ""
}
