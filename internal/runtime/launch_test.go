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

func TestHerdrLaunchRetriesDoNotDuplicate(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	fake := `#!/usr/bin/env python3
import json,sys,pathlib,os
root=pathlib.Path.home()
state=root/'terminal.json'
if sys.argv[1]=='ls':
 print(json.dumps({'terminals':[json.loads(state.read_text())] if state.exists() else []}))
elif sys.argv[1]=='run':
 with open(root/'launch-count','a') as f:f.write('run\n')
 value={'address':'t7','state':'live','status':'working'}
 state.write_text(json.dumps(value))
 print(json.dumps(value))
else:raise SystemExit(1)
`
	os.WriteFile(filepath.Join(bin, "herdr-core"), []byte(fake), 0700)
	os.WriteFile(filepath.Join(bin, "magpie"), []byte("#!/bin/sh\nprintf 'Codex model test/model\\n'\n"), 0700)
	hook := `import urllib.request,io
urllib.request.urlopen=lambda *a,**k:io.BytesIO(b'{"data":[]}')
`
	os.WriteFile(filepath.Join(home, "sitecustomize.py"), []byte(hook), 0600)
	run := func(agent string) error {
		script, err := CloudLaunchScript(strings.Repeat("b", 32), agent, "/local/project with spaces", "1234abcd", nil)
		if err != nil {
			return err
		}
		cmd := exec.Command("sh", "-s")
		cmd.Env = append(os.Environ(), "HOME="+home, "PYTHONPATH="+home)
		cmd.Stdin = strings.NewReader(script)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("script failure: %s", output)
		}
		return err
	}
	os.WriteFile(filepath.Join(bin, "magpie"), []byte("#!/bin/sh\nprintf 'Codex model default\\n'\n"), 0700)
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	if err := run("codex"); err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(home, ".local/fennec/sessions", strings.Repeat("b", 32), ".fennec-local-context.json")
	contextData, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	var localContext map[string]any
	if json.Unmarshal(contextData, &localContext) != nil || localContext["local_directory"] != "/local/project with spaces" || localContext["ssh_host"] != "fennec-device-1234abcd" || localContext["files_synchronized"] != false {
		t.Fatal("cloud context changed", string(contextData))
	}
	if err := run("codex"); err != nil {
		t.Fatal(err)
	}
	if err := run("pi"); err == nil {
		t.Fatal("same session accepted different agent")
	}
	data, _ := os.ReadFile(filepath.Join(home, "launch-count"))
	if string(data) != "run\n" {
		t.Fatalf("duplicate launches: %q", data)
	}
	os.Remove(filepath.Join(home, "terminal.json"))
	if err := run("codex"); err == nil {
		t.Fatal("unknown launch outcome replayed")
	}
	data, _ = os.ReadFile(filepath.Join(home, "launch-count"))
	if string(data) != "run\n" {
		t.Fatal("replayed after missing terminal")
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
