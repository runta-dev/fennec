package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestCapacityUsesObservedMemoryAndCPU(t *testing.T) {
	for _, tc := range []struct {
		name, memory, cpus string
		ready              bool
	}{
		{"four-gib-with-kernel-reservation", "4000000", "2", true},
		{"one-gib", "994000", "2", false},
		{"single-cpu", "4000000", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			meminfo := filepath.Join(dir, "meminfo")
			if err := os.WriteFile(meminfo, []byte("MemTotal: "+tc.memory+" kB\nMemAvailable: 100 kB\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-s")
			cmd.Stdin = strings.NewReader(GuestCapacityScript)
			cmd.Env = append(os.Environ(), "FENNEC_PROC_MEMINFO="+meminfo, "FENNEC_CPU_COUNT="+tc.cpus, "FENNEC_CAPACITY_ATTEMPTS=1")
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ready {
				t.Fatalf("unexpected capacity result: %v %s", err, output)
			}
			if tc.ready && !strings.Contains(string(output), "guest_capacity_verified") {
				t.Fatal("no verified result")
			}
		})
	}
}
