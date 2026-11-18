package localssh

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"fennec/internal/sandbox"
)

// Real SSH requests exercise the byte boundary without executing a large argv
// on Linux, whose per-string exec limit differs from the Mac connector's host.
// Commands within the limit must still reach review, and a denial must stand.
func TestExecCommandByteLimitPreservesApproval(t *testing.T) {
	for _, size := range []int{maxExecCommandBytes - 1, maxExecCommandBytes, maxExecCommandBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var calls atomic.Int32
			broker := NewBroker("", io.Discard)
			broker.Reviewer = countingReviewer(t, `{"outcome":"deny","rationale":"Diagnostic denial."}`, &calls)
			broker.Policy = func() sandbox.Policy { return sandbox.PolicyForPreset(sandbox.PresetAuto, nil) }
			execute := startConnector(t, broker)
			prefix := `fennec-escalate --reason "boundary test" -- : #`
			remaining := size - len(prefix)
			command := prefix + strings.Repeat("é", remaining/2) + strings.Repeat("x", remaining%2)
			code, stderr := execute(command)
			if code != 126 {
				t.Fatalf("unexpected exit: %d %s", code, stderr)
			}
			if size > maxExecCommandBytes {
				if calls.Load() != 0 || !strings.Contains(stderr, "exceeds 128 KiB (131072 bytes)") || !strings.Contains(stderr, "stdin") {
					t.Fatalf("oversize request: reviews=%d stderr=%q", calls.Load(), stderr)
				}
			} else if calls.Load() != 1 || strings.Contains(stderr, "exceeds") {
				t.Fatalf("valid request did not retain approval: reviews=%d stderr=%q", calls.Load(), stderr)
			}
		})
	}
}

func TestExec128KiBOnMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Mac argv acceptance")
	}
	broker := NewBroker("", io.Discard)
	broker.Policy = func() sandbox.Policy { return sandbox.PolicyForPreset(sandbox.PresetFull, nil) }
	execute := startConnector(t, broker)
	prefix := "true #"
	if code, stderr := execute(prefix + strings.Repeat("x", maxExecCommandBytes-len(prefix))); code != 0 {
		t.Fatalf("128 KiB exec failed: %d %s", code, stderr)
	}
	if code, stderr := execute(""); code != 126 || !strings.Contains(stderr, "explicit command is required") {
		t.Fatalf("empty exec: %d %q", code, stderr)
	}
}

func TestDesktopMCPUsesExecCommandLimit(t *testing.T) {
	spec := DesktopMCPSpec{Name: "node_repl", Command: "/Applications/ChatGPT.app/Contents/MacOS/node", Args: []string{strings.Repeat("a", 4096), strings.Repeat("b", 4096)}}
	command, err := EncodeDesktopMCP(spec)
	if err != nil || len(command) <= 8192 {
		t.Fatalf("valid larger command rejected: size=%d err=%v", len(command), err)
	}
	spec.Args = make([]string, 32)
	for i := range spec.Args {
		spec.Args[i] = strings.Repeat("a", 4096)
	}
	if _, err := EncodeDesktopMCP(spec); err == nil {
		t.Fatal("oversize MCP command accepted")
	}
}
