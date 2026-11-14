package runtime

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fennec/internal/localssh"
	"fennec/internal/setup"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// A takeover launch runs the agent on the cloud computer, so a command such as
// "claude --resume <id>" or "codex resume --last" would look for a
// conversation that only exists on the Mac. Like cloud ChatGPT's thread
// migration, the conversation is copied to the cloud first: the Mac collects
// it (CollectLocalSession), sends it on the shared SSH connection, and the
// cloud writes it where the agent looks (ApplySessionImport), with working
// directories mapped to the launch's cloud workspace.

// SessionImportCommand is the cloud command that reads one zstd-compressed
// SessionImport from stdin.
const SessionImportCommand = `"$HOME/.local/fennec/bin/fennec-terminal" import-session`

// SessionImport carries one conversation from the Mac.
type SessionImport struct {
	Version      int           `json:"version"`
	Agent        string        `json:"agent"`
	LaunchID     string        `json:"launch_id"`
	Conversation string        `json:"conversation"`
	Marker       string        `json:"marker"`
	Files        []SessionFile `json:"files"`
}

// SessionFile is one file of a conversation. Claude paths are relative to the
// project directory ("<id>.jsonl", "<id>/subagents/…"); Codex paths are
// relative to CODEX_HOME ("sessions/2026/11/14/rollout-…-<id>.jsonl").
type SessionFile struct {
	Path string `json:"path"`
	Data []byte `json:"-"`
}

const (
	sessionImportLimit = 512 << 20
	sessionImportFiles = 4096
)

var conversationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var cwdField = regexp.MustCompile(`"cwd"\s*:\s*"(?:[^"\\]|\\.)*"`)

// resumeRequest is a launch that continues a conversation: a named one, or the
// latest one for the working directory.
type resumeRequest struct {
	id      string
	latest  bool
	all     bool // codex --all: latest across every directory
	rewrite func(id string) []string
}

// claudeValueOptions take a separate value that must not be read as a flag.
var claudeValueOptions = map[string]bool{"--model": true, "--fallback-model": true, "--append-system-prompt": true, "--system-prompt": true, "--permission-mode": true, "--settings": true, "--session-id": true, "--allowed-tools": true, "--allowedTools": true, "--disallowed-tools": true, "--disallowedTools": true, "--mcp-config": true, "--output-format": true, "--input-format": true, "--agents": true, "--agent": true, "--add-dir": true, "--betas": true, "--setting-sources": true, "--plugin-dir": true}

// codexValueOptions mirror the launcher's list of Codex options with a value.
var codexValueOptions = map[string]bool{"-c": true, "--config": true, "--enable": true, "--disable": true, "--remote": true, "--remote-auth-token-env": true, "-m": true, "--model": true, "--local-provider": true, "-p": true, "--profile": true, "-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true, "-i": true, "--image": true}

func parseResume(agent string, args []string) (resumeRequest, bool) {
	switch agent {
	case "claude":
		return parseClaudeResume(args)
	case "codex":
		return parseCodexResume(args)
	}
	return resumeRequest{}, false
}

// Claude: --resume <id>, --resume=<id>, -r <id>, and --continue/-c. A resume
// without an ID opens Claude's picker, which lists cloud conversations only.
func parseClaudeResume(args []string) (resumeRequest, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return resumeRequest{}, false
		case claudeValueOptions[arg]:
			i++
		case arg == "--resume" || arg == "-r":
			if i+1 < len(args) && conversationPattern.MatchString(args[i+1]) {
				return resumeRequest{id: args[i+1]}, true
			}
			return resumeRequest{}, false
		case strings.HasPrefix(arg, "--resume="):
			value := strings.TrimPrefix(arg, "--resume=")
			return resumeRequest{id: value}, conversationPattern.MatchString(value)
		case arg == "--continue" || arg == "-c":
			at := i
			return resumeRequest{latest: true, rewrite: func(id string) []string {
				out := append([]string{}, args[:at]...)
				out = append(out, "--resume", id)
				return append(out, args[at+1:]...)
			}}, true
		}
	}
	return resumeRequest{}, false
}

// Codex: "resume <id>" and "resume --last [--all]" after any global options.
func parseCodexResume(args []string) (resumeRequest, bool) {
	i := 0
	for ; i < len(args); i++ {
		if args[i] == "--" {
			return resumeRequest{}, false
		}
		if codexValueOptions[args[i]] {
			i++
			continue
		}
		if !strings.HasPrefix(args[i], "-") {
			break
		}
	}
	if i >= len(args) || args[i] != "resume" {
		return resumeRequest{}, false
	}
	request := resumeRequest{}
	last := -1
	for j := i + 1; j < len(args); j++ {
		arg := args[j]
		switch {
		case arg == "--":
			j = len(args)
		case codexValueOptions[arg]:
			j++
		case arg == "--last":
			last = j
		case arg == "--all":
			request.all = true
		case conversationPattern.MatchString(arg) && request.id == "":
			request.id = arg
		}
	}
	if request.id != "" {
		return request, true
	}
	if last < 0 {
		return resumeRequest{}, false
	}
	request.latest = true
	request.rewrite = func(id string) []string {
		out := []string{}
		for j, arg := range args {
			switch {
			case j == last:
				out = append(out, id)
			case j > i && arg == "--all":
			default:
				out = append(out, arg)
			}
		}
		return out
	}
	return request, true
}

// CollectLocalSession returns the conversation a takeover launch resumes and
// the arguments to launch with ("continue latest" becomes the explicit ID it
// found on the Mac). It returns nil when the launch resumes nothing, or names
// a conversation this Mac does not have (it may already be in the cloud).
func CollectLocalSession(agentHome, agent, directory, launchID string, args []string) (*SessionImport, []string, error) {
	request, ok := parseResume(agent, args)
	if !ok || agentHome == "" {
		return nil, args, nil
	}
	var bundle *SessionImport
	var err error
	switch agent {
	case "claude":
		bundle, err = collectClaude(agentHome, directory, request)
	case "codex":
		bundle, err = collectCodex(agentHome, directory, request)
	}
	if err != nil || bundle == nil {
		return nil, args, err
	}
	bundle.Version, bundle.Agent, bundle.LaunchID = 1, agent, launchID
	if request.latest {
		args = request.rewrite(bundle.Conversation)
	}
	return bundle, args, nil
}

// ClaudeProjectDirectory is Claude Code's per-directory folder name: every
// character other than an ASCII letter or digit becomes "-".
func ClaudeProjectDirectory(directory string) string {
	var name strings.Builder
	for _, r := range directory {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			name.WriteRune(r)
		} else {
			name.WriteByte('-')
		}
	}
	return name.String()
}

func collectClaude(configDir, directory string, request resumeRequest) (*SessionImport, error) {
	projects := filepath.Join(configDir, "projects")
	transcript := ""
	if request.latest {
		newest := int64(0)
		for _, candidate := range directoryVariants(directory) {
			entries, _ := os.ReadDir(filepath.Join(projects, ClaudeProjectDirectory(candidate)))
			for _, entry := range entries {
				id, ok := strings.CutSuffix(entry.Name(), ".jsonl")
				info, err := entry.Info()
				if !ok || !conversationPattern.MatchString(id) || err != nil || !info.Mode().IsRegular() {
					continue
				}
				if modified := info.ModTime().UnixNano(); modified > newest {
					newest, transcript = modified, filepath.Join(projects, ClaudeProjectDirectory(candidate), entry.Name())
				}
			}
		}
	} else {
		matches, _ := filepath.Glob(filepath.Join(projects, "*", request.id+".jsonl"))
		newest := int64(0)
		for _, match := range matches {
			if info, err := os.Lstat(match); err == nil && info.Mode().IsRegular() && info.ModTime().UnixNano() > newest {
				newest, transcript = info.ModTime().UnixNano(), match
			}
		}
	}
	if transcript == "" {
		return nil, nil
	}
	id := strings.TrimSuffix(filepath.Base(transcript), ".jsonl")
	bundle := &SessionImport{Conversation: id}
	total := int64(0)
	add := func(file, relative string) error {
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if total += info.Size(); total > sessionImportLimit || len(bundle.Files) >= sessionImportFiles {
			return fmt.Errorf("conversation is too large to move to the cloud computer")
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		bundle.Files = append(bundle.Files, SessionFile{Path: relative, Data: data})
		return nil
	}
	if err := add(transcript, id+".jsonl"); err != nil {
		return nil, err
	}
	// Only what Claude loads to continue crosses the link: a long transcript
	// is mostly history from before its last compaction (screenshots, request
	// dumps) that the model never sees again.
	bundle.Files[0].Data = trimClaudeTranscript(bundle.Files[0].Data)
	if bundle.Marker = sessionMarker("claude", bundle.Files[0].Data); bundle.Marker == "" {
		return nil, nil
	}
	// Subagent transcripts and saved tool results live next to the transcript.
	sidecar := filepath.Join(filepath.Dir(transcript), id)
	err := filepath.WalkDir(sidecar, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(filepath.Dir(transcript), file)
		return add(file, filepath.ToSlash(relative))
	})
	if err != nil {
		return nil, err
	}
	return bundle, nil
}

func collectCodex(codexHome, directory string, request resumeRequest) (*SessionImport, error) {
	file := ""
	if request.latest {
		file = latestCodexRollout(codexHome, directory, request.all)
	} else if found, err := localssh.FindCodexRollout(codexHome, request.id); err == nil {
		file = found
	}
	if file == "" {
		return nil, nil
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil
	}
	if info.Size() > sessionImportLimit {
		return nil, fmt.Errorf("conversation is too large to move to the cloud computer")
	}
	id := request.id
	if id == "" {
		meta, ok := codexSessionMeta(file)
		if !ok {
			return nil, nil
		}
		id = meta.ID
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(codexHome, file)
	if err != nil {
		return nil, err
	}
	marker := sessionMarker("codex", data)
	if marker == "" {
		return nil, nil
	}
	return &SessionImport{Conversation: id, Marker: marker, Files: []SessionFile{{Path: filepath.ToSlash(relative), Data: data}}}, nil
}

type codexMeta struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

// codexSessionMeta reads the session_meta record that opens a plain rollout.
func codexSessionMeta(file string) (codexMeta, bool) {
	handle, err := os.Open(file)
	if err != nil {
		return codexMeta{}, false
	}
	defer handle.Close()
	line, err := bufio.NewReaderSize(handle, 64<<10).ReadSlice('\n')
	if err != nil && err != io.EOF {
		return codexMeta{}, false
	}
	var record struct {
		Type    string    `json:"type"`
		Payload codexMeta `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "session_meta" || !conversationPattern.MatchString(record.Payload.ID) {
		return codexMeta{}, false
	}
	return record.Payload, true
}

// latestCodexRollout is the most recent rollout started in directory (or in
// any directory with all), as "codex resume --last" would choose on the Mac.
func latestCodexRollout(codexHome, directory string, all bool) string {
	var files []string
	filepath.WalkDir(filepath.Join(codexHome, "sessions"), func(file string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, file)
		}
		return nil
	})
	// Rollouts are named by start time under dated folders; check the newest
	// few hundred by modification time.
	type candidate struct {
		file     string
		modified int64
	}
	candidates := make([]candidate, 0, len(files))
	for _, file := range files {
		if info, err := os.Stat(file); err == nil {
			candidates = append(candidates, candidate{file, info.ModTime().UnixNano()})
		}
	}
	sort.Slice(candidates, func(a, b int) bool { return candidates[a].modified > candidates[b].modified })
	variants := directoryVariants(directory)
	for i, candidate := range candidates {
		if i >= 500 {
			break
		}
		meta, ok := codexSessionMeta(candidate.file)
		if !ok {
			continue
		}
		if all {
			return candidate.file
		}
		for _, variant := range variants {
			if filepath.Clean(meta.Cwd) == variant {
				return candidate.file
			}
		}
	}
	return ""
}

// directoryVariants is the directory as given and with symlinks resolved
// (/tmp and /private/tmp on macOS).
func directoryVariants(directory string) []string {
	variants := []string{filepath.Clean(directory)}
	if resolved, err := filepath.EvalSymlinks(directory); err == nil && resolved != variants[0] {
		variants = append(variants, resolved)
	}
	return variants
}

// The wire format, zstd-compressed as a whole: one JSON line with the import
// and each file's size (Data omitted), then the files' bytes in order. Raw
// bytes keep transcripts compressible; base64 inside JSON would not be.
type sessionImportHeader struct {
	SessionImport
	Sizes []int64 `json:"sizes"`
}

// EncodeSessionImport is the payload sent to SessionImportCommand.
func EncodeSessionImport(bundle *SessionImport) ([]byte, error) {
	header := sessionImportHeader{SessionImport: *bundle}
	header.Files = make([]SessionFile, len(bundle.Files))
	for i, file := range bundle.Files {
		header.Files[i] = SessionFile{Path: file.Path}
		header.Sizes = append(header.Sizes, int64(len(file.Data)))
	}
	raw, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder, err := zstd.NewWriter(&output, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	encoder.Write(append(raw, '\n'))
	for _, file := range bundle.Files {
		encoder.Write(file.Data)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// ReceiveSessionImport reads one import from the Mac and applies it.
func ReceiveSessionImport(home string, input io.Reader) (int, error) {
	decoder, err := zstd.NewReader(input, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return 0, err
	}
	defer decoder.Close()
	reader := bufio.NewReader(io.LimitReader(decoder, sessionImportLimit+(4<<20)))
	line, err := reader.ReadBytes('\n')
	var header sessionImportHeader
	if err != nil || len(line) > 4<<20 || json.Unmarshal(line, &header) != nil || len(header.Sizes) != len(header.Files) || len(header.Files) > sessionImportFiles {
		return 0, fmt.Errorf("invalid conversation import")
	}
	total := int64(0)
	for i, size := range header.Sizes {
		if total += size; size < 0 || total > sessionImportLimit {
			return 0, fmt.Errorf("conversation import is too large")
		}
		header.Files[i].Data = make([]byte, size)
		if _, err := io.ReadFull(reader, header.Files[i].Data); err != nil {
			return 0, fmt.Errorf("incomplete conversation import")
		}
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return 0, fmt.Errorf("invalid conversation import")
	}
	return ApplySessionImport(home, header.SessionImport)
}

// ApplySessionImport writes a conversation from the Mac where the cloud agent
// looks for it, unless a cloud copy already holds its latest record (the same
// conversation, or one continued in an earlier cloud session).
func ApplySessionImport(home string, bundle SessionImport) (int, error) {
	if err := bundle.validate(); err != nil {
		return 0, err
	}
	if HasCloudSession(home, bundle.Agent, bundle.Conversation, bundle.Marker) {
		return 0, nil
	}
	workspace := filepath.Join(home, ".fen", bundle.LaunchID)
	written := 0
	switch bundle.Agent {
	case "claude":
		project := filepath.Join(cloudAgentHome(home, "claude"), "projects", ClaudeProjectDirectory(workspace))
		for _, file := range bundle.Files {
			data := file.Data
			if strings.HasSuffix(file.Path, ".jsonl") {
				data = mapWorkingDirectory(data, workspace)
			}
			if err := writeImported(filepath.Join(project, filepath.FromSlash(file.Path)), data); err != nil {
				return written, err
			}
			written++
		}
	case "codex":
		codexHome := cloudAgentHome(home, "codex")
		provider := codexConfiguredProvider(codexHome)
		file := bundle.Files[0]
		data, err := transformRolloutFile(path.Base(file.Path), file.Data, func(plain []byte) []byte {
			plain = mapWorkingDirectory(plain, workspace)
			if provider != "" && provider != "openai" {
				for _, field := range []string{"model_provider", "model_provider_id"} {
					plain = bytes.ReplaceAll(plain, []byte(`"`+field+`":"openai"`), []byte(`"`+field+`":"`+provider+`"`))
				}
			}
			return plain
		})
		if err != nil {
			return 0, err
		}
		destination := filepath.Join(codexHome, filepath.FromSlash(file.Path))
		if existing, err := localssh.FindCodexRollout(codexHome, bundle.Conversation); err == nil {
			if strings.HasSuffix(existing, ".zst") || path.Ext(existing) != path.Ext(file.Path) {
				// Compressed in the cloud: Codex wrote or continued it there.
				return 0, nil
			}
			destination = existing
		}
		if err := writeImported(destination, data); err != nil {
			return 0, err
		}
		written++
	}
	return written, nil
}

// cloudAgentHome is where the cloud agent keeps conversations: the isolated
// native configuration, or the default directory under the gateway.
func cloudAgentHome(home, agent string) string {
	native := setup.NativeAgentMode(home)
	switch agent {
	case "claude":
		if native {
			return filepath.Join(home, ".local/fennec/native/claude")
		}
		return filepath.Join(home, ".claude")
	case "codex":
		if native {
			return filepath.Join(home, ".local/fennec/native/codex")
		}
		return filepath.Join(home, ".codex")
	}
	return ""
}

// markerPattern bounds a conversation marker: a Claude record UUID or a Codex
// RFC 3339 timestamp. It is passed as a command argument, so only these
// characters are accepted.
var markerPattern = regexp.MustCompile(`^[0-9A-Za-z:.+-]{1,64}$`)

// markerField is how the marker appears in a transcript line.
func markerField(agent, marker string) []byte {
	if agent == "codex" {
		return []byte(`"timestamp":"` + marker + `"`)
	}
	return []byte(`"uuid":"` + marker + `"`)
}

// sessionMarker names a conversation's latest record: Claude's last record
// UUID, or the timestamp of Codex's last rollout line.
func sessionMarker(agent string, transcript []byte) string {
	lines := bytes.Split(bytes.TrimRight(transcript, "\n"), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var record struct {
			UUID      string `json:"uuid"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(lines[i], &record) != nil {
			continue
		}
		value := record.UUID
		if agent == "codex" {
			value = record.Timestamp
		}
		if value != "" {
			if !markerPattern.MatchString(value) {
				return ""
			}
			return value
		}
	}
	return ""
}

// HasCloudSession reports whether the cloud computer already has conversation
// up to marker, so the Mac need not send it again.
func HasCloudSession(home, agent, conversation, marker string) bool {
	if !conversationPattern.MatchString(conversation) || !markerPattern.MatchString(marker) {
		return false
	}
	var copies []string
	switch agent {
	case "claude":
		copies, _ = filepath.Glob(filepath.Join(cloudAgentHome(home, "claude"), "projects", "*", conversation+".jsonl"))
	case "codex":
		if found, err := localssh.FindCodexRollout(cloudAgentHome(home, "codex"), conversation); err == nil {
			copies = []string{found}
		}
	}
	field := markerField(agent, marker)
	for _, copy := range copies {
		if strings.HasSuffix(copy, ".zst") {
			return true
		}
		if data, err := os.ReadFile(copy); err == nil && bytes.Contains(data, field) {
			return true
		}
	}
	return false
}

// trimClaudeTranscript keeps what Claude Code loads to continue a compacted
// conversation: everything from the last compact boundary on, the messages
// that compaction preserved from before it, and small session metadata (title,
// mode, file-history snapshots for rewind). Earlier messages and the request
// dumps Claude records for its own diagnostics stay on the Mac. A transcript
// without a compaction is sent whole.
func trimClaudeTranscript(data []byte) []byte {
	lines := bytes.SplitAfter(data, []byte("\n"))
	boundary := -1
	preserved := map[string]bool{}
	var head, tail string
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"compact_boundary"`)) {
			continue
		}
		var record struct {
			Type     string `json:"type"`
			Subtype  string `json:"subtype"`
			Metadata struct {
				Messages struct {
					All   []string `json:"allUuids"`
					Uuids []string `json:"uuids"`
				} `json:"preservedMessages"`
				Segment struct {
					Head string `json:"headUuid"`
					Tail string `json:"tailUuid"`
				} `json:"preservedSegment"`
			} `json:"compactMetadata"`
		}
		if json.Unmarshal(lines[i], &record) != nil || record.Type != "system" || record.Subtype != "compact_boundary" {
			continue
		}
		boundary = i
		for _, id := range append(record.Metadata.Messages.All, record.Metadata.Messages.Uuids...) {
			preserved[id] = true
		}
		head, tail = record.Metadata.Segment.Head, record.Metadata.Segment.Tail
		break
	}
	if boundary <= 0 {
		return data
	}
	type entry struct {
		Type   string  `json:"type"`
		UUID   *string `json:"uuid"`
		Parent *string `json:"parentUuid"`
	}
	records := make([]entry, boundary)
	parents := map[string]string{}
	for i, line := range lines[:boundary] {
		if json.Unmarshal(line, &records[i]) != nil {
			records[i].Type = "\x00invalid"
			continue
		}
		if records[i].UUID != nil && records[i].Parent != nil {
			parents[*records[i].UUID] = *records[i].Parent
		}
	}
	// A preserved segment is the chain from its tail back to its head.
	for id, steps := tail, 0; id != "" && steps < boundary; steps++ {
		preserved[id] = true
		if id == head {
			break
		}
		id = parents[id]
	}
	output := make([]byte, 0, len(data)/8)
	for i, line := range lines[:boundary] {
		record := records[i]
		switch {
		case record.Type == "\x00invalid" || strings.HasPrefix(record.Type, "api-request"):
		case record.UUID != nil && *record.UUID != "":
			if preserved[*record.UUID] {
				output = append(output, line...)
			}
		default:
			output = append(output, line...)
		}
	}
	for _, line := range lines[boundary:] {
		output = append(output, line...)
	}
	return output
}

func (bundle SessionImport) validate() error {
	if bundle.Version != 1 || !markerPattern.MatchString(bundle.Marker) || !sessionIDPattern.MatchString(bundle.LaunchID) || !conversationPattern.MatchString(bundle.Conversation) || len(bundle.Files) == 0 || len(bundle.Files) > sessionImportFiles {
		return fmt.Errorf("invalid conversation import")
	}
	total := 0
	for _, file := range bundle.Files {
		total += len(file.Data)
		clean := path.Clean(file.Path)
		if clean != file.Path || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsAny(clean, "\x00\\") {
			return fmt.Errorf("invalid conversation file path")
		}
		switch bundle.Agent {
		case "claude":
			if clean != bundle.Conversation+".jsonl" && !strings.HasPrefix(clean, bundle.Conversation+"/") {
				return fmt.Errorf("invalid conversation file path")
			}
		case "codex":
			if len(bundle.Files) != 1 || !strings.HasPrefix(clean, "sessions/") && !strings.HasPrefix(clean, "archived_sessions/") || !localssh.IsCodexRolloutName(path.Base(clean), bundle.Conversation) {
				return fmt.Errorf("invalid conversation file path")
			}
		default:
			return fmt.Errorf("unsupported agent")
		}
	}
	if total > sessionImportLimit {
		return fmt.Errorf("conversation import is too large")
	}
	return nil
}

// mapWorkingDirectory points every "cwd" field at the cloud workspace, so the
// agent continues where the cloud session runs. Other bytes are unchanged.
func mapWorkingDirectory(data []byte, workspace string) []byte {
	value, _ := json.Marshal(workspace)
	return cwdField.ReplaceAllLiteral(data, append([]byte(`"cwd":`), value...))
}

var codexProviderLine = regexp.MustCompile(`(?m)^\s*model_provider\s*=\s*"([A-Za-z0-9_-]+)"`)

// codexConfiguredProvider is the cloud Codex's top-level model_provider, so a
// Mac conversation recorded under "openai" resumes under the gateway.
func codexConfiguredProvider(codexHome string) string {
	data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return ""
	}
	// Only the top level: stop at the first table header.
	if header := regexp.MustCompile(`(?m)^\s*\[`).FindIndex(data); header != nil {
		data = data[:header[0]]
	}
	if match := codexProviderLine.FindSubmatch(data); match != nil {
		return string(match[1])
	}
	return ""
}

// writeImported writes one conversation file atomically.
func writeImported(destination string, data []byte) error {
	if info, err := os.Lstat(destination); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("conversation destination is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(destination, data)
}
