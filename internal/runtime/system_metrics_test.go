package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectSystemMetricsParsesProcAndDeltaRates(t *testing.T) {
	dir := t.TempDir()
	stat := filepath.Join(dir, "stat")
	mem := filepath.Join(dir, "meminfo")
	netdev := filepath.Join(dir, "dev")
	cgroup := filepath.Join(dir, "cgroup")
	if err := os.MkdirAll(cgroup, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(stat, []byte("cpu  100 0 100 800 0 0 0 0 0 0\n"), 0644)
	_ = os.WriteFile(mem, []byte("MemTotal:        4194304 kB\nMemAvailable:    2621440 kB\n"), 0644)
	_ = os.WriteFile(netdev, []byte("Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n    lo: 999999       10    0    0    0     0          0         0   999999       10    0    0    0     0       0          0\n  eth0: 10485760    100    0    0    0     0          0         0  2097152      50    0    0    0     0       0          0\n"), 0644)

	state := &metricsSampleState{
		hasSample: true,
		at:        time.Now().Add(-time.Second),
		cpuTotal:  900,
		cpuIdle:   750,
		netRx:     9485760,
		netTx:     1597152,
	}
	got := collectSystemMetricsFromPaths(stat, mem, netdev, cgroup, state)
	if got.CPUPercent < 49.0 || got.CPUPercent > 51.0 {
		t.Fatalf("expected ~50%% CPU utilization, got %.1f%%", got.CPUPercent)
	}
	if got.MemoryCurrentMiB != 4096 || got.MemoryUsedMiB != 1536 {
		t.Fatalf("unexpected memory metrics: %+v", got)
	}
	if got.NetRxBytes != 10485760 || got.NetTxBytes != 2097152 || got.NetRxBPS == 0 || got.NetTxBPS == 0 {
		t.Fatalf("unexpected network metrics (loopback should be excluded): %+v", got)
	}
}
