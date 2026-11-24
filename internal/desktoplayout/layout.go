// Package desktoplayout owns desktop tab placement independently of terminals.
package desktoplayout

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
)

type Tab struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Group string `json:"group"`
}
type Group struct {
	ID     string   `json:"id"`
	Tabs   []string `json:"tabs"`
	Active string   `json:"active"`
	Custom bool     `json:"custom"`
}
type State struct {
	Revision uint64  `json:"revision"`
	Groups   []Group `json:"groups"`
}
type Input struct {
	Tabs      []Tab  `json:"tabs"`
	Operation string `json:"operation"`
	Tab       string `json:"tab"`
	Target    string `json:"target"`
	Before    string `json:"before"`
	Revision  uint64 `json:"revision"`
}
type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

func New(home string) *Store {
	s := &Store{path: filepath.Join(home, ".config/fennec/desktop-layout.json")}
	b, e := os.ReadFile(s.path)
	if e == nil {
		_ = json.Unmarshal(b, &s.state)
	}
	return s
}

// Project retains absent identities on disk, so a reconnect never dissolves groups.
func project(saved State, tabs []Tab) State {
	out := State{Revision: saved.Revision, Groups: []Group{}}
	byID := map[string]Tab{}
	claimed := map[string]bool{}
	defaults := map[string]string{}
	for _, t := range tabs {
		byID[t.ID] = t
	}
	for _, g := range saved.Groups {
		p := Group{ID: g.ID, Custom: g.Custom}
		for _, id := range g.Tabs {
			if t, ok := byID[id]; ok && !claimed[id] {
				p.Tabs = append(p.Tabs, t.Key)
				claimed[id] = true
				if defaults[t.Group] == "" || g.ID == t.Group {
					defaults[t.Group] = g.ID
				}
				if id == g.Active {
					p.Active = t.Key
				}
			}
		}
		if len(p.Tabs) > 0 {
			if p.Active == "" {
				p.Active = p.Tabs[0]
			}
			out.Groups = append(out.Groups, p)
		}
	}
	for _, t := range tabs {
		if claimed[t.ID] {
			continue
		}
		claimed[t.ID] = true
		group := t.Group
		if mapped := defaults[group]; mapped != "" {
			group = mapped
		}
		at := slices.IndexFunc(out.Groups, func(g Group) bool { return g.ID == group })
		if at < 0 {
			out.Groups = append(out.Groups, Group{ID: group, Active: t.Key})
			at = len(out.Groups) - 1
		}
		out.Groups[at].Tabs = append(out.Groups[at].Tabs, t.Key)
	}
	return out
}
func (s *Store) Apply(in Input) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(in.Tabs) > 4096 {
		return State{}, errors.New("too many tabs")
	}
	keys := map[string]string{}
	ids := map[string]bool{}
	for _, t := range in.Tabs {
		if t.ID == "" || t.Key == "" || t.Group == "" || len(t.ID) > 512 || len(t.Key) > 512 || len(t.Group) > 512 || keys[t.Key] != "" || ids[t.ID] {
			return State{}, errors.New("invalid tab snapshot")
		}
		keys[t.Key] = t.ID
		ids[t.ID] = true
	}
	view := project(s.state, in.Tabs)
	if in.Operation == "" {
		return view, nil
	}
	if in.Revision != s.state.Revision {
		return view, errors.New("layout changed; refresh and try again")
	}
	from := slices.IndexFunc(view.Groups, func(g Group) bool { return slices.Contains(g.Tabs, in.Tab) })
	if from < 0 {
		return view, errors.New("tab is no longer available")
	}
	target := slices.IndexFunc(view.Groups, func(g Group) bool { return g.ID == in.Target })
	switch in.Operation {
	case "activate":
		view.Groups[from].Active = in.Tab
	case "move", "detach":
		if in.Operation == "detach" {
			if len(view.Groups[from].Tabs) == 1 {
				return view, nil
			}
			view.Groups = append(view.Groups, Group{ID: "detached:" + keys[in.Tab] + ":" + strconv.FormatUint(s.state.Revision+1, 10), Custom: true})
			target = len(view.Groups) - 1
		} else if target < 0 {
			return view, errors.New("target group is no longer available")
		}
		if in.Before == in.Tab && from == target {
			return view, nil
		}
		if in.Before != "" && !slices.Contains(view.Groups[target].Tabs, in.Before) {
			return view, errors.New("insertion target is no longer available")
		}
		view.Groups[from].Tabs = slices.DeleteFunc(view.Groups[from].Tabs, func(k string) bool { return k == in.Tab })
		for _, i := range []int{from, target} {
			view.Groups[i].Custom = true
		}
		if view.Groups[from].Active == in.Tab {
			view.Groups[from].Active = ""
			if len(view.Groups[from].Tabs) > 0 {
				view.Groups[from].Active = view.Groups[from].Tabs[0]
			}
		}
		at := len(view.Groups[target].Tabs)
		if in.Before != "" {
			at = slices.Index(view.Groups[target].Tabs, in.Before)
		}
		view.Groups[target].Tabs = slices.Insert(view.Groups[target].Tabs, at, in.Tab)
		view.Groups[target].Active = in.Tab
	default:
		return view, errors.New("unknown layout operation")
	}
	// Keep disconnected members in their existing group. Only the chosen tab moves.
	next := State{Revision: s.state.Revision + 1, Groups: []Group{}}
	for _, g := range view.Groups {
		stored := Group{ID: g.ID, Active: keys[g.Active], Custom: g.Custom}
		for _, key := range g.Tabs {
			stored.Tabs = append(stored.Tabs, keys[key])
		}
		if old := slices.IndexFunc(s.state.Groups, func(x Group) bool { return x.ID == g.ID }); old >= 0 {
			for _, id := range s.state.Groups[old].Tabs {
				if !ids[id] {
					stored.Tabs = append(stored.Tabs, id)
				}
			}
		}
		if len(stored.Tabs) > 0 {
			next.Groups = append(next.Groups, stored)
		}
	}
	for _, g := range s.state.Groups {
		if slices.IndexFunc(view.Groups, func(x Group) bool { return x.ID == g.ID }) < 0 {
			next.Groups = append(next.Groups, g)
		}
	}
	data, err := json.Marshal(next)
	if err != nil {
		return view, err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return view, err
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".layout-*")
	if err != nil {
		return view, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), s.path)
	}
	if err != nil {
		return view, err
	}
	s.state = next
	return project(next, in.Tabs), nil
}
