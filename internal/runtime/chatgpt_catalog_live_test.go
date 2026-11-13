package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TestLiveChatGPTModelCatalog runs on the cloud computer with
// FENNEC_LIVE_CHATGPT_CATALOG=1. It builds a ChatGPT home from a copy of the
// real gateway configuration in a temporary HOME, starts the installed codex
// app-server over stdio with Fennec's options and checks that model/list names
// the configured Magpie model as Magpie does. It sends no inference request and
// changes nothing outside the temporary HOME.
func TestLiveChatGPTModelCatalog(t *testing.T) {
	if os.Getenv("FENNEC_LIVE_CHATGPT_CATALOG") != "1" {
		t.Skip("set FENNEC_LIVE_CHATGPT_CATALOG=1 on the cloud computer")
	}
	realHome, _ := os.UserHomeDir()
	gatewayConfig, err := os.ReadFile(filepath.Join(realHome, ".codex/config.toml"))
	if err != nil {
		t.Fatal("no gateway Codex configuration")
	}
	var routing struct {
		Model string `toml:"model"`
	}
	if toml.Unmarshal(gatewayConfig, &routing) != nil || routing.Model == "" {
		t.Fatal("gateway configuration names no model")
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".codex"), 0700)
	os.MkdirAll(filepath.Join(home, ".local/fennec"), 0700)
	os.WriteFile(filepath.Join(home, ".codex/config.toml"), gatewayConfig, 0600)
	os.WriteFile(filepath.Join(home, ".local/fennec/agent-mode.json"), []byte(`{"native":false}`), 0600)
	t.Setenv("CODEX_HOME", "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	started := time.Now()
	_, env, codexHome, err := codexAppServerEnvironment(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ChatGPT home prepared in %s", time.Since(started).Round(time.Millisecond))
	started = time.Now()
	if _, _, _, err := codexAppServerEnvironment(ctx, home); err != nil {
		t.Fatal(err)
	}
	t.Logf("unchanged ChatGPT home checked in %s", time.Since(started).Round(time.Millisecond))
	overrides := gatewayOverrides(codexHome)
	if overrides == nil || catalogSignature(codexHome) == "" {
		t.Fatal("ChatGPT home lacks the magpie provider or the model catalog")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("codex is not installed")
	}
	command := exec.CommandContext(ctx, codex, append(append([]string{"-c", "features.code_mode_host=true"}, overrides...), "app-server")...)
	command.Env, command.Dir = env, home
	stdin, _ := command.StdinPipe()
	stdout, _ := command.StdoutPipe()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	io.WriteString(stdin, `{"id":1,"method":"initialize","params":{"clientInfo":{"name":"fennec-live-test","version":"1"},"capabilities":{"experimentalApi":true}}}`+"\n"+`{"method":"initialized"}`+"\n"+`{"id":2,"method":"model/list","params":{}}`+"\n")
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for lines.Scan() {
		var reply struct {
			ID     int `json:"id"`
			Result struct {
				Data []struct {
					Model       string `json:"model"`
					DisplayName string `json:"displayName"`
					Efforts     []struct {
						Effort string `json:"reasoningEffort"`
					} `json:"supportedReasoningEfforts"`
				} `json:"data"`
			} `json:"result"`
		}
		if json.Unmarshal(lines.Bytes(), &reply) != nil || reply.ID != 2 {
			continue
		}
		t.Logf("model/list returned %d models", len(reply.Result.Data))
		for _, model := range reply.Result.Data {
			if model.Model != routing.Model {
				continue
			}
			efforts := []string{}
			for _, effort := range model.Efforts {
				efforts = append(efforts, effort.Effort)
			}
			t.Logf("%s: %q efforts %v", model.Model, model.DisplayName, efforts)
			if model.DisplayName == "" || model.DisplayName == model.Model || len(efforts) == 0 {
				t.Fatal("configured model is not described by Magpie's catalog")
			}
			return
		}
		t.Fatalf("model/list lacks the configured model %s", routing.Model)
	}
	t.Fatal("no model/list reply", lines.Err())
}
