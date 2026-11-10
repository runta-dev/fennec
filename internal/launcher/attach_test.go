package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachUsesPinnedIdentityAndHerdr(t *testing.T) {
	args, err := AttachArgs("01a1b016-11bc-7e63-b3d6-c4955e17b73b", "t9", strings.Repeat("a", 32), "/tmp/fennec cloud")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, required := range []string{"StrictHostKeyChecking=yes", "ForwardAgent=no", "ClearAllForwardings=yes", "/tmp/fennec cloud/runtime_key", "-tt"} {
		if !strings.Contains(joined, required) {
			t.Fatal(required)
		}
	}
}
func TestAttachRejectsInjectedSession(t *testing.T) {
	for _, address := range []string{"t1;id", "remote/t1", "-oProxyCommand=bad"} {
		if _, err := AttachArgs("01a1b016-11bc-7e63-b3d6-c4955e17b73b", address, strings.Repeat("a", 32), "/tmp"); err == nil {
			t.Fatal(address)
		}
	}
}

func TestFinishedAttachReturnsExitAndRejectsOtherSession(t *testing.T) {
	for _, tc := range []struct {
		name, label, code string
		expected          int
		read              bool
	}{
		{"failed-agent", strings.Repeat("a", 32), "7", 7, true},
		{"other-session", strings.Repeat("b", 32), "0", 1, false},
		{"unknown-exit", strings.Repeat("a", 32), "null", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			installAttachTestBinary(t, home)
			bin := filepath.Join(home, ".local/bin")
			os.MkdirAll(bin, 0700)
			fake := `#!/usr/bin/env python3
import os,sys,json,pathlib
if sys.argv[1]=='get':
 print(json.dumps({'state':'finished','exit_code':json.loads(os.environ['TEST_EXIT']),'labels':{'fennec.session':os.environ['TEST_LABEL']}}))
elif sys.argv[1]=='read':
 pathlib.Path.home().joinpath('read-called').touch()
 print('RETAINED_OUTPUT')
else:raise SystemExit('unexpected attach')
`
			os.WriteFile(filepath.Join(bin, "herdr-core"), []byte(fake), 0700)
			args, err := AttachArgs("01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", "t1", strings.Repeat("a", 32), home)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", args[len(args)-1])
			cmd.Env = append(os.Environ(), "HOME="+home, "TEST_EXIT="+tc.code, "TEST_LABEL="+tc.label)
			output, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				code = cmd.ProcessState.ExitCode()
			}
			if code != tc.expected {
				t.Fatalf("exit %d: %s", code, output)
			}
			_, statErr := os.Stat(filepath.Join(home, "read-called"))
			if (statErr == nil) != tc.read {
				t.Fatal("unexpected output access", string(output))
			}
			if tc.read && !strings.Contains(string(output), "RETAINED_OUTPUT") {
				t.Fatal("lost output")
			}
		})
	}
}

func TestAttachRaceReadsFinishedResultWithoutRelaunch(t *testing.T) {
	home := t.TempDir()
	installAttachTestBinary(t, home)
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	fake := `#!/usr/bin/env python3
import pathlib,sys,json
marker=pathlib.Path.home()/'attached'
if sys.argv[1]=='get':print(json.dumps({'state':'finished' if marker.exists() else 'live','exit_code':7,'labels':{'fennec.session':'a'*32}}))
elif sys.argv[1]=='attach':marker.touch();raise SystemExit(11)
elif sys.argv[1]=='read':print('RACE_OUTPUT')
else:raise SystemExit(99)
`
	os.WriteFile(filepath.Join(bin, "herdr-core"), []byte(fake), 0700)
	args, err := AttachArgs("01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", "t1", strings.Repeat("a", 32), home)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", args[len(args)-1])
	cmd.Env = append(os.Environ(), "HOME="+home)
	output, err := cmd.CombinedOutput()
	if err == nil || cmd.ProcessState.ExitCode() != 7 || strings.Count(string(output), "RACE_OUTPUT") != 1 {
		t.Fatalf("lost finished result: %v %s", err, output)
	}
}

func installAttachTestBinary(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", path, "../../cmd/fennec-terminal").CombinedOutput(); err != nil {
		t.Fatalf("build terminal: %v %s", err, output)
	}
}
