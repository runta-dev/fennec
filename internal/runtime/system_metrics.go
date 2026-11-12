package runtime

import (
	"bufio"
	"bytes"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SystemMetrics reports live CPU utilization, dynamic memory occupancy, and
// cumulative + instantaneous network traffic from the shared cloud runtime.
type SystemMetrics struct {
	CPUPercent       float64 `json:"cpu_percent"`
	VCPUs            int     `json:"vcpus"`
	MemoryUsedMiB    uint64  `json:"memory_used_mib"`
	MemoryCurrentMiB uint64  `json:"memory_current_mib"`
	MemoryMinMiB     uint64  `json:"memory_min_mib,omitempty"`
	MemoryMaxMiB     uint64  `json:"memory_max_mib,omitempty"`
	NetRxBytes       uint64  `json:"net_rx_bytes"`
	NetTxBytes       uint64  `json:"net_tx_bytes"`
	NetRxBPS         uint64  `json:"net_rx_bps"`
	NetTxBPS         uint64  `json:"net_tx_bps"`
	SampledUnixMS    int64   `json:"sampled_unix_ms"`
}

type metricsSampleState struct {
	mu        sync.Mutex
	at        time.Time
	cpuTotal  uint64
	cpuIdle   uint64
	netRx     uint64
	netTx     uint64
	hasSample bool
}

var globalMetricsState metricsSampleState

func CollectSystemMetrics() SystemMetrics {
	return collectSystemMetricsFromPaths("/proc/stat", "/proc/meminfo", "/proc/net/dev", "/sys/fs/cgroup", &globalMetricsState)
}

func collectSystemMetricsFromPaths(procStat, procMeminfo, procNetDev, cgroupDir string, state *metricsSampleState) SystemMetrics {
	now := time.Now()
	total1, idle1, okCPU := readProcStatCPU(procStat)
	rx1, tx1 := readProcNetDev(procNetDev)

	var cpuPct float64
	var rxBPS, txBPS uint64

	state.mu.Lock()
	if state.hasSample && now.Sub(state.at) >= 40*time.Millisecond && now.Sub(state.at) <= 60*time.Second {
		dt := now.Sub(state.at).Seconds()
		if okCPU && total1 > state.cpuTotal {
			dTotal := float64(total1 - state.cpuTotal)
			dIdle := float64(0)
			if idle1 >= state.cpuIdle {
				dIdle = float64(idle1 - state.cpuIdle)
			}
			if dTotal > 0 {
				cpuPct = math.Max(0, math.Min(100, (1.0-dIdle/dTotal)*100.0))
			}
		}
		if dt > 0 {
			if rx1 >= state.netRx {
				rxBPS = uint64(float64(rx1-state.netRx) / dt)
			}
			if tx1 >= state.netTx {
				txBPS = uint64(float64(tx1-state.netTx) / dt)
			}
		}
		state.at = now
		state.cpuTotal = total1
		state.cpuIdle = idle1
		state.netRx = rx1
		state.netTx = tx1
		state.mu.Unlock()
	} else {
		state.mu.Unlock()
		if okCPU {
			time.Sleep(65 * time.Millisecond)
			now2 := time.Now()
			total2, idle2, ok2 := readProcStatCPU(procStat)
			rx2, tx2 := readProcNetDev(procNetDev)
			dt := now2.Sub(now).Seconds()
			if ok2 && total2 > total1 {
				dTotal := float64(total2 - total1)
				dIdle := float64(0)
				if idle2 >= idle1 {
					dIdle = float64(idle2 - idle1)
				}
				cpuPct = math.Max(0, math.Min(100, (1.0-dIdle/dTotal)*100.0))
			}
			if dt > 0 {
				if rx2 >= rx1 {
					rxBPS = uint64(float64(rx2-rx1) / dt)
				}
				if tx2 >= tx1 {
					txBPS = uint64(float64(tx2-tx1) / dt)
				}
			}
			now, total1, idle1, rx1, tx1 = now2, total2, idle2, rx2, tx2
		}
		state.mu.Lock()
		state.at = now
		state.cpuTotal = total1
		state.cpuIdle = idle1
		state.netRx = rx1
		state.netTx = tx1
		state.hasSample = true
		state.mu.Unlock()
	}

	usedMiB, currentMiB, maxMiB := readMemoryMetrics(procMeminfo, cgroupDir)
	return SystemMetrics{
		CPUPercent:       math.Round(cpuPct*10) / 10,
		VCPUs:            runtime.NumCPU(),
		MemoryUsedMiB:    usedMiB,
		MemoryCurrentMiB: currentMiB,
		MemoryMaxMiB:     maxMiB,
		NetRxBytes:       rx1,
		NetTxBytes:       tx1,
		NetRxBPS:         rxBPS,
		NetTxBPS:         txBPS,
		SampledUnixMS:    now.UnixMilli(),
	}
}

func readProcStatCPU(path string) (total uint64, idle uint64, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for i := 1; i < len(fields); i++ {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				continue
			}
			total += v
			if i == 4 || i == 5 {
				idle += v
			}
		}
		return total, idle, total > 0
	}
	return 0, 0, false
}

func readMemoryMetrics(meminfoPath, cgroupDir string) (usedMiB, currentMiB, maxMiB uint64) {
	var totalKiB, availKiB uint64
	if raw, err := os.ReadFile(meminfoPath); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			val, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				totalKiB = val
			case "MemAvailable:":
				availKiB = val
			}
		}
	}
	if totalKiB > 0 {
		currentMiB = (totalKiB + 512) / 1024
		if totalKiB > availKiB {
			usedMiB = (totalKiB - availKiB + 512) / 1024
		}
	}
	if cgroupBytes, ok := readCgroupBytes(cgroupDir + "/memory.current"); ok && cgroupBytes > 0 {
		cgUsed := (cgroupBytes + (512 << 10)) >> 20
		if cgUsed > 0 && (usedMiB == 0 || cgUsed < currentMiB) {
			usedMiB = cgUsed
		}
	}
	if cgroupMax, ok := readCgroupBytes(cgroupDir + "/memory.max"); ok && cgroupMax > 0 && cgroupMax <= (1<<40) {
		maxMiB = (cgroupMax + (512 << 10)) >> 20
	}
	return usedMiB, currentMiB, maxMiB
}

func readCgroupBytes(path string) (uint64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(text, 10, 64)
	return v, err == nil
}

func readProcNetDev(path string) (rxBytes, txBytes uint64) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		iface, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name := strings.TrimSpace(iface)
		if name == "" || name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, errRx := strconv.ParseUint(fields[0], 10, 64)
		tx, errTx := strconv.ParseUint(fields[8], 10, 64)
		if errRx == nil {
			rxBytes += rx
		}
		if errTx == nil {
			txBytes += tx
		}
	}
	return rxBytes, txBytes
}
