package cmuxhooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalCallbackUsesFixedScopeAndNeverRemotePaths(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "cmux")
	input := filepath.Join(dir, "input.json")
	argv := filepath.Join(dir, "argv")
	script := "#!/bin/sh\ncat > \"$FIXTURE_INPUT\"\nprintf '%s\\n' \"$@\" > \"$FIXTURE_ARGS\"\ntest \"$CMUX_AGENT_HOOK_RELAY_ORIGIN\" = 1 || exit 99\ntest -z \"${CMUX_CODEX_PID:-}\" || exit 98\nprintf '%s' '{\"decision\":\"block\",\"reason\":\"fixture\"}'\nexit 2\n"
	os.WriteFile(binary, []byte(script), 0700)
	local := &Local{Executable: binary, Socket: "/local/socket", Workspace: "local-workspace", Surface: "local-surface", Env: append(os.Environ(), "FIXTURE_INPUT="+input, "FIXTURE_ARGS="+argv, "CMUX_CODEX_PID=999")}
	result := local.Run(context.Background(), "inbox-stop", []byte(`{"session_id":"cloud-session","transcript_path":"/Users/someone/private.json","cwd":"/Users/someone","tool_name":"Bash"}`))
	if result.Code != 2 {
		t.Fatal("hook decision lost", result)
	}
	raw, _ := os.ReadFile(input)
	var payload map[string]any
	json.Unmarshal(raw, &payload)
	if payload["transcript_path"] != nil || payload["cwd"] != nil || payload["session_id"] != "cloud-session" {
		t.Fatal("remote path treated as local")
	}
	args, _ := os.ReadFile(argv)
	if string(args) != "--socket\n/local/socket\nhooks\ncodex\ninbox-stop\n--workspace\nlocal-workspace\n--surface\nlocal-surface\n" {
		t.Fatal("scope escaped", string(args))
	}
}
func TestUnknownEventCannotExecuteLocalCommand(t *testing.T) {
	result := (&Local{Executable: "/bin/echo"}).Run(context.Background(), "../arbitrary-command", []byte(`{}`))
	if string(result.Data) != "{}" {
		t.Fatal(result)
	}
	result = (&Local{Executable: "/missing/cmux"}).Run(context.Background(), "stop", []byte(strings.Repeat("x", PayloadLimit+1)))
	if string(result.Data) != "{}" || result.Code != 0 {
		t.Fatal(result)
	}
}
