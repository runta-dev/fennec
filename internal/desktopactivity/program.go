// Package desktopactivity adapts Herdr's program and attention presentation to Fennec.
// Reference: herdr-app 317e673, Apache-2.0, src/programs.rs and src/model.rs.
package desktopactivity

import (
	_ "embed"
	"encoding/json"
	"path"
	"sort"
	"strings"
)

//go:embed assets/programs.json
var programJSON []byte

type Program struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Tint  bool   `json:"tint"`
	Tile  string `json:"tile"`
	Color string `json:"color"`
	Shell bool   `json:"shell"`
	Agent bool   `json:"agent"`
}
type entry struct {
	Name, Icon, Tile, Color string
	Tint                    *bool
	Match                   []string
}

var table struct {
	Shells, Interpreters []string
	Programs             map[string]entry
}
var keys []string

func init() {
	if err := json.Unmarshal(programJSON, &table); err != nil {
		panic(err)
	}
	for k := range table.Programs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
}
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
func base(s string) string {
	s = path.Base(strings.ReplaceAll(s, "\\", "/"))
	s = strings.TrimLeft(s, "-")
	if strings.HasSuffix(strings.ToLower(s), ".exe") {
		s = s[:len(s)-4]
	}
	return s
}
func find(s string) (string, bool) {
	var alias string
	for _, k := range keys {
		for _, a := range table.Programs[k].Match {
			if strings.EqualFold(s, a) && alias == "" {
				alias = k
			}
		}
	}
	for _, k := range keys {
		if strings.EqualFold(s, k) {
			if k == "agent" && alias != "" {
				return alias, true
			}
			return k, true
		}
	}
	return alias, alias != ""
}
func shown(k, name string, shell, agent bool) Program {
	e := table.Programs[k]
	if name == "" {
		name = e.Name
	}
	if name == "" {
		name = k
	}
	return Program{k, name, e.Icon, e.Tint == nil || *e.Tint, e.Tile, e.Color, shell, agent}
}
func Resolve(app string, argv []string) Program {
	if app != "" {
		if k, ok := find(app); ok {
			return shown(k, "", false, true)
		}
		return shown("agent", app, false, true)
	}
	if len(argv) > 4 && contains(table.Shells, base(argv[0])) && strings.HasPrefix(argv[1], "-") && strings.HasSuffix(argv[1], "c") && (strings.Contains(argv[2], `"$@"`) || strings.Contains(argv[2], "command $argv[2..]")) {
		return Resolve("", argv[4:])
	}
	if len(argv) == 4 && contains(table.Shells, base(argv[0])) && argv[1] == "-NoExit" && argv[2] == "-Command" {
		if words := powershellWords(argv[3]); words != nil {
			return Resolve("", words)
		}
	}
	if len(argv) == 0 {
		return shown("shell", "", true, false)
	}
	first := base(argv[0])
	if contains(table.Interpreters, first) {
		for _, arg := range argv[1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			name := base(arg)
			for _, ext := range []string{".js", ".mjs", ".cjs", ".ts", ".py", ".rb"} {
				if strings.HasSuffix(name, ext) {
					name = strings.TrimSuffix(name, ext)
					break
				}
			}
			if k, ok := find(name); ok && !contains(table.Interpreters, k) {
				return shown(k, "", false, false)
			}
		}
	}
	if k, ok := find(first); ok {
		return shown(k, "", false, false)
	}
	if contains(table.Shells, first) {
		return shown("shell", first, true, false)
	}
	p := shown("program", first, false, false)
	h := uint32(2166136261)
	for _, b := range []byte(first) {
		h = (h ^ uint32(b)) * 16777619
	}
	p.Tile = []string{"#6d5bd0", "#2f80ed", "#0f9d8a", "#d9822b", "#c2417d", "#3a7d44", "#8a5a44", "#4e5d94"}[h%8]
	p.Color = "#ffffff"
	return p
}
func powershellWords(s string) []string {
	if !strings.HasPrefix(s, "& ") {
		return nil
	}
	r := []rune(s[2:])
	var out []string
	quote := func(c rune) bool { return strings.ContainsRune("'‘’‚‛", c) }
	for i := 0; i < len(r); {
		if r[i] == ' ' {
			i++
			continue
		}
		if !quote(r[i]) {
			return nil
		}
		i++
		var b strings.Builder
		for i < len(r) {
			c := r[i]
			i++
			if quote(c) {
				if i < len(r) && quote(r[i]) {
					b.WriteRune(r[i])
					i++
					continue
				}
				break
			}
			b.WriteRune(c)
		}
		out = append(out, b.String())
	}
	return out
}
