package cmuxhooks

import (
	"bufio"
	"context"
	"encoding/json"
	"fennec/internal/hookbridge"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// This optional acceptance test executes the downloaded, signed cmux CLI. The
// socket is a task-owned cmux API fixture; no user's workspace is modified.
func TestOfficialCmuxCLIReceivesRelayedHook(t *testing.T) {
	binary := os.Getenv("FENNEC_CMUX_BINARY")
	if binary == "" {
		t.Skip("explicit official cmux binary required")
	}
	home := shortHome(t)
	socket := filepath.Join(home, "cmux.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	workspace, surface := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	var mu sync.Mutex
	var methods []string
	var events []map[string]any
	var workers sync.WaitGroup
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connection.Close()
				scanner := bufio.NewScanner(connection)
				scanner.Buffer(make([]byte, 4096), 1<<20)
				for scanner.Scan() {
					var request map[string]any
					if json.Unmarshal(scanner.Bytes(), &request) != nil {
						connection.Write([]byte("OK\n"))
						continue
					}
					method, _ := request["method"].(string)
					mu.Lock()
					methods = append(methods, method)
					if method == "feed.push" {
						events = append(events, request)
					}
					mu.Unlock()
					result := map[string]any{"status": "acknowledged"}
					switch method {
					case "surface.list":
						result = map[string]any{"surfaces": []any{map[string]any{"id": surface, "workspace_id": workspace}}}
					case "workspace.list":
						result = map[string]any{"workspaces": []any{map[string]any{"id": workspace}}}
					case "feed.push":
						result["item_id"] = "44444444-4444-4444-8444-444444444444"
						result["item_ids"] = []string{"44444444-4444-4444-8444-444444444444"}
					}
					json.NewEncoder(connection).Encode(map[string]any{"id": request["id"], "ok": true, "result": result})
				}
			}()
		}
	}()
	local := &Local{Executable: binary, Socket: socket, Workspace: workspace, Surface: surface, Env: []string{"HOME=" + home, "CFFIXED_USER_HOME=" + home, "PATH=/usr/bin:/bin", "CMUX_AGENT_HOOK_STATE_DIR=" + filepath.Join(home, "hook-state"), "CMUX_WORKSPACE_ID=" + workspace, "CMUX_SURFACE_ID=" + surface}}
	adapters, err := Adapters(local)
	if err != nil {
		t.Fatal(err)
	}
	approved := false
	result := hookbridge.Execute(context.Background(), "fixture-owner", "fixture-runtime", adapters["session-start"], []byte(`{"session_id":"33333333-3333-4333-8333-333333333333","hook_event_name":"SessionStart","source":"startup","cwd":"/root/.fen/petite-neat","transcript_path":"/root/private.json"}`), func(context.Context, string, string) (context.Context, func(), error) {
		approved = true
		return context.Background(), func() {}, nil
	})
	if !approved || result.Status != "executed" {
		t.Fatal("official adapter did not pass authorization", result)
	}
	listener.Close()
	workers.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatalf("official cmux emitted no hook event: code=%d methods=%v", result.Code, methods)
	}
	for _, method := range methods {
		if method == "agent.resolve_delivery_target" {
			t.Fatal("remote hook inferred an unrelated local process")
		}
	}
	t.Logf("official cmux accepted relayed session-start; methods=%v", methods)
}
