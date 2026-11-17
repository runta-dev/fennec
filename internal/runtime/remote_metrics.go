package runtime

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// RemoteMetricsScript prints the files CollectSystemMetrics reads, for a
// client that reaches the cloud computer over SSH instead of the Mac's
// terminal stream (Fennec for iPhone). Sections start with "@@<name>".
const RemoteMetricsScript = `for f in stat meminfo net/dev; do echo "@@$f"; cat /proc/$f 2>/dev/null; done; ` +
	`for f in memory.current memory.max; do echo "@@$f"; cat /sys/fs/cgroup/$f 2>/dev/null; done; ` +
	`echo "@@nproc"; nproc 2>/dev/null; echo "@@df"; df -B1 --output=used,size / 2>/dev/null | tail -n 1`

// RemoteSampler turns successive RemoteMetricsScript outputs into the same
// SystemMetrics the Mac panel shows, with CPU and memory history. The first
// sample has no CPU reading yet; poll about once a second or two.
type RemoteSampler struct {
	state metricsSampleState
	dir   string
}

func (r *RemoteSampler) Sample(output string) (SystemMetrics, error) {
	if r.dir == "" {
		dir, err := os.MkdirTemp("", "fennec-metrics-")
		if err != nil {
			return SystemMetrics{}, err
		}
		if err = os.MkdirAll(filepath.Join(dir, "cgroup"), 0o700); err != nil {
			return SystemMetrics{}, err
		}
		r.dir = dir
	}
	sections := map[string]string{}
	for _, part := range strings.Split("\n"+output, "\n@@")[1:] {
		name, body, _ := strings.Cut(part, "\n")
		sections[strings.TrimSpace(name)] = body
	}
	files := map[string]string{"stat": "stat", "meminfo": "meminfo", "net/dev": "netdev", "memory.current": "cgroup/memory.current", "memory.max": "cgroup/memory.max"}
	for section, file := range files {
		if err := os.WriteFile(filepath.Join(r.dir, file), []byte(sections[section]), 0o600); err != nil {
			return SystemMetrics{}, err
		}
	}
	m := collectSystemMetricsFromPaths(filepath.Join(r.dir, "stat"), filepath.Join(r.dir, "meminfo"), filepath.Join(r.dir, "netdev"), filepath.Join(r.dir, "cgroup"), &r.state)
	m.VCPUs, _ = strconv.Atoi(strings.TrimSpace(sections["nproc"]))
	if fields := strings.Fields(sections["df"]); len(fields) == 2 {
		m.DiskUsedBytes, _ = strconv.ParseUint(fields[0], 10, 64)
		m.DiskTotalBytes, _ = strconv.ParseUint(fields[1], 10, 64)
	}
	memMiB := float64(m.MemoryUsedMiB)
	if m.MemoryUsedBytes > 0 {
		memMiB = math.Round((float64(m.MemoryUsedBytes)/1048576.0)*1000) / 1000
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.cpuHistory = append(r.state.cpuHistory, m.CPUPercent)
	if len(r.state.cpuHistory) > metricsHistoryCap {
		r.state.cpuHistory = r.state.cpuHistory[len(r.state.cpuHistory)-metricsHistoryCap:]
	}
	r.state.memHistory = append(r.state.memHistory, memMiB)
	if len(r.state.memHistory) > metricsHistoryCap {
		r.state.memHistory = r.state.memHistory[len(r.state.memHistory)-metricsHistoryCap:]
	}
	m.CPUHistory = append([]float64(nil), r.state.cpuHistory...)
	m.MemHistory = append([]float64(nil), r.state.memHistory...)
	return m, nil
}
