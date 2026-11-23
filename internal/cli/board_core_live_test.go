package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/board"
	"fmt"
	"image"
	"image/png"
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
	store := board.Store{Home: home}
	p, err := store.CreateProject(board.Project{Name: "Fixture", Queue: 1, Workers: []board.Worker{{Agent: "codex", On: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	attachment, err := (board.Store{Home: home}).SaveAttachment(picture.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.CreateRequestWithAttachments(p.ID, "me", "Fixture", "", "", []board.Attachment{attachment})
	if err != nil {
		t.Fatal(err)
	}
	r, err = store.ClaimAt(p.ID, r.Number, "0123456789abcdef0123456789abcdef", "codex", "local")
	if err != nil {
		t.Fatal(err)
	}
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
	if terminal.Labels["fennec.session"] != r.Session || terminal.Labels["fennec.queue"] != p.ID || terminal.Labels["fennec.request"] != "1" {
		t.Fatal(terminal.Labels)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		screen, err := core.call(ctx, "terminal.read", map[string]any{"terminal": terminal.Address, "lines": 100})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(screen, []byte("No inference: subprocess fixture")) && bytes.Contains(screen, []byte("image.png")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("image path and request did not reach the actual terminal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := executor.Start(ctx, p, r, "duplicate"); err != nil {
		t.Fatal("idempotent delivery failed", err)
	}
	if _, err = os.Stat(filepath.Join(home, ".fen/board/launches", r.Session+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Complete(p.ID, r.Number, r.Session, r.Agent, "Question", board.NeedsYou); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FollowUp(p.ID, r.Number, "me", "Follow-up fixture", nil); err != nil {
		t.Fatal(err)
	}
	r, err = store.ClaimAt(p.ID, r.Number, "11111111111111111111111111111111", "codex", "local")
	if err != nil {
		t.Fatal(err)
	}
	if err = executor.Start(ctx, p, r, "Follow-up fixture"); err != nil {
		t.Fatal(err)
	}
	raw, err = core.call(ctx, "terminal.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &listing) != nil || len(listing.Terminals) != 1 || listing.Terminals[0].Address != terminal.Address {
		t.Fatal("follow-up created another terminal", string(raw))
	}
	current, _ := store.Request(p.ID, r.Number)
	if current.ExecutionSession() != terminal.Labels["fennec.session"] {
		t.Fatal("lost thread association")
	}
	if _, err = core.call(ctx, "terminal.kill", map[string]string{"terminal": terminal.Address}); err != nil {
		t.Fatal(err)
	}
}
