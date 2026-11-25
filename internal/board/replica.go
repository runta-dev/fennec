package board

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// A mutation changes fields of one object, never overwrites a whole board.
// Versions are Lamport clocks with replica IDs as deterministic tie breakers.
type Version struct {
	Clock  uint64 `json:"clock"`
	Origin string `json:"origin"`
}
type Mutation struct {
	Version
	Object string                     `json:"object"`
	Fields map[string]json.RawMessage `json:"fields"`
}
type ReplicaState struct {
	ID     string            `json:"id"`
	Clock  uint64            `json:"clock"`
	Events []Mutation        `json:"events"`
	Seen   map[string]uint64 `json:"seen"`
}
type Exchange struct {
	Missing []MissingAttachment `json:"missing,omitempty"`
	Seen    map[string]uint64   `json:"seen"`
	Events  []Mutation          `json:"events"`
	More    bool                `json:"more,omitempty"`
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (s Store) replicaPath() string { return filepath.Join(s.root(), "replica.json") }
func (s Store) replica() (ReplicaState, error) {
	var state ReplicaState
	err := readJSON(s.replicaPath(), &state)
	if err == ErrNotFound {
		state = ReplicaState{ID: randomID(), Seen: map[string]uint64{}}
		err = nil
	}
	if state.Seen == nil {
		state.Seen = map[string]uint64{}
	}
	return state, err
}
func newer(a, b Version) bool { return a.Clock > b.Clock || a.Clock == b.Clock && a.Origin > b.Origin }
func validObject(object string) bool {
	if object == "routing.json" || object == "local-control.json" {
		return true
	}
	parts := strings.Split(object, "/")
	if len(parts) == 3 && parts[0] == "projects" && projectID.MatchString(parts[1]) && parts[2] == "project.json" {
		return true
	}
	if len(parts) == 4 && parts[0] == "projects" && projectID.MatchString(parts[1]) && parts[2] == "requests" {
		var n int
		_, err := fmt.Sscanf(parts[3], "%d.json", &n)
		return err == nil && n > 0 && parts[3] == fmt.Sprintf("%d.json", n)
	}
	return false
}

// commit runs under the board lock. The journal is the durable source of truth;
// JSON objects are materialized views recoverable after an interrupted write.
func (s Store) commit(path string, value any) error {
	object, err := filepath.Rel(s.root(), path)
	if err != nil || !validObject(filepath.ToSlash(object)) {
		return ErrInvalid
	}
	object = filepath.ToSlash(object)
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var next, previous map[string]json.RawMessage
	if json.Unmarshal(data, &next) != nil {
		return ErrInvalid
	}
	_ = readJSON(path, &previous)
	fields := map[string]json.RawMessage{}
	for key, v := range next {
		if !sameJSON(v, previous[key]) {
			fields[key] = v
		}
	}
	for key := range previous {
		if _, ok := next[key]; !ok {
			fields[key] = json.RawMessage("null")
		}
	}
	state, err := s.replica()
	if err != nil {
		return err
	}
	known := false
	for _, event := range state.Events {
		if event.Object == object {
			known = true
			break
		}
	}
	if !known {
		fields = next
	}
	if known {
		for _, key := range []string{"messages", "runs", "slots"} {
			current, ok := fields[key]
			if !ok {
				continue
			}
			var rows, oldRows []json.RawMessage
			_ = json.Unmarshal(current, &rows)
			_ = json.Unmarshal(previous[key], &oldRows)
			oldByID := map[string]json.RawMessage{}
			for _, row := range oldRows {
				oldByID[memberID(row)] = row
			}
			deltas := []map[string]json.RawMessage{}
			for _, row := range rows {
				id := memberID(row)
				var cols, oldCols map[string]json.RawMessage
				_ = json.Unmarshal(row, &cols)
				_ = json.Unmarshal(oldByID[id], &oldCols)
				delta := map[string]json.RawMessage{}
				for col, v := range cols {
					if !sameJSON(v, oldCols[col]) {
						delta[col] = v
					}
				}
				if len(delta) > 0 {
					identity := "id"
					if key == "messages" {
						identity = "uid"
					}
					delta[identity], _ = json.Marshal(id)
					deltas = append(deltas, delta)
				}
			}
			if len(deltas) == 0 {
				delete(fields, key)
			} else {
				fields[key], _ = json.Marshal(deltas)
			}
		}
	}
	if len(fields) == 0 {
		return nil
	}
	parts := []map[string]json.RawMessage{}
	for _, key := range []string{"messages", "runs", "slots"} {
		if value, ok := fields[key]; ok {
			var rows []json.RawMessage
			if json.Unmarshal(value, &rows) == nil {
				delete(fields, key)
				for _, row := range rows {
					array, _ := json.Marshal([]json.RawMessage{row})
					parts = append(parts, map[string]json.RawMessage{key: array})
				}
			}
		}
	}
	if len(fields) > 0 {
		parts = append([]map[string]json.RawMessage{fields}, parts...)
	}
	for _, part := range parts {
		state.Clock++
		state.Events = append(state.Events, Mutation{Version: Version{state.Clock, state.ID}, Object: object, Fields: part})
	}
	state.Seen[state.ID] = state.Clock
	if err = writeJSON(s.replicaPath(), state); err != nil {
		return err
	}
	return s.materialize(state)
}
func sameJSON(a, b json.RawMessage) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

type stampedField struct {
	Version
	Value json.RawMessage
}

func memberID(value json.RawMessage) string {
	var row map[string]json.RawMessage
	_ = json.Unmarshal(value, &row)
	var id string
	for _, key := range []string{"uid", "id"} {
		if json.Unmarshal(row[key], &id) == nil && id != "" {
			return id
		}
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
func (s Store) materialize(state ReplicaState) error {
	objects := map[string]map[string]stampedField{}
	members := map[string]map[string]map[string]stampedField{}
	births := map[string]map[string]Version{}
	for _, event := range state.Events {
		fields := objects[event.Object]
		if fields == nil {
			fields = map[string]stampedField{}
			objects[event.Object] = fields
		}
		for key, value := range event.Fields {
			if key == "messages" || key == "runs" || key == "slots" {
				var rows []json.RawMessage
				if json.Unmarshal(value, &rows) != nil {
					continue
				}
				bucket := event.Object + "/" + key
				if members[bucket] == nil {
					members[bucket] = map[string]map[string]stampedField{}
					births[bucket] = map[string]Version{}
				}
				for _, row := range rows {
					id := memberID(row)
					if first, ok := births[bucket][id]; !ok || newer(first, event.Version) {
						births[bucket][id] = event.Version
					}
					if members[bucket][id] == nil {
						members[bucket][id] = map[string]stampedField{}
					}
					var cols map[string]json.RawMessage
					_ = json.Unmarshal(row, &cols)
					for col, v := range cols {
						old := members[bucket][id][col]
						if newer(event.Version, old.Version) {
							members[bucket][id][col] = stampedField{event.Version, v}
						}
					}
				}
			} else {
				old := fields[key]
				if newer(event.Version, old.Version) {
					fields[key] = stampedField{event.Version, value}
				}
			}
		}
	}
	mirror := map[string]bool{}
	for object, fields := range objects {
		value := map[string]json.RawMessage{}
		path := filepath.Join(s.root(), object)
		// Fields predating the journal are retained until explicitly mutated.
		current, _ := os.ReadFile(path)
		_ = json.Unmarshal(current, &value)
		for key, field := range fields {
			if string(field.Value) == "null" {
				delete(value, key)
			} else {
				value[key] = field.Value
			}
		}
		for _, key := range []string{"messages", "runs", "slots"} {
			rows, ok := members[object+"/"+key]
			if !ok {
				continue
			}
			type ordered struct {
				ID  string
				Row map[string]json.RawMessage
				Version
			}
			list := []ordered{}
			for id, cols := range rows {
				row := map[string]json.RawMessage{}
				first := births[object+"/"+key][id]
				for col, v := range cols {
					row[col] = v.Value
				}
				if key == "messages" {
					row["uid"], _ = json.Marshal(id)
				}
				list = append(list, ordered{id, row, first})
			}
			sort.Slice(list, func(i, j int) bool {
				if list[i].Clock == list[j].Clock {
					if list[i].Origin == list[j].Origin {
						return list[i].ID < list[j].ID
					}
					return list[i].Origin < list[j].Origin
				}
				return list[i].Clock < list[j].Clock
			})
			out := []map[string]json.RawMessage{}
			for i, row := range list {
				if key == "messages" {
					row.Row["id"], _ = json.Marshal(i + 1)
				}
				out = append(out, row.Row)
			}
			value[key], _ = json.Marshal(out)
		}
		// THI-101: every commit replays the whole journal; rewriting (and
		// fsyncing, F_FULLFSYNC on macOS) every object made each board write
		// take ~0.5 s on the Mac. Only objects whose content changed are written.
		if data, err := json.MarshalIndent(value, "", "  "); err == nil && bytes.Equal(append(data, '\n'), current) {
			continue
		}
		if err := writeJSON(path, value); err != nil {
			return err
		}
		if parts := strings.Split(object, "/"); len(parts) == 4 && parts[2] == "requests" {
			mirror[parts[1]] = true
		}
	}
	s.mirrorThreads(objects, mirror)
	return writeJSON(filepath.Join(s.root(), "replica-applied.json"), len(state.Events))
}

// Sync is private to the paired resident channel. Contiguous per-origin batches
// avoid acknowledging gaps. Callers send all events after the peer's cursor.
const syncBatchBytes = 128 << 10

func (s Store) Sync(in Exchange) (Exchange, error) {
	unlock, err := s.lock()
	if err != nil {
		return Exchange{}, err
	}
	defer unlock()
	state, err := s.replica()
	if err != nil {
		return Exchange{}, err
	}
	// Seed pre-journal objects once; snapshots never overwrite a peer's fields.
	known := map[string]bool{}
	for _, event := range state.Events {
		known[event.Object] = true
	}
	err = filepath.WalkDir(filepath.Join(s.root(), "projects"), func(path string, entry os.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		object, _ := filepath.Rel(s.root(), path)
		object = filepath.ToSlash(object)
		if !validObject(object) || known[object] {
			return nil
		}
		var value map[string]json.RawMessage
		if err := readJSON(path, &value); err != nil {
			return err
		}
		return s.commit(path, value)
	})
	if err != nil {
		return Exchange{}, err
	}
	state, err = s.replica()
	if err != nil {
		return Exchange{}, err
	}
	if len(in.Events) > 4096 {
		return Exchange{}, ErrInvalid
	}
	sort.Slice(in.Events, func(i, j int) bool {
		if in.Events[i].Origin == in.Events[j].Origin {
			return in.Events[i].Clock < in.Events[j].Clock
		}
		return in.Events[i].Origin < in.Events[j].Origin
	})
	changed := false
	for _, event := range in.Events {
		if len(event.Origin) != 32 || event.Clock == 0 || !validObject(event.Object) || len(event.Fields) == 0 {
			return Exchange{}, ErrInvalid
		}
		if event.Clock <= state.Seen[event.Origin] {
			continue
		}
		state.Events = append(state.Events, event)
		changed = true
		state.Seen[event.Origin] = event.Clock
		if event.Clock > state.Clock {
			state.Clock = event.Clock
		}
	}
	if _, err := os.Stat(s.replicaPath()); os.IsNotExist(err) {
		changed = true
	}
	if changed {
		if err = writeJSON(s.replicaPath(), state); err != nil {
			return Exchange{}, err
		}
		if err = s.materialize(state); err != nil {
			return Exchange{}, err
		}
	}
	out := Exchange{Seen: state.Seen, Missing: s.MissingAttachments()}
	// Each batch must cross the resident channel within one sync step;
	// larger backlogs page through More instead of one oversized request.
	batchBytes := 0
	for _, event := range state.Events {
		if event.Clock > in.Seen[event.Origin] {
			encoded, _ := json.Marshal(event)
			if len(out.Events) > 0 && batchBytes+len(encoded) > syncBatchBytes {
				out.More = true
				break
			}
			out.Events = append(out.Events, event)
			batchBytes += len(encoded)
			if len(out.Events) == 4096 {
				out.More = true
				break
			}
		}
	}
	return out, nil
}

func (s Store) recoverReplica() error {
	if _, err := os.Stat(s.replicaPath()); os.IsNotExist(err) {
		return nil
	}
	state, err := s.replica()
	if err != nil {
		return err
	}
	var applied int
	if readJSON(filepath.Join(s.root(), "replica-applied.json"), &applied) == nil && applied == len(state.Events) {
		return nil
	}
	return s.materialize(state)
}

func (s Store) Recover() error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	unlock()
	return nil
}
