package extensions_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fennec/cua/internal/extensions"
)

func names(set extensions.Set) []string {
	var out []string
	for _, p := range set.Plugins {
		out = append(out, p.Name)
	}
	for _, s := range set.Services {
		out = append(out, "svc:"+s.Name)
	}
	return out
}

// CAP-DMN-036: files load in file-name order; a file breaking any rule is
// skipped whole with one log line; non-JSON files are ignored.
func TestLoadPlugins(t *testing.T) {
	dir := t.TempDir()
	good := func(name string) map[string]any {
		return map[string]any{"name": name, "description": "d", "entry": "/usr/bin/true"}
	}
	writeSpec(t, dir, "b.json", good("bee_run"))
	writeSpec(t, dir, "a.json", good("ay_run"))
	writeSpec(t, dir, "c.json", good("ay_run")) // taken by a.json
	writeSpec(t, dir, "d.json", `{"name":"dee_run","description":"d","entry":"/usr/bin/true","extra":1}`)
	writeSpec(t, dir, "e.json", `{"name":"eee_run","description":"d","entry":"bin/relative"}`)
	writeSpec(t, dir, "f.json", map[string]any{"name": "eff_run", "description": strings.Repeat("é", extensions.MaxDescription+1), "entry": "/usr/bin/true"})
	writeSpec(t, dir, "g.json", `{"name":"gee_run","description":"d","entry":"/usr/bin/true","resources":[{"uri":"x://a","path":"/tmp/a","mime":"text/plain"}]}`)
	writeSpec(t, dir, "h.json", `not json`)
	writeSpec(t, dir, "i.json", `{"name":"observe","description":"d","entry":"/usr/bin/true"}`)
	writeSpec(t, dir, "j.json", map[string]any{"name": "max_run", "description": strings.Repeat("é", extensions.MaxDescription), "entry": "/usr/bin/true"})
	writeSpec(t, dir, "k.json", `{"name":"kay_run","description":"d","entry":"/usr/bin/true"} {}`)
	writeSpec(t, dir, "notes.txt", `{"name":"txt_run","description":"d","entry":"/usr/bin/true"}`)
	l := newLogs()
	set := extensions.Load(dir, l.f)
	if got := strings.Join(names(set), ","); got != "ay_run,bee_run,max_run" {
		t.Fatalf("loaded %s", got)
	}
	lines := l.all()
	if len(lines) != 8 {
		t.Fatalf("want one log line per skipped file (8), got %d: %q", len(lines), lines)
	}
	for i, f := range []string{"c.json", "d.json", "e.json", "f.json", "g.json", "h.json", "i.json", "k.json"} {
		if !strings.Contains(lines[i], filepath.Join(dir, f)) {
			t.Errorf("log line %d %q does not name %s", i, lines[i], f)
		}
	}
	if set.Plugins[0].Entry != "/usr/bin/true" || set.Plugins[0].File != filepath.Join(dir, "a.json") {
		t.Errorf("plugin = %+v", set.Plugins[0])
	}
	// A missing directory loads nothing and says nothing.
	l2 := newLogs()
	if set := extensions.Load(filepath.Join(dir, "missing"), l2.f); len(set.Plugins)+len(set.Services) != 0 || len(l2.all()) != 0 {
		t.Error("a missing directory must load nothing quietly")
	}
}

// CAP-DMN-036: name rules.
func TestPluginName(t *testing.T) {
	cases := map[string]bool{
		"a_run": true, "notes_run": true, "x9_y_run": true,
		"run": false, "_run": false, "9a_run": false, "A_run": false, "a-b_run": false,
		"notes": false, "notes_run2": false, "": false, "é_run": false,
	}
	for name, ok := range cases {
		dir := t.TempDir()
		writeSpec(t, dir, "p.json", map[string]any{"name": name, "description": "d", "entry": "/usr/bin/true"})
		set := extensions.Load(dir, nil)
		if got := len(set.Plugins) == 1; got != ok {
			t.Errorf("name %q: loaded %v, want %v", name, got, ok)
		}
	}
	// A built-in name is refused even where the pattern would pass for a
	// service tool.
	dir := t.TempDir()
	writeSpec(t, dir, "p.json", map[string]any{"name": "run", "description": "d", "entry": "/usr/bin/true"})
	if len(extensions.Load(dir, nil).Plugins) != 0 {
		t.Error("run is built in")
	}
}

// CAP-DMN-036: the directory is tools.d beside the configuration file, or
// the configured one relative to the file's directory; a broken
// configuration loads none.
func TestPluginDir(t *testing.T) {
	if got := extensions.Dir("/etc/cua/config.toml", ""); got != "/etc/cua/tools.d" {
		t.Errorf("default dir = %s", got)
	}
	if got := extensions.Dir("/etc/cua/config.toml", "plugins"); got != "/etc/cua/plugins" {
		t.Errorf("relative dir = %s", got)
	}
	if got := extensions.Dir("/etc/cua/config.toml", "/opt/p"); got != "/opt/p" {
		t.Errorf("absolute dir = %s", got)
	}
	base := shortDir(t)
	writeSpec(t, filepath.Join(base, "mine"), "p.json", map[string]any{"name": "mine_run", "description": "d", "entry": "/usr/bin/true"})
	writeSpec(t, filepath.Join(base, "tools.d"), "p.json", map[string]any{"name": "def_run", "description": "d", "entry": "/usr/bin/true"})
	opts := extensions.Options{ConfigPath: filepath.Join(base, "config.toml"), DaemonDir: filepath.Join(base, "d")}
	x, err := extensions.Open(context.Background(), opts)
	if err != nil || len(x.Set().Plugins) != 1 || x.Set().Plugins[0].Name != "def_run" {
		t.Fatalf("default dir: %v %+v", err, x.Set())
	}
	opts.Plugins = "mine"
	x, err = extensions.Open(context.Background(), opts)
	if err != nil || len(x.Set().Plugins) != 1 || x.Set().Plugins[0].Name != "mine_run" {
		t.Fatalf("configured dir: %v %+v", err, x.Set())
	}
	opts.ConfigBroken = true
	x, err = extensions.Open(context.Background(), opts)
	if err != nil || len(x.Set().Plugins) != 0 {
		t.Fatalf("broken configuration: %v %+v", err, x.Set())
	}
}

// CAP-DMN-038: service spec rules.
func TestLoadServices(t *testing.T) {
	obj := `{"type":"object"}`
	svc := func(service, tools string) string {
		return `{"service":"` + service + `","program":"/usr/bin/true","args":["-x"],"tools":[` + tools + `]}`
	}
	tool := func(name string) string {
		return `{"name":"` + name + `","description":"d","input_schema":` + obj + `}`
	}
	dir := t.TempDir()
	writeSpec(t, dir, "01.json", svc("notes", tool("notes_read")+`,{"name":"notes_watch","description":"d","input_schema":`+obj+`,"mcp":false}`))
	writeSpec(t, dir, "02.json", svc("notes", tool("other_tool")))                // duplicate service
	writeSpec(t, dir, "03.json", svc("files", tool("notes_read")))                // taken tool
	writeSpec(t, dir, "04.json", svc("Bad", tool("bad_tool")))                    // service name
	writeSpec(t, dir, "05.json", svc(strings.Repeat("s", 33), tool("long_tool"))) // service too long
	writeSpec(t, dir, "06.json", svc("pre", tool("desk_peek")))                   // reserved prefix
	writeSpec(t, dir, "07.json", svc("runner", tool("thing_run")))                // _run suffix
	writeSpec(t, dir, "08.json", svc("builtin", tool("observe")))                 // built in
	writeSpec(t, dir, "09.json", svc("empty", ""))                                // no tools
	writeSpec(t, dir, "10.json", svc("dup", tool("dup_a")+","+tool("dup_a")))     // duplicate in file
	writeSpec(t, dir, "11.json", svc("schema", `{"name":"s_t","description":"d","input_schema":{"type":"string"}}`))
	writeSpec(t, dir, "12.json", svc("unknown", `{"name":"u_t","description":"d","input_schema":`+obj+`,"extra":true}`))
	writeSpec(t, dir, "13.json", `{"service":"rel","program":"bin/x","tools":[`+tool("rel_t")+`]}`)
	writeSpec(t, dir, "14.json", svc("longtool", tool(strings.Repeat("t", 65))))
	writeSpec(t, dir, "15.json", svc(strings.Repeat("s", 32), tool(strings.Repeat("t", 64))))
	writeSpec(t, dir, "16.json", `{"name":"after_run","description":"d","entry":"/usr/bin/true"}`)
	writeSpec(t, dir, "17.json", `{"name":"notes_read_run","description":"d","entry":"/usr/bin/true"}`)
	l := newLogs()
	set := extensions.Load(dir, l.f)
	if got := strings.Join(names(set), ","); got != "after_run,notes_read_run,svc:notes,svc:"+strings.Repeat("s", 32) {
		t.Fatalf("loaded %s", got)
	}
	if n := len(l.all()); n != 13 {
		t.Errorf("want 13 skip lines, got %d", n)
	}
	s := set.Services[0]
	if s.Program != "/usr/bin/true" || len(s.Args) != 1 || len(s.Tools) != 2 || !s.Tools[0].MCP || s.Tools[1].MCP {
		t.Errorf("service = %+v", s)
	}
}

// CAP-DMN-038: a socket path too long fails the start, names the service,
// and creates no services directory.
func TestServiceSocketTooLong(t *testing.T) {
	base := shortDir(t)
	long := filepath.Join(base, strings.Repeat("d", 80))
	tools := filepath.Join(base, "tools.d")
	writeSpec(t, tools, "s.json", map[string]any{"service": "verylongservicename", "program": "/usr/bin/true", "tools": []any{map[string]any{"name": "v_t", "description": "d", "input_schema": map[string]string{"type": "object"}}}})
	_, err := extensions.Open(context.Background(), extensions.Options{ConfigPath: filepath.Join(base, "config.toml"), DaemonDir: long})
	if err == nil || !strings.Contains(err.Error(), "verylongservicename") || !strings.Contains(err.Error(), "shorter") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(long, "services")); !os.IsNotExist(err) {
		t.Error("the services directory was created")
	}
}

// CAP-RGA-090: a plugin whose resource URI duplicates an earlier one is
// skipped whole.
func TestDuplicateResourceURI(t *testing.T) {
	dir := t.TempDir()
	res := func(uri string) map[string]any {
		return map[string]any{"uri": uri, "path": "/tmp/r.md", "mime": "text/markdown", "description": "r"}
	}
	writeSpec(t, dir, "a.json", map[string]any{"name": "a_run", "description": "d", "entry": "/usr/bin/true", "resources": []any{res("doc://one")}})
	writeSpec(t, dir, "b.json", map[string]any{"name": "b_run", "description": "d", "entry": "/usr/bin/true", "resources": []any{res("doc://two"), res("doc://one")}})
	writeSpec(t, dir, "c.json", map[string]any{"name": "c_run", "description": "d", "entry": "/usr/bin/true", "resources": []any{res("doc://three"), res("doc://three")}})
	writeSpec(t, dir, "d.json", map[string]any{"name": "d_run", "description": "d", "entry": "/usr/bin/true", "resources": []any{res("doc://two")}})
	set := extensions.Load(dir, nil)
	if got := strings.Join(names(set), ","); got != "a_run,d_run" {
		t.Fatalf("loaded %s", got)
	}
	x := extensions.New(set, extensions.Options{})
	if r := x.Resources(); len(r) != 2 || r[0].URI != "doc://one" || r[1].URI != "doc://two" || r[0].Name != "r.md" {
		t.Errorf("resources = %+v", r)
	}
}
