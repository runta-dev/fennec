package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Use the agent's public export/import interface instead of depending on its
// SQLite schema, WAL state or blob layout. Resolve managed launchers first so
// exporting a local conversation cannot recursively launch a cloud TUI.
type openCodeSessionCLI struct {
	binary, root, directory string
	v2                      bool
}

func newOpenCodeSessionCLI(root, directory string, executable ...string) (*openCodeSessionCLI, error) {
	binary := ""
	if len(executable) > 0 {
		binary = executable[0]
	}
	if binary == "" {
		var err error
		binary, err = exec.LookPath("opencode")
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(binary) {
		return nil, fmt.Errorf("invalid OpenCode executable")
	}
	f, err := os.Open(binary)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 256)
	n, _ := f.Read(header)
	f.Close()
	if bytes.Contains(header[:n], []byte("# Fennec managed")) {
		return nil, fmt.Errorf("OpenCode original executable required")
	}
	c := &openCodeSessionCLI{binary: binary, root: root, directory: directory}
	version, err := c.run("--version")
	if err != nil {
		return nil, err
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(version)), "v")
	if !strings.HasPrefix(v, "1.") && !strings.HasPrefix(v, "2.") {
		return nil, fmt.Errorf("unsupported OpenCode session interface")
	}
	c.v2 = strings.HasPrefix(v, "2.")
	return c, nil
}

type sessionOutput struct {
	bytes.Buffer
	limit int
}

func (b *sessionOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("conversation output exceeds size limit")
	}
	return b.Buffer.Write(p)
}

func (c *openCodeSessionCLI) run(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Dir = c.directory
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+c.root, "OPENCODE_DISABLE_AUTOUPDATE=true")
	// Cloud native data/config are a pair; local custom config stays inherited.
	if filepath.Base(filepath.Dir(c.root)) == "opencode" && filepath.Base(c.root) == "data" {
		cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+filepath.Join(filepath.Dir(c.root), "config"))
	}
	// Some Bun/OpenCode releases exit before a pipe's async stdout write has
	// drained. A private regular file makes native export writes synchronous;
	// otherwise a valid large export can be silently cut at the pipe capacity.
	out, err := os.CreateTemp("", "fennec-opencode-export-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	stderr := &sessionOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	runErr := cmd.Run()
	data, err := readSessionFile(out.Name())
	if err != nil {
		return nil, err
	}
	if runErr != nil {
		if bytes.Contains(stderr.Bytes(), []byte("Session not found")) || bytes.Contains(data, []byte("Session not found")) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("OpenCode conversation command failed: %w", runErr)
	}
	return data, nil
}

func (c *openCodeSessionCLI) export(id string) ([]byte, error) {
	if c.v2 {
		return c.run("session", "export", id)
	}
	return c.run("export", id)
}

func collectOpenCode(root, directory string, r resumeRequest, executable ...string) (*SessionImport, error) {
	c, err := newOpenCodeSessionCLI(root, directory, executable...)
	if err != nil {
		return nil, err
	}
	id := r.id
	if r.latest {
		data, err := c.run("session", "list", "--format", "json")
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return nil, nil
		}
		var sessions []struct {
			ID        string `json:"id"`
			Directory string `json:"directory"`
			Updated   int64  `json:"updated"`
		}
		if json.Unmarshal(data, &sessions) != nil {
			return nil, fmt.Errorf("invalid OpenCode session list")
		}
		sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].Updated > sessions[j].Updated })
		for _, s := range sessions {
			for _, v := range directoryVariants(directory) {
				if filepath.Clean(s.Directory) == v {
					id = s.ID
					break
				}
			}
			if id != "" {
				break
			}
		}
	}
	if !validConversation("opencode", id) {
		return nil, nil
	}
	data, err := c.export(id)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rootObject map[string]json.RawMessage
	if err := json.Unmarshal(data, &rootObject); err != nil {
		return nil, fmt.Errorf("invalid OpenCode conversation export: %w", err)
	}
	if recordString(recordObject(rootObject, "info"), "id") != id {
		return nil, fmt.Errorf("OpenCode exported a different conversation")
	}
	hash := sha256.Sum256(data)
	return &SessionImport{Conversation: id, Marker: hex.EncodeToString(hash[:]), Files: []SessionFile{{Path: id + ".json", Data: trimOpenCodeExport(data)}}}, nil
}

func openCodeSessionExists(root, id string) bool {
	c, err := newOpenCodeSessionCLI(root, "")
	if err != nil {
		return false
	}
	data, err := c.export(id)
	if err != nil {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(data, &object) == nil && recordString(recordObject(object, "info"), "id") == id
}

func applyOpenCode(home, workspace string, b SessionImport) (int, error) {
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return 0, err
	}
	c, err := newOpenCodeSessionCLI(cloudAgentHome(home, "opencode"), workspace)
	if errors.Is(err, exec.ErrNotFound) || os.IsNotExist(err) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if installErr := ensureLocalAgentBinary(ctx, home, "opencode"); installErr != nil {
			return 0, installErr
		}
		c, err = newOpenCodeSessionCLI(cloudAgentHome(home, "opencode"), workspace, filepath.Join(home, ".local/fennec/agents/bin/opencode"))
	}
	if err != nil {
		return 0, err
	}
	// A native copy is authoritative even when its marker has advanced. Never
	// let the v1 import command merge stale local records into that session.
	if data, err := c.export(b.Conversation); err == nil && json.Valid(data) {
		return 0, nil
	} else if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(b.Files[0].Data, &object) != nil || recordString(recordObject(object, "info"), "id") != b.Conversation {
		return 0, fmt.Errorf("invalid OpenCode conversation")
	}
	file, err := os.CreateTemp(workspace, ".fennec-conversation-*.json")
	if err != nil {
		return 0, err
	}
	defer os.Remove(file.Name())
	data := mapOpenCodeWorkingDirectory(b.Files[0].Data, workspace)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return 0, err
	}
	if err = file.Close(); err != nil {
		return 0, err
	}
	var output []byte
	if c.v2 {
		output, err = c.run("session", "import", file.Name())
	} else {
		output, err = c.run("import", file.Name())
	}
	if err != nil {
		return 0, err
	}
	if !bytes.Contains(output, []byte("Imported session: "+b.Conversation)) {
		return 0, fmt.Errorf("OpenCode did not confirm conversation import")
	}
	return 1, nil
}

func mapOpenCodeWorkingDirectory(data []byte, workspace string) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return data
	}
	value, _ := json.Marshal(workspace)
	info := recordObject(root, "info")
	if info != nil {
		if info["directory"] != nil {
			info["directory"] = value
		}
		if location := recordObject(info, "location"); location != nil {
			location["directory"] = value
			info["location"], _ = json.Marshal(location)
		}
		root["info"], _ = json.Marshal(info)
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(root["messages"], &messages) != nil {
		return data
	}
	for _, message := range messages {
		mi := recordObject(message, "info")
		if mi == nil {
			continue
		}
		if p := recordObject(mi, "path"); p != nil {
			p["cwd"] = value
			p["root"] = value
			mi["path"], _ = json.Marshal(p)
			message["info"], _ = json.Marshal(mi)
		}
	}
	root["messages"], _ = json.Marshal(messages)
	return encodeAny(root)
}
