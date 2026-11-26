package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A core left by an earlier Fennec.app refuses terminal.run until it is
// restarted; the queue restarts it and asks once more (FEN-33).
func TestBoardLocalCoreRestartsStaleCore(t *testing.T) {
	dir := t.TempDir()
	restarted := filepath.Join(dir, "restarted")
	program := filepath.Join(dir, "hc")
	script := `#!/bin/sh
if [ "$1" = server ] && [ "$2" = restart ]; then echo "$HC_SESSION" > "` + restarted + `"; exit 0; fi
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9]*\).*/\1/')
  if [ -f "` + restarted + `" ]; then
    printf '{"jsonrpc":"2.0","id":%s,"result":{"address":"t1"}}\n' "$id"
  else
    printf '{"jsonrpc":"2.0","id":%s,"error":{"kind":"unavailable","message":"no holder: holder build 1:2:3:4 is not current for core build 1:2:0:4"}}\n' "$id"
  fi
done
`
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	core := &boardLocalCore{lifetime: ctx, program: program, session: "fen33"}
	result, err := core.call(ctx, "terminal.run", map[string]any{"argv": []string{"true"}})
	if err != nil {
		t.Fatalf("terminal.run after restart: %v", err)
	}
	if !strings.Contains(string(result), `"t1"`) {
		t.Fatalf("result = %s", result)
	}
	if data, err := os.ReadFile(restarted); err != nil || strings.TrimSpace(string(data)) != "fen33" {
		t.Fatalf("core was not restarted for session fen33: %q %v", data, err)
	}
}

func TestBoardLocalCoreDoesNotRetryOtherMethods(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "hc")
	script := `#!/bin/sh
if [ "$1" = server ]; then touch "` + filepath.Join(dir, "restarted") + `"; exit 0; fi
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9]*\).*/\1/')
  printf '{"jsonrpc":"2.0","id":%s,"error":{"message":"no holder: x is not current for core build y"}}\n' "$id"
done
`
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	core := &boardLocalCore{lifetime: ctx, program: program, session: "fen33"}
	if _, err := core.call(ctx, "terminal.kill", map[string]any{"terminal": "t1"}); err == nil {
		t.Fatal("terminal.kill on a stale core must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "restarted")); err == nil {
		t.Fatal("only terminal.run restarts the core")
	}
}
