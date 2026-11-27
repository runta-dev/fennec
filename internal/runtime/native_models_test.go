package runtime

import (
	"context"
	"testing"
)

func TestNativeLoginLaunchHasNoModelPromptOrHooks(t *testing.T) {
	home := t.TempDir()
	var commands [][]string
	backend := LocalRuntime{Home: home, Lookup: func(string) (string, error) { return "/fixture/codex", nil }, Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "ls" {
			return []byte(`{"terminals":[]}`), nil
		}
		commands = append(commands, append([]string{}, args...))
		return []byte(`{"address":"t1","state":"live"}`), nil
	}}
	request := LocalLaunch{ID: "login-fixture", Agent: "codex", Device: "1234abcd", LocalDirectory: "/", NativeLogin: true}
	if _, err := backend.Launch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 {
		t.Fatal("login was not launched exactly once")
	}
	args := commands[0]
	found := false
	for i, arg := range args {
		if arg == "native-login" {
			found = i+2 < len(args) && args[i+1] == "codex" && args[i+2] == "login-fixture"
		}
		if arg == "--no-daemon" || arg == "--permission-mode" {
			t.Fatal("regular agent options entered authentication")
		}
	}
	if !found {
		t.Fatal("dedicated native login command missing")
	}
	request.Arguments = []string{"untrusted prompt"}
	if request.Validate() == nil {
		t.Fatal("login accepted user-supplied command arguments")
	}
}
