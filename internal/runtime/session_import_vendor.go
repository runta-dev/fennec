package runtime

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func parseVendorResume(agent string, args []string) (resumeRequest, bool) {
	r := resumeRequest{}
	values := map[string]bool{"--model": true, "-m": true, "--workspace": true, "--cwd": true, "--config": true, "--output-format": true, "--api-key": true, "--endpoint": true, "--system-prompt": true, "--append-system-prompt": true, "--session-id": true, "-s": true, "--new-session-id": true, "--mode": true, "--sandbox": true, "--trust": true, "--attach": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if values[a] {
			i++
			continue
		}
		flag, value, equal := strings.Cut(a, "=")
		n := 1
		switch {
		case a == "--continue" || agent == "grok" && a == "-c" || agent == "cursor" && a == "resume" && i == 0:
			r.latest = true
		case flag == "--resume" || agent == "grok" && flag == "-r":
			if !equal && i+1 < len(args) && (!strings.HasPrefix(args[i+1], "-") || args[i+1] == "-1") {
				value = args[i+1]
				n = 2
			}
			if value == "" {
				if agent == "cursor" {
					return r, false
				}
				r.latest = true
				r.all = true
			} else if agent == "cursor" && value == "-1" {
				r.latest = true
			} else {
				r.id = value
			}
		default:
			continue
		}
		at := i
		r.rewrite = func(id string) []string {
			out := append([]string{}, args[:at]...)
			out = append(out, "--resume", id)
			return append(out, args[at+n:]...)
		}
		return r, true
	}
	return r, false
}

func cursorWorkspaceBucket(cwd string) string {
	// Cursor uses MD5 only as a directory label, not for integrity or security.
	sum := md5.Sum([]byte(filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:])
}

func vendorSessionFile(agent, name string) bool {
	if agent == "cursor" {
		return name == "store.db" || name == "meta.json"
	}
	// Deliberately exclude global settings, auth, sockets and replay backups.
	switch name {
	case "summary.json", "chat_history.jsonl", "updates.jsonl", "plan.json", "plan_mode.json", "signals.json", "usage.json", "announcement_state.json", "goal/state.json", "rewind_points.jsonl", "feedback.jsonl", "btw_history.jsonl":
		return true
	}
	for _, dir := range []string{"compaction_checkpoints/", "compaction/", "attachments/", "workflows/", "subagents/", "goal/"} {
		if strings.HasPrefix(name, dir) {
			return true
		}
	}
	return false
}

type vendorCandidate struct {
	dir, id, cwd, title string
	modified            int64
}

func vendorCandidates(home, agent string) []vendorCandidate {
	dirName, marker := "sessions", "summary.json"
	if agent == "cursor" {
		dirName, marker = "chats", "store.db"
	}
	var found []vendorCandidate
	buckets, _ := os.ReadDir(filepath.Join(home, dirName))
	for _, bucket := range buckets {
		if !bucket.IsDir() {
			continue
		}
		entries, _ := os.ReadDir(filepath.Join(home, dirName, bucket.Name()))
		for _, entry := range entries {
			if !entry.IsDir() || !validConversation(agent, entry.Name()) {
				continue
			}
			dir := filepath.Join(home, dirName, bucket.Name(), entry.Name())
			info, err := os.Lstat(filepath.Join(dir, marker))
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			c := vendorCandidate{dir: dir, id: entry.Name(), modified: info.ModTime().UnixNano()}
			if agent == "grok" {
				data, err := readSessionFile(filepath.Join(dir, marker))
				if err != nil {
					continue
				}
				var meta struct {
					Info           struct{ ID, Cwd string }
					SessionSummary string `json:"session_summary"`
					GeneratedTitle string `json:"generated_title"`
					Updated        string `json:"updated_at"`
				}
				if json.Unmarshal(data, &meta) != nil || meta.Info.ID != c.id {
					continue
				}
				c.cwd, c.title = meta.Info.Cwd, meta.SessionSummary
				if meta.GeneratedTitle != "" {
					c.title = meta.GeneratedTitle
				}
				if t, err := time.Parse(time.RFC3339Nano, meta.Updated); err == nil {
					c.modified = t.UnixNano()
				}
			} else {
				// The sidecar is optional in older Cursor releases. Its timestamp is
				// preferred; the database and WAL mtimes cover versions without it.
				for _, name := range []string{"store.db-wal", "meta.json"} {
					if stat, err := os.Lstat(filepath.Join(dir, name)); err == nil && stat.ModTime().UnixNano() > c.modified {
						c.modified = stat.ModTime().UnixNano()
					}
				}
			}
			found = append(found, c)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].modified == found[j].modified {
			return found[i].dir < found[j].dir
		}
		return found[i].modified > found[j].modified
	})
	return found
}

func vendorSessionExists(home, agent, id string) bool {
	for _, c := range vendorCandidates(home, agent) {
		if c.id == id {
			return true
		}
	}
	return false
}

func collectVendorSession(home, agent, cwd string, r resumeRequest) (*SessionImport, error) {
	var candidates []vendorCandidate
	for _, c := range vendorCandidates(home, agent) {
		if r.latest {
			match := r.all
			for _, dir := range directoryVariants(cwd) {
				if agent == "cursor" && filepath.Base(filepath.Dir(c.dir)) == cursorWorkspaceBucket(dir) || agent == "grok" && filepath.Clean(c.cwd) == dir {
					match = true
				}
			}
			if match {
				candidates = append(candidates, c)
				break
			}
		} else if c.id == r.id || agent == "grok" && c.title == r.id {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("multiple local conversations match; resume by conversation ID")
	}
	c := candidates[0]
	b := &SessionImport{Conversation: c.id}
	var err error
	if agent == "cursor" {
		data, err := snapshotCursorSession(filepath.Join(c.dir, "store.db"), c.id)
		if err != nil {
			return nil, err
		}
		b.Files = append(b.Files, SessionFile{"store.db", data})
		if _, err := os.Lstat(filepath.Join(c.dir, "meta.json")); err == nil {
			data, err := readSessionFile(filepath.Join(c.dir, "meta.json"))
			if err != nil {
				return nil, err
			}
			b.Files = append(b.Files, SessionFile{"meta.json", data})
		}
	} else {
		total := 0
		initial := map[string]fs.FileInfo{}
		for _, name := range []string{"summary.json", "chat_history.jsonl", "updates.jsonl"} {
			if info, e := os.Lstat(filepath.Join(c.dir, name)); e == nil {
				initial[name] = info
			}
		}
		err = filepath.WalkDir(c.dir, func(file string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(c.dir, file)
			rel = filepath.ToSlash(rel)
			if e.IsDir() {
				if rel != "." && !vendorSessionFile(agent, rel+"/") {
					return filepath.SkipDir
				}
				return nil
			}
			if !vendorSessionFile(agent, rel) {
				return nil
			}
			data, err := readSessionFile(file)
			if err != nil {
				return err
			}
			total += len(data)
			if total > sessionImportLimit || len(b.Files) >= sessionImportFiles {
				return fmt.Errorf("conversation is too large to move to the cloud computer")
			}
			b.Files = append(b.Files, SessionFile{rel, data})
			return nil
		})
		if err != nil {
			return nil, err
		}
		for name, before := range initial {
			after, e := os.Lstat(filepath.Join(c.dir, name))
			if e != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
				return nil, fmt.Errorf("conversation changed while being collected; resume again")
			}
		}
		b.Files = trimGrokSession(b.Files)
	}
	hash := sha256.New()
	for _, f := range b.Files {
		hash.Write([]byte(f.Path))
		hash.Write(f.Data)
	}
	b.Marker = hex.EncodeToString(hash.Sum(nil))
	return b, nil
}

func applyVendorSession(home, workspace string, b SessionImport) (int, error) {
	root := cloudAgentHome(home, b.Agent)
	bucket := strings.ReplaceAll(url.QueryEscape(workspace), "+", "%20")
	subdir := "sessions"
	if b.Agent == "cursor" {
		bucket = cursorWorkspaceBucket(workspace)
		subdir = "chats"
	}
	if len(bucket) > 255 {
		return 0, fmt.Errorf("cloud workspace path is too long for session storage")
	}
	parent := filepath.Join(root, subdir, bucket)
	destination := filepath.Join(parent, b.Conversation)
	if err := safeConversationDestination(home, filepath.Join(destination, "placeholder")); err != nil {
		return 0, err
	}
	required := "summary.json"
	if b.Agent == "cursor" {
		required = "store.db"
	}
	files := map[string][]byte{}
	for _, f := range b.Files {
		files[f.Path] = f.Data
	}
	if len(files[required]) == 0 {
		return 0, fmt.Errorf("conversation state is missing")
	}
	if b.Agent == "grok" {
		var meta struct{ Info struct{ ID string } }
		if json.Unmarshal(files[required], &meta) != nil || meta.Info.ID != b.Conversation || len(files["chat_history.jsonl"]) == 0 {
			return 0, fmt.Errorf("invalid Grok conversation state")
		}
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return 0, err
	}
	stage, err := os.MkdirTemp(parent, ".import-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(stage)
	for _, f := range b.Files {
		data := f.Data
		if f.Path == "summary.json" || f.Path == "meta.json" {
			data = mapWorkingDirectory(data, workspace)
		}
		if b.Agent == "grok" && f.Path == "summary.json" {
			var summary map[string]json.RawMessage
			if json.Unmarshal(data, &summary) != nil {
				return 0, fmt.Errorf("invalid Grok conversation summary")
			}
			summary["grok_home"] = encodeAny(root)
			data = encodeAny(summary)
		}
		if err := writeImported(filepath.Join(stage, filepath.FromSlash(f.Path)), data); err != nil {
			return 0, err
		}
	}
	if b.Agent == "cursor" {
		if err := validateCursorSnapshot(filepath.Join(stage, "store.db"), b.Conversation); err != nil {
			return 0, err
		}
	}
	if err := os.Rename(stage, destination); err != nil {
		return 0, err
	}
	return len(b.Files), nil
}

// Grok already rewrites chat_history.jsonl to the active model context. The
// append-only UI journal still contains old screenshots; trim only known
// message payloads before a complete checkpoint, retaining side state and
// original prompt indices. A rewind can depend on any earlier record.
func trimGrokSession(files []SessionFile) []SessionFile {
	byPath := map[string][]byte{}
	for _, f := range files {
		byPath[f.Path] = f.Data
	}
	data := byPath["updates.jsonl"]
	lines := bytes.SplitAfter(data, []byte("\n"))
	type envelope struct {
		Method string `json:"method"`
		Params struct {
			Update map[string]json.RawMessage `json:"update"`
		} `json:"params"`
	}
	records := make([]envelope, len(lines))
	last := -1
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if json.Unmarshal(line, &records[i]) != nil {
			return files
		}
		u := records[i].Params.Update
		kind := recordString(u, "sessionUpdate")
		if kind == "" || kind == "rewind_marker" {
			return files
		}
		if kind == "compaction_checkpoint" {
			last = -1
			var cp struct {
				ID      string            `json:"checkpoint_id"`
				Version int               `json:"schema_version"`
				History []json.RawMessage `json:"compacted_history"`
				Index   int               `json:"prompt_index_at_compaction"`
			}
			name := recordString(u, "checkpoint_file")
			if records[i].Method != "_x.ai/session/update" || path.Dir(name) != "compaction_checkpoints" || json.Unmarshal(byPath[name], &cp) != nil || cp.Version != 1 || cp.ID != recordString(u, "checkpoint_id") || len(cp.History) == 0 {
				continue
			}
			var version, index int
			if json.Unmarshal(u["schema_version"], &version) != nil || version != cp.Version || json.Unmarshal(u["prompt_index_at_compaction"], &index) != nil || index != cp.Index || index < 0 {
				continue
			}
			last = i
		}
	}
	if last < 0 {
		return files
	}
	var out bytes.Buffer
	checkpoints := map[string]bool{}
	for i, line := range lines {
		kind := recordString(records[i].Params.Update, "sessionUpdate")
		if kind == "compaction_checkpoint" {
			if i < last {
				continue
			}
			checkpoints[recordString(records[i].Params.Update, "checkpoint_file")] = true
		}
		if i < last && records[i].Method == "session/update" {
			switch kind {
			case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update":
				continue
			}
		}
		out.Write(line)
	}
	result := make([]SessionFile, 0, len(files))
	for _, file := range files {
		if strings.HasPrefix(file.Path, "compaction_checkpoints/") && !checkpoints[file.Path] {
			continue
		}
		if file.Path == "updates.jsonl" {
			file.Data = out.Bytes()
		}
		result = append(result, file)
	}
	return result
}
