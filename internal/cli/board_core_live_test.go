package cli

import (
	"context"
	"encoding/json"
	"fennec/internal/board"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func init() {
	if os.Getenv("FENNEC_TEST_QUEUE_AGENT") == "1" && len(os.Args) > 1 && os.Args[1] == "local-agent" {
		fmt.Println("Fennec queue fixture ready")
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}
func TestLiveQueueUsesRealHerdrSession(t *testing.T) {
	if os.Getenv("FENNEC_LIVE_QUEUE_CORE") != "1" {
		t.Skip("opt-in isolated real herdr-core integration")
	}
	program := "/Applications/Fennec.app/Contents/MacOS/hc"
	if _, err := os.Stat(program); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := fmt.Sprintf("test-fq-%x", uint32(time.Now().UnixNano()))
	t.Setenv("FENNEC_TEST_QUEUE_AGENT", "1")
	core := &boardLocalCore{lifetime: ctx, program: program, session: session}
	defer func() {
		cmd := exec.Command(program, "server", "stop")
		cmd.Env = append(os.Environ(), "HC_SESSION="+session)
		if err := cmd.Run(); err != nil {
			t.Error(err)
		}
	}()
	exe, _ := os.Executable()
	home := t.TempDir()
	executor := queueMacExecutor{core: core, home: home, exe: exe}
	p := board.Project{ID: "fixture", Workers: []board.Worker{{Agent: "codex", On: true}}}
	r := board.Request{Number: 1, Session: "0123456789abcdef0123456789abcdef", Agent: "codex"}
	if err := executor.Start(ctx, p, r, "No inference: subprocess fixture"); err != nil {
		t.Fatal(err)
	}
	raw, err := core.call(ctx, "terminal.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Terminals []struct {
			Address string            `json:"address"`
			Labels  map[string]string `json:"labels"`
		} `json:"terminals"`
	}
	if json.Unmarshal(raw, &listing) != nil {
		t.Fatal("invalid listing")
	}
	if len(listing.Terminals) != 1 {
		t.Fatal(string(raw))
	}
	terminal := listing.Terminals[0]
	if terminal.Labels["fennec.run"] != r.Session || terminal.Labels["fennec.queue"] != p.ID || terminal.Labels["fennec.request"] != "1" {
		t.Fatal(terminal.Labels)
	}
	if err := executor.Start(ctx, p, r, "duplicate"); err == nil {
		t.Fatal("repeated terminal.run")
	}
	if _, err = os.Stat(filepath.Join(home, ".fen/board/launches", r.Session+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err = core.call(ctx, "terminal.kill", map[string]string{"terminal": terminal.Address}); err != nil {
		t.Fatal(err)
	}
}
