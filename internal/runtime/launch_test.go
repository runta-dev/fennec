package runtime

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLaunchValidation(t *testing.T) {
	for _, input := range [][3]string{{"../x", "codex", "/tmp"}, {strings.Repeat("a", 32), "sh", "/tmp"}, {strings.Repeat("a", 32), "pi", "relative"}, {strings.Repeat("a", 32), "claude", "/tmp\ncmd"}} {
		if _, err := LaunchScript(input[0], input[1], input[2]); err == nil {
			t.Fatal(input)
		}
	}
}
func TestLaunchUsesHerdrAndReplayGuard(t *testing.T) {
	script, err := LaunchScript(strings.Repeat("a", 32), "codex", "/tmp/project with spaces")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"herdr-core", "--outlive-parent", "fennec.session=", "launch outcome unknown", "fcntl.LOCK_EX", "cloud working directory does not exist"} {
		if !strings.Contains(script, required) {
			t.Fatal(required)
		}
	}
}

func TestCloudLaunchDelegatesStructuredContextToGo(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/fennec/bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "fennec-terminal"), []byte("#!/bin/sh\n[ \"$1\" = launch ] || exit 1\ncat\n"), 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"literal $(touch should-not-exist)", "a'b", "line\nbreak"}
	script, err := CloudLaunchScript("sunny-spicy", "codex", "/local/project with spaces", "1234abcd", args)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "python") || strings.Contains(script, "local-context") {
		t.Fatal("legacy cloud path retained")
	}
	command := exec.Command("sh", "-s")
	command.Env = append(os.Environ(), "HOME="+home)
	command.Stdin = strings.NewReader(script)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var request LocalLaunch
	if json.Unmarshal(output, &request) != nil || request.ID != "sunny-spicy" || request.LocalDirectory != "/local/project with spaces" || !reflect.DeepEqual(request.Arguments, args) {
		t.Fatal("context or arguments changed")
	}
	attached, err := CloudLaunchAttachScript(request.ID, request.Agent, request.LocalDirectory, request.Device, args)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(attached)
	payload, err := base64.StdEncoding.DecodeString(parts[len(parts)-1])
	if err != nil {
		t.Fatal(err)
	}
	var decoded LocalLaunch
	if json.Unmarshal(payload, &decoded) != nil || !reflect.DeepEqual(decoded, request) {
		t.Fatal("attach context differs")
	}
}

func TestLaunchArgumentsRemainStructured(t *testing.T) {
	args := []string{"--model", "provider/model", "literal $(touch /tmp/no)", "line\nbreak", "--cwd", "/somewhere else"}
	script, err := LaunchScriptWithArgs(strings.Repeat("a", 32), "codex", "/tmp", args)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.Split(strings.Split(script, "python3 - '")[1], "'")[0]
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Command []string `json:"command"`
	}
	if json.Unmarshal(data, &payload) != nil {
		t.Fatal("invalid payload")
	}
	want := append([]string{"codex", "--no-daemon", "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`}, args...)
	if !reflect.DeepEqual(payload.Command, want) {
		t.Fatalf("%q", payload.Command)
	}
	if !strings.Contains(script, "'--']+command") {
		t.Fatal("agent flags may be parsed by Herdr")
	}
	if _, err = LaunchScriptWithArgs(strings.Repeat("a", 32), "pi", "/tmp", []string{"bad\x00arg"}); err == nil {
		t.Fatal("NUL accepted")
	}
}

func TestVersionMismatchPreventsLaunch(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	os.WriteFile(filepath.Join(bin, "herdr-core"), []byte("#!/bin/sh\nif [ \"$1\" = ls ]; then echo '{\"terminals\":[]}'; else touch \"$HOME/launched\"; fi\n"), 0700)
	os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\necho 'opencode v1.18.18'\n"), 0700)
	script, err := LaunchScriptWithVersion(strings.Repeat("e", 32), "opencode", home, nil, "2.1.4")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(script)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "cloud agent version differs") {
		t.Fatalf("expected mismatch: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(home, "launched")); !os.IsNotExist(err) {
		t.Fatal("mismatched agent launched")
	}
}

func TestLaunchPinsVerifiedExecutable(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	original := filepath.Join(home, "original-codex")
	replacement := filepath.Join(home, "replacement-codex")
	os.WriteFile(original, []byte("#!/bin/sh\necho 'codex-cli 0.166.0'\n"), 0700)
	os.WriteFile(replacement, []byte("#!/bin/sh\necho 'codex-cli 0.165.0'\n"), 0700)
	os.Symlink(original, filepath.Join(bin, "codex"))
	core := `#!/usr/bin/env python3
import sys,json,pathlib
if sys.argv[1]=='ls':print('{"terminals":[]}')
else:
 pathlib.Path.home().joinpath('argv.json').write_text(json.dumps(sys.argv))
 print('{"address":"t1","state":"live"}')
`
	os.WriteFile(filepath.Join(bin, "herdr-core"), []byte(core), 0700)
	// Simulate an entrypoint changing after verification; Herdr must receive
	// the resolved version path, never the mutable alias.
	os.WriteFile(filepath.Join(bin, "magpie"), []byte("#!/bin/sh\nln -sfn \"$HOME/replacement-codex\" \"$HOME/.local/bin/codex\"\necho 'Codex model test/model'\n"), 0700)
	hook := "import urllib.request,io\nurllib.request.urlopen=lambda *a,**k:io.BytesIO(b'{\"data\":[]}')\n"
	os.WriteFile(filepath.Join(home, "sitecustomize.py"), []byte(hook), 0600)
	script, err := LaunchScriptWithVersion(strings.Repeat("f", 32), "codex", home, nil, "0.166.0")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Env = append(os.Environ(), "HOME="+home, "PYTHONPATH="+home)
	cmd.Stdin = strings.NewReader(script)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(home, "argv.json"))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if json.Unmarshal(data, &args) != nil {
		t.Fatal("invalid args")
	}
	original, err = filepath.EvalSymlinks(original)
	if err != nil {
		t.Fatal(err)
	}
	for i, arg := range args {
		if arg == "--" {
			if i+1 >= len(args) || filepath.Clean(args[i+1]) != filepath.Clean(original) {
				t.Fatalf("unpinned command %q", args)
			}
			return
		}
	}
	t.Fatal("missing Herdr argument boundary")
}
