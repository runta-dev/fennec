package bench

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A synthetic task file in the upstream shape (no upstream text).
const fakeTask = `{
  "id": "u1",
  "task": {"en": "Open the thing.", "zh": "x"},
  "pre_command": {"en": "touch /Users/ec2-user/a", "zh": "true"},
  "before_action_delay_seconds": 10,
  "before_grading_delay_seconds": 30,
  "grading_command": [["check /Users/ec2-user/a", 100], ["partial", 50]]
}`

func TestLoadTaskReadsTheUpstreamShape(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tasks", "cat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "cat", "u1.json"), []byte(fakeTask), 0o644); err != nil {
		t.Fatal(err)
	}
	tk, err := LoadTask(root, "cat/u1", "en")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Text != "Open the thing." || tk.BeforeAction != 10 || tk.BeforeGrading != 30 {
		t.Errorf("task: %+v", tk)
	}
	if tk.Pre != "touch /Users/admin/a" {
		t.Errorf("pre_command for en, with the home replaced: %q", tk.Pre)
	}
	if len(tk.Grading) != 2 || tk.Grading[0].Command != "check /Users/admin/a" || tk.Grading[1].Score != 50 {
		t.Errorf("grading: %+v", tk.Grading)
	}
	if _, err := LoadTask(root, "cat/u1", "ja"); err == nil {
		t.Error("a missing language must fail")
	}
}

func TestGradeIsBinaryAndOrdered(t *testing.T) {
	cmds := []GradingCommand{{"half", 50}, {"a", 100}, {"b", 100}}
	ran := []string{}
	run := func(out map[string]string, ok map[string]bool) func(string) (string, bool) {
		return func(c string) (string, bool) {
			ran = append(ran, c)
			return out[c], ok[c]
		}
	}
	score, outs, evalErr := Grade(cmds, run(map[string]string{"a": "False", "b": "TRUE"}, map[string]bool{"a": true, "b": true}))
	if score != 100 || evalErr || len(outs) != 2 || strings.Join(ran, ",") != "a,b" {
		t.Errorf("score %d, evalErr %v, ran %v", score, evalErr, ran)
	}
	// "true" anywhere in the output counts, as upstream.
	score, _, _ = Grade(cmds[:2], func(string) (string, bool) { return "Finder\ntrue", true })
	if score != 100 {
		t.Errorf("substring true: %d", score)
	}
	// A failing command stops grading as an evaluation error.
	ran = nil
	score, _, evalErr = Grade(cmds, run(nil, map[string]bool{"a": false}))
	if score != 0 || !evalErr || len(ran) != 1 {
		t.Errorf("failing command: score %d, evalErr %v, ran %v", score, evalErr, ran)
	}
	// Nothing true: zero, not an error.
	score, _, evalErr = Grade(cmds, func(string) (string, bool) { return "False", true })
	if score != 0 || evalErr {
		t.Errorf("all false: %d %v", score, evalErr)
	}
}

func TestReadIDList(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(p, []byte("# a comment\n# commit: abc123\n\ncat/u1\ncat/u2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := ReadIDList(p)
	if err != nil {
		t.Fatal(err)
	}
	if l.Commit != "abc123" || strings.Join(l.IDs, " ") != "cat/u1 cat/u2" {
		t.Errorf("%+v", l)
	}
}

func TestBackendSetupGivesEveryModelBackendTheSameEndpoint(t *testing.T) {
	m := ModelAccess{BaseURL: "http://192.168.64.1:34250/v1", Model: "zai/glm"}
	for _, b := range []string{BackendShuttle, BackendFennec} {
		setup, serve, err := BackendSetup(b, m)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(setup, m.BaseURL) || strings.Count(setup, `"zai/glm"`) != 3 || !strings.Contains(setup, `decide_api = \"chat\"`) && !strings.Contains(setup, `decide_api = "chat"`) {
			t.Errorf("%s setup: %s", b, setup)
		}
		if !strings.Contains(serve, " serve ") || !strings.HasSuffix(serve, " mcp") {
			t.Errorf("%s serve: %s", b, serve)
		}
	}
	_, serve, _ := BackendSetup(BackendDriver, m)
	if !strings.HasSuffix(serve, "mcp --direct") {
		t.Errorf("driver: %s", serve)
	}
	if _, _, err := BackendSetup("nope", m); err == nil {
		t.Error("unknown backend accepted")
	}
}

func TestSummaryCountsScoredTrialsOnly(t *testing.T) {
	scored := []TrialResult{
		{Task: "a/1", Backend: BackendFennec, Trial: 1, Score: 100, AgentMs: 1000, Transcript: &Transcript{Turns: 4, InputTokens: 10}},
		{Task: "a/2", Backend: BackendFennec, Trial: 1, Score: 0, AgentMs: 3000, Transcript: &Transcript{Turns: 6}},
		{Task: "a/1", Backend: BackendShuttle, Trial: 1, Score: 100},
	}
	harness := []TrialResult{{Task: "a/2", Backend: BackendShuttle, Trial: 1, Attempt: 1, HarnessError: "boot: x"}}
	md := Summary([]string{"a/1", "a/2"}, scored, harness)
	for _, want := range []string{"| fennec-cua | 1 / 2 | 50.0% | 1 / 2 | 5.0 | 5 | 2s | 0 |", "| shuttle | 1 / 1 |", "| a/1 | - | 1/1", "boot: x"} {
		if !strings.Contains(md, want) {
			t.Errorf("summary lacks %q:\n%s", want, md)
		}
	}
}

func TestRelayForwards(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c) // echo
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr, err := Relay(ctx, "127.0.0.1:0", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("ping"))
	_ = c.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(c)
	if string(got) != "ping" {
		t.Errorf("relay echoed %q", got)
	}
}
