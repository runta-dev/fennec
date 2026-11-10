package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayRouteValidation(t *testing.T) {
	for _, route := range []GatewayRoute{{Provider: "p", Model: "m"}, {Provider: "p", Model: "m", Agents: []string{"bad"}}, {Provider: "p", Model: "m", Agents: []string{"codex", "codex"}}, {Provider: "p\ncmd", Model: "m", Agents: []string{"codex"}}} {
		if _, err := GatewayRouteScript(route); err == nil {
			t.Fatal("invalid route accepted")
		}
	}
}
func TestGatewayRouteQuotesAndPreflights(t *testing.T) {
	s, err := GatewayRouteScript(GatewayRoute{Provider: "provider", Model: "model'$(touch /tmp/unsafe)", Agents: []string{"claude", "codex", "pi", "opencode"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, `'"'"'`) || strings.Index(s, "selected model is not exposed") > strings.Index(s, "magpie 'claude'") {
		t.Fatal("missing quote/preflight")
	}
}

// Execute the generated shell against controlled tools. The fake gateway accepts
// the preflight; the fake CLI records mutations but serves real model readbacks.
func TestGatewayReconcileSkipsMatchingModels(t *testing.T) {
	for _, matching := range []bool{true, false} {
		t.Run(fmt.Sprint(matching), func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local/bin")
			os.MkdirAll(bin, 0700)
			os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\ncat >/dev/null\n"), 0700)
			current := "other/model"
			if matching {
				current = "magpie/provider/model"
			}
			cli := "#!/bin/sh\nif [ \"$2\" = model ]; then printf '%s model %s\\n' \"$1\" " + shellQuote(current) + "; else printf '%s\\n' \"$1\" >> \"$HOME/writes\"; fi\n"
			os.WriteFile(filepath.Join(bin, "magpie"), []byte(cli), 0700)
			script, err := GatewayRouteScript(GatewayRoute{Provider: "provider", Model: "model", Agents: []string{"claude", "codex", "pi", "opencode"}})
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("sh", "-s")
			command.Stdin = strings.NewReader(script)
			command.Env = append(os.Environ(), "HOME="+home)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, output)
			}
			data, err := os.ReadFile(filepath.Join(home, "writes"))
			if matching {
				if !os.IsNotExist(err) {
					t.Fatalf("matching models rewritten: %s", data)
				}
			} else if err != nil || len(strings.Fields(string(data))) != 4 {
				t.Fatalf("missing route repairs: %s %v", data, err)
			}
		})
	}
}
