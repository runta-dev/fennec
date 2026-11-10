package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResumeHistoryUsesSavedConversationAndDoesNotDuplicate(t *testing.T) {
	home := t.TempDir()
	installAttachTestBinary(t, home)
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	os.MkdirAll(filepath.Join(home, ".codex"), 0700)
	os.MkdirAll(filepath.Join(home, "workspace"), 0700)
	fixture := `#!/usr/bin/env python3
import json,sys,pathlib
home=pathlib.Path.home()
id='a'*32
live=home/'live.json'
base={'labels':{'fennec.session':id,'fennec.agent':'codex'},'cwd':str(home/'workspace')}
if sys.argv[1]=='get':
 print(json.dumps(json.loads(live.read_text()) if sys.argv[2]=='t2' and live.exists() else dict(base,address='t1',state='finished',exit_code=0)))
elif sys.argv[1]=='ls':print(json.dumps({'terminals':[json.loads(live.read_text())] if live.exists() else []}))
elif sys.argv[1]=='run':
 assert sys.argv[-2:] == ['resume','01a1b3a2-29fe-7b32-9a1b-3848c0b6b538']
 with (home/'launches').open('a') as output:output.write('run\n')
 value=dict(base,address='t2',state='live')
 live.write_text(json.dumps(value));print(json.dumps(value))
elif sys.argv[1]=='attach':print('ATTACHED')
else:raise SystemExit(1)
`
	if err := os.WriteFile(filepath.Join(bin, "herdr-core"), []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	setup := exec.Command("python3", "-c", `import sqlite3,pathlib
home=pathlib.Path.home()
connection=sqlite3.connect(home/'.codex/state_5.sqlite')
connection.execute('create table threads(id text,cwd text,archived integer)')
connection.execute('insert into threads values(?,?,0)',('01a1b3a2-29fe-7b32-9a1b-3848c0b6b538',str(home/'workspace')))
connection.commit()
`)
	setup.Env = append(os.Environ(), "HOME="+home)
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	for attempt := 0; attempt < 2; attempt++ {
		command := exec.Command("sh", "-c", resumeHistoryCommand("t1", strings.Repeat("a", 32)))
		command.Env = append(os.Environ(), "HOME="+home)
		output, err := command.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "ATTACHED") || !strings.Contains(string(output), "fen resume "+strings.Repeat("a", 32)) {
			t.Fatal(err, string(output))
		}
	}
	launches, err := os.ReadFile(filepath.Join(home, "launches"))
	if err != nil || string(launches) != "run\n" {
		t.Fatal("resume duplicated process", string(launches), err)
	}
	if err := os.Remove(filepath.Join(home, "live.json")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, ".local/fennec/resume-locks", strings.Repeat("a", 32)+".json")
	if err := os.WriteFile(marker, []byte(`{"state":"pending"}`), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", resumeHistoryCommand("t1", strings.Repeat("a", 32)))
	command.Env = append(os.Environ(), "HOME="+home)
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "Previous resume outcome is unknown") {
		t.Fatal("uncertain resume was replayed", err, string(output))
	}
	launches, _ = os.ReadFile(filepath.Join(home, "launches"))
	if string(launches) != "run\n" {
		t.Fatal("uncertain resume created duplicate")
	}
}
