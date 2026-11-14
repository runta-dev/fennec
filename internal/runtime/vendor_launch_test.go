package runtime

import (
	"context"
	"encoding/json"
	"fennec/internal/setup"
	"path/filepath"
	"strings"
	"testing"
)

func TestVendorLaunchUsesNativeIdentityWithMagpie(t *testing.T) {
	for _, id := range []string{"cursor", "grok"} {
		t.Run(id, func(t *testing.T) {
			home := t.TempDir()
			if err := setup.ConfigureAgentMode(home, false); err != nil {
				t.Fatal(err)
			}
			var command []string
			backend := &LocalRuntime{Home: home, Lookup: func(name string) (string, error) {
				want := id
				if id == "cursor" {
					want = "cursor-agent"
				}
				if name != want {
					t.Fatalf("looked up %s instead of %s", name, want)
				}
				return "/bin/" + name, nil
			}, Run: func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "ls" {
					return []byte(`{"terminals":[]}`), nil
				}
				for i, arg := range args {
					if arg == "--" {
						command = args[i+1:]
						break
					}
				}
				return json.Marshal(LocalTerminal{Address: "t1", State: "live"})
			}}
			request := LocalLaunch{ID: "vendor-launch", Agent: id, LocalDirectory: "/project", Device: "1234abcd", Arguments: []string{"--model", "native-model"}}
			if _, err := backend.Launch(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if command[0] != filepath.Join(home, ".local/fennec/bin/fennec-terminal") || command[1] != "native-agent" || command[2] != id {
				t.Fatal(command)
			}
			if !strings.HasSuffix(strings.Join(command, " "), "--model native-model") {
				t.Fatal("lost user model", command)
			}
			if _, err := CloudLaunchScript(request.ID, id, request.LocalDirectory, request.Device, request.Arguments); err != nil {
				t.Fatal(err)
			}
		})
	}
}
