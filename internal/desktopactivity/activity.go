package desktopactivity

import (
	"encoding/json"
	"strconv"
)

type Terminal struct {
	Address    string            `json:"address"`
	State      string            `json:"state"`
	Parent     string            `json:"parent"`
	Labels     map[string]string `json:"labels"`
	Argv       []string          `json:"argv"`
	Foreground struct {
		Argv []string `json:"argv"`
	} `json:"foreground"`
	Status struct {
		App   string `json:"app"`
		State string `json:"state"`
	} `json:"status"`
	Turn uint64 `json:"turn"`
}
type Memory struct {
	Written *uint64 `json:"written,omitempty"`
	Word    string  `json:"word"`
	Seen    bool    `json:"seen"`
	Looked  *uint64 `json:"looked,omitempty"`
}
type Input struct {
	ObservedSeen map[string]uint64 `json:"observed_seen"`
	Resynced     bool              `json:"resynced"`
	Terminals    []Terminal        `json:"terminals"`
	Looking      string            `json:"looking"`
	Previous     map[string]Memory `json:"previous"`
}
type Presentation struct {
	GroupState string  `json:"group_state"`
	Program    Program `json:"program"`
	State      string  `json:"state"`
	Group      string  `json:"group"`
}
type Alert struct {
	Address string `json:"address"`
	Group   string `json:"group"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Visual  bool   `json:"visual"`
}
type Output struct {
	Previous  map[string]Memory       `json:"previous"`
	Terminals map[string]Presentation `json:"terminals"`
	Alerts    []Alert                 `json:"alerts"`
	Seen      map[string]uint64       `json:"seen"`
}

func blocked(w string) bool { return w == "blocked" || w == "error" }
func busy(w string) bool    { return w == "working" || blocked(w) }
func priority(w string) int {
	switch w {
	case "blocked":
		return 4
	case "done":
		return 3
	case "working":
		return 2
	case "idle":
		return 1
	}
	return 0
}
func Update(in Input) Output {
	out := Output{Previous: map[string]Memory{}, Terminals: map[string]Presentation{}, Seen: map[string]uint64{}}
	records := map[string]Terminal{}
	sessions := map[string]string{}
	for _, t := range in.Terminals {
		if t.State == "live" {
			records[t.Address] = t
			if n := t.Labels["fennec.session"]; n != "" {
				sessions[n] = t.Address
			}
		}
	}
	group := func(t Terminal) string {
		visited := map[string]bool{}
		for t.Parent != "" && !visited[t.Address] {
			visited[t.Address] = true
			p, ok := records[t.Parent]
			if !ok {
				return ""
			}
			t = p
		}
		if t.Labels["fennec.session"] != "" {
			return t.Address
		}
		return sessions[t.Labels["fennec.shell-of"]]
	}
	for addr, t := range records {
		g := group(t)
		if g == "" {
			continue
		}
		old, exists := in.Previous[addr]
		if !exists {
			old.Seen = true
		}
		m := old
		if m.Written != nil && (in.Resynced || in.ObservedSeen[addr] >= *m.Written) {
			m.Written = nil
		}
		looking := in.Looking == addr
		word := t.Status.State
		if word != old.Word {
			if word == "working" || word == "blocked" {
				m.Seen = true
			} else if old.Word == "working" || old.Word == "blocked" {
				m.Seen = looking
			}
		}
		m.Word = word
		if looking {
			m.Seen = true
		} else {
			m.Looked = nil
		}
		seen, hasSeen := t.Labels["herdr.seen"]
		seenTurn, err := strconv.ParseUint(seen, 10, 64)
		hasSeen = hasSeen && err == nil
		if m.Written != nil && (!hasSeen || *m.Written > seenTurn) {
			seenTurn = *m.Written
			hasSeen = true
		}
		if looking && word != "" && (m.Looked == nil || *m.Looked != t.Turn) {
			turn := t.Turn
			m.Looked = &turn
			if !hasSeen || seenTurn < turn {
				out.Seen[addr] = turn
				m.Written = &turn
				seenTurn = turn
				hasSeen = true
			}
		}
		state := ""
		switch {
		case word == "working":
			state = "working"
		case blocked(word):
			state = "blocked"
		case word == "":
		case hasSeen && t.Turn > seenTurn || !hasSeen && !m.Seen:
			state = "done"
		default:
			state = "idle"
		}
		argv := t.Foreground.Argv
		if len(argv) == 0 {
			argv = t.Argv
		}
		p := Resolve(t.Status.App, argv)
		out.Terminals[addr] = Presentation{Program: p, State: state, Group: g, GroupState: state}
		out.Previous[addr] = m
		if in.Previous != nil && t.Parent == "" {
			kind := ""
			if blocked(word) && !blocked(old.Word) {
				kind = "blocked"
			} else if busy(old.Word) && word != "" && !busy(word) && !looking {
				kind = "done"
			}
			if kind != "" {
				out.Alerts = append(out.Alerts, Alert{addr, g, kind, p.Name, !looking})
			}
		}
	}
	// Children and sibling tabs contribute attention without producing child alerts.
	for addr, p := range out.Terminals {
		if addr != p.Group {
			root := out.Terminals[p.Group]
			if priority(p.State) > priority(root.GroupState) {
				root.GroupState = p.State
				out.Terminals[p.Group] = root
			}
		}
	}
	return out
}

// Core accepts both legacy state strings and OSC 7501 report objects.
func (t *Terminal) UnmarshalJSON(data []byte) error {
	type plain Terminal
	var raw struct {
		*plain
		Status json.RawMessage `json:"status"`
	}
	raw.plain = (*plain)(t)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.Status) == 0 || string(raw.Status) == "null" {
		return nil
	}
	if raw.Status[0] == '"' {
		return json.Unmarshal(raw.Status, &t.Status.State)
	}
	return json.Unmarshal(raw.Status, &t.Status)
}
