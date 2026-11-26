package fennecmobile

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
)

func TestParseCloudAgents(t *testing.T) {
	got := parseCloudAgents("device\tabcdef12\nagents\t{\"claude\":{\"account\":\"signed_in\",\"ready\":true},\"pi\":{\"account\":\"signed_out\",\"magpie\":true,\"ready\":true},\"codex\":{\"account\":\"signed_out\",\"ready\":false}}\n")
	want := CloudAgentsState{Connected: true, Known: true, Agents: map[string]setup.CloudAgent{
		"claude": {Account: "signed_in", Ready: true},
		"pi":     {Account: "signed_out", Magpie: true, Ready: true},
		"codex":  {Account: "signed_out"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// An older cloud computer: no device key, and no cloud_agents operation.
	old := parseCloudAgents("device\t\nagents\t\n")
	if old.Connected || old.Known {
		t.Fatalf("got %+v", old)
	}
}

// The script under sh with a fake fennec-terminal that knows cloud_agents,
// and one that predates it.
func TestCloudAgentsScriptRuns(t *testing.T) {
	for _, known := range []bool{true, false} {
		home := t.TempDir()
		bin := filepath.Join(home, ".local/fennec/bin")
		os.MkdirAll(bin, 0o755)
		os.MkdirAll(filepath.Join(home, ".ssh/fennec"), 0o700)
		os.WriteFile(filepath.Join(home, ".ssh/fennec/0123456789abcdef.conf"), nil, 0o600)
		fake := "#!/bin/sh\necho unsupported operation >&2; exit 1\n"
		if known {
			fake = "#!/bin/sh\ncase \"$(echo \"$2\" | base64 -d)\" in *cloud_agents*) printf '{\"claude\":\\n{\"account\":\"signed_in\",\"ready\":true}}\\n';; *) exit 1;; esac\n"
		}
		os.WriteFile(filepath.Join(bin, "fennec-terminal"), []byte(fake), 0o755)
		cmd := exec.Command("sh", "-c", cloudruntime.CloudAgentsScript())
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err, string(out))
		}
		got := parseCloudAgents(string(out))
		want := CloudAgentsState{Connected: true, Known: known, Agents: map[string]setup.CloudAgent{}}
		if known {
			want.Agents["claude"] = setup.CloudAgent{Account: "signed_in", Ready: true}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("known=%v: got %+v from %q", known, got, out)
		}
	}
}
