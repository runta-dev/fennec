package desktopactivity

import (
	"encoding/json"
	"testing"
)

func terminal(a, word string) Terminal {
	t := Terminal{Address: a, State: "live", Labels: map[string]string{"fennec.session": a}, Argv: []string{"/bin/zsh"}}
	t.Status.State = word
	t.Status.App = "codex"
	return t
}
func TestAttentionTransitions(t *testing.T) {
	term := terminal("t1", "working")
	in := Input{Terminals: []Terminal{term}}
	first := Update(in)
	if len(first.Alerts) != 0 {
		t.Fatal("startup rang")
	}
	in.Previous = first.Previous
	in.Terminals[0].Status.State = "blocked"
	in.Looking = "t1"
	blocked := Update(in)
	if len(blocked.Alerts) != 1 || blocked.Alerts[0].Kind != "blocked" || blocked.Alerts[0].Visual {
		t.Fatal(blocked)
	}
	in.Previous = blocked.Previous
	repeat := Update(in)
	if len(repeat.Alerts) != 0 {
		t.Fatal("repeat rang")
	}
	in.Looking = ""
	in.Previous = repeat.Previous
	in.Terminals[0].Status.State = "idle"
	done := Update(in)
	if len(done.Alerts) != 1 || done.Alerts[0].Kind != "done" || !done.Alerts[0].Visual || done.Terminals["t1"].State != "done" {
		t.Fatal(done)
	}
	in.Previous = done.Previous
	in.Looking = "t1"
	seen := Update(in)
	if seen.Terminals["t1"].State != "idle" {
		t.Fatal(seen)
	}
}
func TestSharedSeenAndContinuousViewing(t *testing.T) {
	term := terminal("t1", "idle")
	term.Turn = 4
	term.Labels["herdr.seen"] = "2"
	in := Input{Terminals: []Terminal{term}}
	unread := Update(in)
	if unread.Terminals["t1"].State != "done" || len(unread.Alerts) != 0 {
		t.Fatal(unread)
	}
	in.Previous = unread.Previous
	in.Looking = "t1"
	seen := Update(in)
	if seen.Seen["t1"] != 4 || seen.Terminals["t1"].State != "idle" {
		t.Fatal(seen)
	}
	in.Previous = seen.Previous
	in.ObservedSeen = map[string]uint64{"t1": 4}
	again := Update(in)
	if len(again.Seen) != 0 || again.Terminals["t1"].State != "done" {
		t.Fatal("must respect another client's unread without rewriting", again)
	}
	in.Previous = again.Previous
	in.Looking = ""
	away := Update(in)
	in.Previous = away.Previous
	in.Looking = "t1"
	if Update(in).Seen["t1"] != 4 {
		t.Fatal("return must acknowledge")
	}
}
func TestOwnershipChildrenAndShellTabs(t *testing.T) {
	root := terminal("t1", "")
	root.Status.App = ""
	child := terminal("t2", "working")
	child.Parent = "t1"
	child.Labels = nil
	shell := terminal("t3", "")
	shell.Labels = map[string]string{"fennec.shell-of": "t1"}
	shell.Status.App = ""
	shell.Foreground.Argv = []string{"node", "/bin/codex.js"}
	foreign := terminal("t4", "working")
	foreign.Labels = nil
	in := Input{Terminals: []Terminal{root, child, shell, foreign}}
	first := Update(in)
	if first.Terminals["t3"].Program.Key != "codex" || first.Terminals["t1"].GroupState != "working" || len(first.Terminals) != 3 {
		t.Fatal(first)
	}
	in.Previous = first.Previous
	in.Terminals[1].Status.State = "blocked"
	in.Terminals[3].Status.State = "blocked"
	next := Update(in)
	if len(next.Alerts) != 0 || next.Terminals["t1"].GroupState != "blocked" {
		t.Fatal(next)
	}
}
func TestPrograms(t *testing.T) {
	cases := []struct {
		app       string
		argv      []string
		key, name string
	}{
		{"claude-code", []string{"zsh"}, "claude", "Claude Code"}, {"agent", nil, "cursor", "Cursor"},
		{"grok-build", nil, "grok", "Grok"}, {"my-agent", nil, "agent", "my-agent"},
		{"", []string{"node", "/opt/codex.js"}, "codex", "Codex"}, {"", []string{"/bin/zsh"}, "shell", "zsh"},
		{"", []string{"zsh", "-lic", `"$@"; exec "$0" -i`, "zsh", "pi"}, "pi", "Pi"},
		{"", []string{"pwsh.exe", "-NoExit", "-Command", "& 'codex' '--help'"}, "codex", "Codex"},
		{"", []string{"not-codex"}, "program", "not-codex"},
	}
	for _, c := range cases {
		p := Resolve(c.app, c.argv)
		if p.Key != c.key || p.Name != c.name {
			t.Errorf("%+v: %+v", c, p)
		}
	}
}

func TestRawCoreReports(t *testing.T) {
	for _, raw := range []string{`{"address":"t1","state":"live","status":"working","labels":{"fennec.session":"test"}}`, `{"address":"t1","state":"live","status":{"app":"codex","state":"working"},"labels":{"fennec.session":"test"}}`} {
		var term Terminal
		if err := json.Unmarshal([]byte(raw), &term); err != nil {
			t.Fatal(err)
		}
		out := Update(Input{Terminals: []Terminal{term}})
		if out.Terminals["t1"].State != "working" {
			t.Fatal(out)
		}
	}
}
func TestNewBlockedAgentAndReconnect(t *testing.T) {
	out := Update(Input{Terminals: []Terminal{terminal("t1", "idle")}})
	next := Update(Input{Previous: out.Previous, Terminals: []Terminal{terminal("t1", "idle"), terminal("t2", "blocked")}})
	if len(next.Alerts) != 1 || next.Alerts[0].Address != "t2" {
		t.Fatal(next)
	}
	again := Update(Input{Previous: next.Previous, Resynced: true, Terminals: []Terminal{terminal("t1", "idle"), terminal("t2", "blocked")}})
	if len(again.Alerts) != 0 {
		t.Fatal(again)
	}
}

func TestLocalAndCloudIdentitiesStaySeparate(t *testing.T) {
	local := terminal("t1", "idle")
	local.Labels["fennec.session"] = "same-name"
	remote := terminal("cloud:t1", "working")
	remote.Source = "cloud"
	remote.Labels["fennec.session"] = "same-name"
	localTab := terminal("t2", "blocked")
	localTab.Labels = map[string]string{"fennec.shell-of": "same-name"}
	remoteTab := terminal("cloud:t2", "idle")
	remoteTab.Source = "cloud"
	remoteTab.Labels = map[string]string{"fennec.shell-of": "same-name"}
	attached := terminal("t3", "")
	attached.Labels = map[string]string{"fennec.shell-of": "cloud:same-name"}
	attached.DesktopGroup = "cloud:t1"
	out := Update(Input{Terminals: []Terminal{local, remote, localTab, remoteTab, attached}})
	if len(out.Terminals) != 5 || out.Terminals["t2"].Group != "t1" || out.Terminals["cloud:t2"].Group != "cloud:t1" || out.Terminals["t3"].Group != "cloud:t1" {
		t.Fatalf("source identity crossed: %+v", out)
	}
	if out.Terminals["t1"].GroupState != "blocked" || out.Terminals["cloud:t1"].GroupState != "working" {
		t.Fatal("attention leaked between sources")
	}
}
func TestCloudInitialSnapshotDoesNotReplayNotifications(t *testing.T) {
	local := terminal("t1", "working")
	previous := Update(Input{Terminals: []Terminal{local}}).Previous
	local.Status.State = "blocked"
	remote := terminal("cloud:t1", "blocked")
	remote.Source = "cloud"
	out := Update(Input{Terminals: []Terminal{local, remote}, Previous: previous, QuietPrefixes: []string{"cloud:"}})
	if len(out.Alerts) != 1 || out.Alerts[0].Address != "t1" {
		t.Fatalf("cloud initialization affected alerts: %+v", out.Alerts)
	}
}
