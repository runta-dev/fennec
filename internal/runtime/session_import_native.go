package runtime

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var nativeConversationPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,126}[A-Za-z0-9])?$`)
var openCodeConversationPattern = regexp.MustCompile(`^ses_[A-Za-z0-9]{1,96}$`)

func validConversation(agent, id string) bool {
	switch agent {
	case "claude", "codex":
		return conversationPattern.MatchString(id)
	case "pi":
		return nativeConversationPattern.MatchString(id)
	case "kimi":
		return conversationPattern.MatchString(strings.TrimPrefix(id, "session_")) && strings.HasPrefix(id, "session_")
	case "opencode":
		return openCodeConversationPattern.MatchString(id)
	}
	return false
}

// Pickers without an ID remain native cloud pickers. Explicit local paths and
// continue flags become the resolved cloud ID; never forward a Mac-only path.
func parseNativeResume(agent string, args []string) (resumeRequest, bool) {
	r := resumeRequest{}
	selected := -1
	count := 1
	remove := map[int]bool{}
	valueFlags := map[string]bool{"--model": true, "-m": true, "--provider": true, "--thinking": true, "--mode": true, "--agent": true, "--prompt": true, "--system-prompt": true, "--append-system-prompt": true, "--config": true, "--tools": true, "--work-dir": true, "--extension": true, "-e": true, "--skill": true, "--theme": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--no-session" {
			return r, false
		}
		if arg == "-p" && agent == "kimi" {
			i++
			continue
		}
		if arg == "--session-dir" && agent == "pi" && i+1 < len(args) {
			r.root = args[i+1]
			remove[i] = true
			remove[i+1] = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "--session-dir=") && agent == "pi" {
			r.root = strings.TrimPrefix(arg, "--session-dir=")
			remove[i] = true
			continue
		}
		if valueFlags[arg] {
			i++
			continue
		}
		if arg == "--continue" || arg == "-c" {
			if selected < 0 {
				selected = i
				r.latest = true
			}
			continue
		}
		flag, value, hasValue := strings.Cut(arg, "=")
		if flag != "--session" && !(flag == "-s" && agent == "opencode") && !(flag == "-S" && agent == "kimi") && !(flag == "--session-id" && agent == "pi") {
			continue
		}
		if !hasValue {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				continue
			}
			value = args[i+1]
		}
		selected, count, r.id, r.latest = i, 1, value, false
		if !hasValue {
			count = 2
			i++
		}
	}
	if selected < 0 {
		return r, false
	}
	r.rewrite = func(id string) []string {
		out := []string{}
		for i := 0; i < len(args); i++ {
			if remove[i] {
				continue
			}
			if i == selected {
				out = append(out, "--session", id)
				i += count - 1
				continue
			}
			out = append(out, args[i])
		}
		return out
	}
	return r, true
}

type nativeSessionCandidate struct {
	file, id, cwd string
	modified      int64
}

func readSessionFile(file string) ([]byte, error) {
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > sessionImportLimit {
		return nil, fmt.Errorf("invalid or oversized conversation file")
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, sessionImportLimit+1))
	if len(data) > sessionImportLimit {
		return nil, fmt.Errorf("conversation is too large to move to the cloud computer")
	}
	if after, statErr := f.Stat(); statErr != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("conversation changed while being collected; resume again")
	}
	return data, err
}

func nativeSessionCandidates(root, agent string) []nativeSessionCandidate {
	var found []nativeSessionCandidate
	filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if agent == "pi" && !strings.HasSuffix(file, ".jsonl") || agent == "kimi" && entry.Name() != "state.json" {
			return nil
		}
		// Kimi agent state files are not session metadata.
		if agent == "kimi" && !strings.HasPrefix(filepath.Base(filepath.Dir(file)), "session_") {
			return nil
		}
		f, err := os.Open(file)
		if err != nil {
			return nil
		}
		var data []byte
		if agent == "pi" {
			buf := make([]byte, 64<<10)
			n, _ := f.Read(buf)
			data = bytes.SplitN(buf[:n], []byte("\n"), 2)[0]
		} else {
			data, _ = io.ReadAll(io.LimitReader(f, 1<<20))
		}
		f.Close()
		var meta struct {
			ID       string          `json:"id"`
			Cwd      string          `json:"cwd"`
			Type     string          `json:"type"`
			Archived bool            `json:"archived"`
			Updated  json.RawMessage `json:"updatedAt"`
		}
		if json.Unmarshal(data, &meta) != nil || !validConversation(agent, meta.ID) || meta.Archived || agent == "pi" && meta.Type != "session" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		modified := info.ModTime().UnixNano()
		if agent == "kimi" {
			modified = info.ModTime().UnixMilli()
			var updated int64
			if json.Unmarshal(meta.Updated, &updated) == nil && updated > 0 && updated < 1<<53 {
				modified = updated
			}
		}
		found = append(found, nativeSessionCandidate{file, meta.ID, meta.Cwd, modified})
		return nil
	})
	sort.Slice(found, func(i, j int) bool {
		if found[i].modified == found[j].modified {
			return found[i].file < found[j].file
		}
		return found[i].modified > found[j].modified
	})
	return found
}

func collectNativeSession(home, agent, directory string, r resumeRequest) (*SessionImport, error) {
	root := filepath.Join(home, "sessions")
	if r.root != "" {
		root = r.root
		if !filepath.IsAbs(root) {
			root = filepath.Join(directory, root)
		}
	}
	var candidates []nativeSessionCandidate
	if agent == "pi" && (strings.ContainsAny(r.id, "/\\") || strings.HasSuffix(r.id, ".jsonl")) {
		file := r.id
		if !filepath.IsAbs(file) {
			file = filepath.Join(directory, file)
		}
		data, err := readSessionFile(file)
		if err != nil {
			return nil, err
		}
		var header map[string]json.RawMessage
		if json.Unmarshal(bytes.SplitN(data, []byte("\n"), 2)[0], &header) != nil || !validConversation(agent, recordString(header, "id")) {
			return nil, fmt.Errorf("invalid Pi session")
		}
		candidates = []nativeSessionCandidate{{file: file, id: recordString(header, "id"), cwd: recordString(header, "cwd")}}
	} else {
		for _, c := range nativeSessionCandidates(root, agent) {
			if r.latest {
				match := false
				for _, v := range directoryVariants(directory) {
					if filepath.Clean(c.cwd) == v {
						match = true
					}
				}
				if match {
					candidates = append(candidates, c)
					break
				}
			} else if c.id == r.id || agent == "pi" && strings.HasPrefix(c.id, r.id) || agent == "kimi" && c.id == "session_"+r.id {
				candidates = append(candidates, c)
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("conversation identifier is ambiguous")
	}
	c := candidates[0]
	b := &SessionImport{Conversation: c.id}
	data, err := readSessionFile(c.file)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	b.Marker = hex.EncodeToString(hash[:])
	if agent == "pi" {
		b.Files = []SessionFile{{Path: c.id + ".jsonl", Data: trimPiTranscript(data)}}
		return b, nil
	}
	base := filepath.Dir(c.file)
	total := 0
	err = filepath.WalkDir(base, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, file)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && rel != "agents" && rel != "attachments" && !strings.HasPrefix(rel, "agents/") && !strings.HasPrefix(rel, "attachments/") {
				return filepath.SkipDir
			}
			return nil
		}
		if rel != "state.json" && !strings.HasPrefix(rel, "agents/") && !strings.HasPrefix(rel, "attachments/") {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("conversation contains a non-regular file")
		}
		data, err := readSessionFile(file)
		if err != nil {
			return err
		}
		total += len(data)
		if total > sessionImportLimit || len(b.Files) >= sessionImportFiles {
			return fmt.Errorf("conversation is too large to move to the cloud computer")
		}
		if filepath.Base(file) == "wire.jsonl" {
			data = trimKimiTranscript(data)
		}
		b.Files = append(b.Files, SessionFile{Path: rel, Data: data})
		return nil
	})
	return b, err
}

func piSessionDirectory(cwd string) string {
	return "--" + strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimLeft(cwd, "/\\")) + "--"
}

func kimiWorkdirBucket(cwd string) string {
	normal := strings.TrimRight(strings.ReplaceAll(cwd, "\\", "/"), "/")
	slug := regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(strings.ToLower(filepath.Base(normal)), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" || slug == "." || slug == ".." {
		slug = "workspace"
	}
	hash := sha256.Sum256([]byte(normal))
	return "wd_" + slug + "_" + hex.EncodeToString(hash[:6])
}

func nativeSessionExists(home, agent, id string) bool {
	for _, c := range nativeSessionCandidates(filepath.Join(home, "sessions"), agent) {
		if c.id == id {
			if agent == "kimi" {
				return kimiIndexContains(home, id)
			}
			return true
		}
	}
	return false
}

func applyNativeSession(home, workspace string, b SessionImport) (int, error) {
	root := cloudAgentHome(home, b.Agent)
	if b.Agent == "pi" {
		var header map[string]json.RawMessage
		if json.Unmarshal(bytes.SplitN(b.Files[0].Data, []byte("\n"), 2)[0], &header) != nil || recordString(header, "id") != b.Conversation || recordString(header, "type") != "session" {
			return 0, fmt.Errorf("invalid Pi conversation header")
		}
		file := filepath.Join(root, "sessions", piSessionDirectory(workspace), b.Conversation+".jsonl")
		if err := safeConversationDestination(home, file); err != nil {
			return 0, err
		}
		if err := writeImported(file, mapWorkingDirectory(b.Files[0].Data, workspace)); err != nil {
			return 0, err
		}
		return 1, nil
	}
	// Repair an index append interrupted after the directory rename without
	// overwriting an already published journal.
	for _, c := range nativeSessionCandidates(filepath.Join(root, "sessions"), "kimi") {
		if c.id == b.Conversation {
			return 0, ensureKimiIndex(home, root, c.id, filepath.Dir(c.file), c.cwd)
		}
	}
	parent := filepath.Join(root, "sessions", kimiWorkdirBucket(workspace))
	if err := safeConversationDestination(home, filepath.Join(parent, b.Conversation)); err != nil {
		return 0, err
	}
	if err := safeConversationDestination(home, filepath.Join(root, "session_index.jsonl")); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return 0, err
	}
	stage, err := os.MkdirTemp(parent, ".import-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(stage)
	destination := filepath.Join(parent, b.Conversation)
	var state map[string]json.RawMessage
	for _, f := range b.Files {
		if f.Path == "state.json" {
			json.Unmarshal(f.Data, &state)
		}
	}
	if recordString(state, "id") != b.Conversation {
		return 0, fmt.Errorf("invalid Kimi conversation state")
	}
	oldCwd := recordString(state, "cwd")
	// Replace only actual JSON string values, including attachment/agent roots,
	// never snippets embedded inside a prompt or a tool result.
	for _, f := range b.Files {
		data := f.Data
		if strings.HasSuffix(f.Path, ".json") || strings.HasSuffix(f.Path, ".jsonl") {
			data = mapNativeJSONPaths(data, oldCwd, workspace, destination)
		}
		if err := writeImported(filepath.Join(stage, filepath.FromSlash(f.Path)), data); err != nil {
			return 0, err
		}
	}
	if err := os.Rename(stage, destination); err != nil {
		return 0, err
	}
	return len(b.Files), ensureKimiIndex(home, root, b.Conversation, destination, workspace)
}

func kimiIndexContains(root, id string) bool {
	f, err := os.Open(filepath.Join(root, "session_index.jsonl"))
	if err != nil {
		return false
	}
	defer f.Close()
	reader := bufio.NewScanner(f)
	reader.Buffer(make([]byte, 4096), 1<<20)
	for reader.Scan() {
		var entry struct {
			ID string `json:"sessionId"`
		}
		if json.Unmarshal(reader.Bytes(), &entry) == nil && entry.ID == id {
			return true
		}
	}
	return false
}

func ensureKimiIndex(home, root, id, destination, workspace string) error {
	if kimiIndexContains(root, id) {
		return nil
	}
	if err := safeConversationDestination(home, filepath.Join(root, "session_index.jsonl")); err != nil {
		return err
	}
	index, err := os.OpenFile(filepath.Join(root, "session_index.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer index.Close()
	entry, _ := json.Marshal(map[string]string{"sessionId": id, "sessionDir": destination, "workDir": workspace})
	if _, err = index.Write(append(entry, '\n')); err != nil {
		return err
	}
	return nil
}

func mapNativeJSONPaths(data []byte, oldCwd, workspace, destination string) []byte {
	// Kimi stores absolute session paths in agent metadata and attachment URLs.
	var visit func(string, any) any
	visit = func(key string, value any) any {
		switch v := value.(type) {
		case map[string]any:
			for k, x := range v {
				v[k] = visit(k, x)
			}
		case []any:
			for i, x := range v {
				v[i] = visit(key, x)
			}
		case string:
			if v == oldCwd && oldCwd != "" && (key == "cwd" || key == "workDir") {
				return workspace
			}
			if key != "homedir" && key != "path" && key != "url" {
				return value
			}
			id := filepath.Base(destination)
			for _, suffix := range []string{"/" + id + "/agents/", "/" + id + "/attachments/"} {
				if at := strings.Index(v, suffix); at >= 0 && (strings.HasPrefix(v, "/") || strings.HasPrefix(v, "file://")) {
					prefix := ""
					if strings.HasPrefix(v, "file://") {
						prefix = "file://"
					}
					return prefix + destination + v[at+len(id)+1:]
				}
			}
		}
		return value
	}
	decode := func(data []byte) (any, error) {
		var object any
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		err := d.Decode(&object)
		if err == nil {
			var extra any
			if d.Decode(&extra) != io.EOF {
				err = fmt.Errorf("multiple JSON records")
			}
		}
		return object, err
	}
	if object, err := decode(data); err == nil {
		mapped, _ := json.Marshal(visit("", object))
		return mapped
	}
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if object, err := decode(line); err == nil {
			out.Write(encodeAny(visit("", object)))
			out.WriteByte('\n')
		} else {
			out.Write(line)
		}
	}
	return out.Bytes()
}

func encodeAny(v any) []byte { data, _ := json.Marshal(v); return data }
