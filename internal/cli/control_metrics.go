package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/runta"
	r "fennec/internal/runtime"
	"time"
)

func (s *daemonServices) runtimeMetrics(ctx context.Context, _ json.RawMessage, controller *controlplane.Controller) (any, error) {
	lifecycle, token, epoch, ok := s.account.Session()
	if !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Runta first"}
	}
	offline := s.network != nil && !s.network.Online()
	if offline {
		return map[string]any{"ready": false, "status": "offline"}, nil
	}
	s.mu.Lock()
	channel, runtimeID, current := s.channel, s.config.RuntimeID, s.epoch
	s.mu.Unlock()
	notReady := func() map[string]any {
		status := "not_created"
		if controller != nil {
			if h, ok := controller.Handlers()["runtime.status"]; ok {
				if val, err := h(ctx, nil); err == nil {
					if rt, ok := val.(controlplane.RuntimeState); ok {
						switch rt.Stage {
						case "preparing", "retrying":
							status = "creating"
						case "offline":
							status = "offline"
						case "disabled":
							status = "not_created"
						case "failed":
							if rt.Events["runtime_progress"]["status"] == "billing_restricted" {
								status = "billing_restricted"
							} else if rt.RuntimeID == "" || rt.Events["runtime_progress"]["status"] != "" {
								status = "not_created"
							} else {
								status = "failed"
							}
						}
					}
				}
			}
		}
		return map[string]any{"ready": false, "status": status}
	}
	if channel == nil || runtimeID == "" || current != epoch {
		return notReady(), nil
	}
	opCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	stop := context.AfterFunc(lifecycle, cancel)
	defer stop()

	transport, err := channel.get(opCtx)
	if err != nil || transport == nil || !transport.Supports("system_metrics") {
		return notReady(), nil
	}
	raw, err := transport.Operate(opCtx, r.LocalOperation{Kind: "system_metrics"})
	if err != nil {
		return notReady(), nil
	}
	var metrics r.SystemMetrics
	if json.Unmarshal(raw, &metrics) != nil {
		return notReady(), nil
	}

	metrics.MemoryMinMiB = runta.SharedRuntimeBaselineMemoryMiB
	if metrics.MemoryMaxMiB == 0 {
		metrics.MemoryMaxMiB = runta.SharedRuntimeMaxMemoryMiB
	}
	s.metricsAPIMu.Lock()
	if s.metricsRuntimeID != runtimeID || time.Since(s.metricsAPIAt) > 15*time.Second {
		s.metricsAPIMu.Unlock()
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
			s.metricsVCPUs = int(rt.Resources.Requests.VCPUs)
			s.metricsAPIMu.Unlock()
		}
	} else {
		s.metricsAPIMu.Unlock()
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
	s.metricsAPIMu.Unlock()

	return map[string]any{
		"ready":              true,
		"runtime_id":         runtimeID,
		"cpu_percent":        metrics.CPUPercent,
		"vcpus":              metrics.VCPUs,
		"memory_used_mib":    metrics.MemoryUsedMiB,
		"memory_current_mib": metrics.MemoryCurrentMiB,
		"memory_min_mib":     metrics.MemoryMinMiB,
		"memory_max_mib":     metrics.MemoryMaxMiB,
		"net_rx_bytes":       metrics.NetRxBytes,
		"net_tx_bytes":       metrics.NetTxBytes,
		"net_rx_bps":         metrics.NetRxBPS,
		"net_tx_bps":         metrics.NetTxBPS,
		"sampled_unix_ms":    metrics.SampledUnixMS,
	}, nil
}
