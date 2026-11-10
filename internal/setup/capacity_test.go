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
			hook := `import builtins,io,os,time
original=builtins.open
def observed(path,*args,**kwargs):
 if path=='/proc/meminfo':return io.StringIO('MemTotal: '+os.environ['TEST_MEMORY']+' kB\nMemAvailable: 100 kB\n')
 return original(path,*args,**kwargs)
builtins.open=observed
os.cpu_count=lambda:int(os.environ['TEST_CPUS'])
clock=[0]
def monotonic():
 clock[0]+=100
 return clock[0]
time.monotonic=monotonic
`
			if err := os.WriteFile(filepath.Join(dir, "sitecustomize.py"), []byte(hook), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-s")
			cmd.Stdin = strings.NewReader(GuestCapacityScript)
			cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "TEST_MEMORY="+tc.memory, "TEST_CPUS="+tc.cpus)
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
