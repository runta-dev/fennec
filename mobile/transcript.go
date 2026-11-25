package fennecmobile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fennec/internal/transcript"
)

// The chat view reads the agent's own session log; Herdr only supplies the
// live state. A session log is found from the Herdr terminal's process:
//   - Claude Code writes <CLAUDE_CONFIG_DIR or ~/.claude>/sessions/<pid>.json
//     with its sessionId and cwd; the log is projects/<cwd, non-alphanumerics
//     as ->/<sessionId>.jsonl.
//   - Codex keeps sessions/YYYY/MM/DD/rollout-*.jsonl open while it runs.
//   - Pi appends to <PI_CODING_AGENT_DIR or ~/.pi/agent>/sessions/--<cwd,
//     leading slash dropped, other slashes and colons as ->--/<time>_<id>.jsonl
//     without keeping it open; the newest file there is the running session's.
//   - Kimi Code lists sessions in <KIMI_CODE_HOME or ~/.kimi-code>/session_index.jsonl
//     ({"sessionDir","workDir"}); the latest one for the agent's cwd is its
//     session, logged to <sessionDir>/agents/main/wire.jsonl.
//   - Antigravity CLI lists conversations with their folder in
//     ~/.gemini/antigravity-cli/conversation_summaries.db; `fennec-terminal
//     agy-transcript <cwd>` prints the newest one's transcript_full.jsonl.

const (
	maxTranscriptRead = 2 << 20 // bytes of new lines per read
	// A single line can outgrow a read: a message with a pasted screenshot
	// carries it as base64. Such a line is read again whole, up to this.
	maxTranscriptLine = 32 << 20
	// How far back the first read looks. Only the lines it keeps are sent.
	initialTranscriptWindow = 16 << 20
	pathTTL                 = 10 * time.Second
)

var nonAlphanumeric = regexp.MustCompile(`[^A-Za-z0-9]`)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (s *Sessions) exec(command string) ([]byte, error) {
	output, _, err := s.run(func(string) (string, error) { return command, nil })
	return output, err
}

// read runs a command that only reads, so it may repeat after a dropped link.
func (s *Sessions) read(command string) ([]byte, error) {
	output, _, err := s.runRead(func(string) (string, error) { return command, nil })
	return output, err
}

// transcriptPath resolves (and caches) the session log behind a terminal.
func (s *Sessions) transcriptPath(address, sessionID string) (string, error) {
	key := address + "/" + sessionID
	s.mu.Lock()
	// Agents move to a new log (a new conversation, or a host that starts a
	// log per turn), so a resolved path is trusted only briefly.
	if path, ok := s.paths[key]; ok && time.Since(s.pathsAt[key]) < pathTTL {
		s.mu.Unlock()
		return path, nil
	}
	s.mu.Unlock()
	if !addressPattern.MatchString(address) || !sessionPattern.MatchString(sessionID) {
		return "", errors.New("invalid terminal")
	}
	raw, err := s.read(`"$HOME/.local/bin/herdr-core" get ` + address + ` --json`)
	if err != nil {
		return "", err
	}
	var terminal struct {
		PID    int               `json:"pid"`
		Labels map[string]string `json:"labels"`
	}
	if json.Unmarshal(raw, &terminal) != nil || terminal.Labels["fennec.session"] != sessionID || terminal.PID <= 1 {
		return "", errors.New("session not found")
	}
	table, err := s.read(`ps -eo pid=,ppid=`)
	if err != nil {
		return "", err
	}
	pids := descendants(string(table), terminal.PID)
	var script strings.Builder
	if terminal.Labels["fennec.agent"] == "kimi" {
		p := strconv.Itoa(pids[len(pids)-1])
		fmt.Fprintf(&script, `c=$(readlink /proc/%s/cwd); h=$(tr '\0' '\n' < /proc/%s/environ 2>/dev/null | sed -n 's/^KIMI_CODE_HOME=//p'); h=${h:-$HOME/.kimi-code}; grep -F "\"workDir\":\"$c\"" "$h/session_index.jsonl" 2>/dev/null | tail -n 1 | sed -n 's/.*"sessionDir":"\([^"]*\)".*/kimi \1/p'; `, p, p)
	}
	if terminal.Labels["fennec.agent"] == "agy" {
		// agy's own index names the newest conversation of its folder.
		p := strconv.Itoa(pids[len(pids)-1])
		fmt.Fprintf(&script, `c=$(readlink /proc/%s/cwd); "$HOME/.local/fennec/bin/fennec-terminal" agy-transcript "$c" 2>/dev/null | sed -n 's|^/|agy /|p'; `, p)
	}
	if terminal.Labels["fennec.agent"] == "pi" {
		// The deepest process is Pi itself; its cwd names the sessions folder.
		p := strconv.Itoa(pids[len(pids)-1])
		fmt.Fprintf(&script, `c=$(readlink /proc/%s/cwd); d=$(tr '\0' '\n' < /proc/%s/environ 2>/dev/null | sed -n 's/^PI_CODING_AGENT_DIR=//p'); d=${d:-$HOME/.pi/agent}; printf 'pi %%s %%s\n' "$d" "$c"; `, p, p)
	}
	for _, pid := range pids {
		p := strconv.Itoa(pid)
		// One block per process: its Claude session record, then open logs.
		fmt.Fprintf(&script, `echo "@%s"; d=$(tr '\0' '\n' < /proc/%s/environ 2>/dev/null | sed -n 's/^CLAUDE_CONFIG_DIR=//p'); d=${d:-$HOME/.claude}; [ -f "$d/sessions/%s.json" ] && { printf 'claude %%s ' "$d"; tr -d '\n' < "$d/sessions/%s.json"; echo; }; ls -l /proc/%s/fd 2>/dev/null | sed -n 's/.* -> \(.*rollout-.*\.jsonl\)$/open \1/p'; `, p, p, p, p, p)
	}
	out, err := s.read(script.String())
	if err != nil {
		return "", err
	}
	path := pickTranscript(string(out))
	for _, line := range strings.Split(string(out), "\n") {
		if dir, ok := strings.CutPrefix(line, "kimi "); ok && path == "" && strings.HasPrefix(dir, "/") && !strings.Contains(dir, "..") {
			path = strings.TrimSpace(dir) + "/agents/main/wire.jsonl"
		}
		if log, ok := strings.CutPrefix(line, "agy "); ok && path == "" {
			if log = strings.TrimSpace(log); strings.HasPrefix(log, "/") && !strings.Contains(log, "..") && strings.HasSuffix(log, "/.system_generated/logs/transcript_full.jsonl") {
				path = log
			}
		}
	}
	if dir := piSessions(string(out)); path == "" && dir != "" {
		if newest, err := s.read(`ls -t ` + shellQuote(dir) + `/*.jsonl 2>/dev/null | head -n 1`); err == nil {
			path = strings.TrimSpace(string(newest))
		}
	}
	if path == "" {
		return "", nil
	}
	s.mu.Lock()
	if s.paths == nil {
		s.paths = map[string]string{}
	}
	s.paths[key] = path
	if s.pathsAt == nil {
		s.pathsAt = map[string]time.Time{}
	}
	s.pathsAt[key] = time.Now()
	s.mu.Unlock()
	return path, nil
}

// descendants lists root and every process below it, root first.
func descendants(table string, root int) []int {
	children := map[int][]int{}
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 == nil && err2 == nil {
			children[ppid] = append(children[ppid], pid)
		}
	}
	out := []int{root}
	for i := 0; i < len(out) && len(out) < 256; i++ {
		out = append(out, children[out[i]]...)
	}
	return out
}

// piSessions is the folder Pi writes this agent's logs to, from the
// resolver's "pi <agent dir> <cwd>" line.
func piSessions(output string) string {
	for _, line := range strings.Split(output, "\n") {
		rest, ok := strings.CutPrefix(line, "pi ")
		if !ok {
			continue
		}
		dir, cwd, ok := strings.Cut(rest, " /")
		if !ok || dir == "" {
			continue
		}
		encoded := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimSpace(cwd))
		return dir + "/sessions/--" + encoded + "--"
	}
	return ""
}

// pickTranscript chooses from the resolver's output: a Claude session record
// first (the agent's own claim), else a Codex log held open.
func pickTranscript(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "claude ") {
			continue
		}
		rest := strings.TrimPrefix(line, "claude ")
		brace := strings.Index(rest, "{")
		if brace < 1 {
			continue
		}
		dir := strings.TrimSpace(rest[:brace])
		var record struct {
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
		}
		if json.Unmarshal([]byte(rest[brace:]), &record) != nil || record.SessionID == "" || record.CWD == "" || strings.ContainsAny(record.SessionID, "/.") {
			continue
		}
		return dir + "/projects/" + nonAlphanumeric.ReplaceAllString(record.CWD, "-") + "/" + record.SessionID + ".jsonl"
	}
	for _, line := range strings.Split(output, "\n") {
		if path, ok := strings.CutPrefix(line, "open "); ok && strings.HasSuffix(path, ".jsonl") {
			return path
		}
	}
	return ""
}

// Transcript returns {"items":[...],"offset":n,"reset":bool,"path":...}.
// Pass offset -1 for the latest part of the log, then the returned offset to
// get only new lines. reset means start over: drop what was shown.
//
// Lines that only carry tool output (results, file contents, screenshots) are
// most of a log and never shown, so awk drops them on the cloud computer and
// tags each kept line with its byte offset. The first read can then look far
// back for the earlier conversation while sending little.
func (s *Sessions) Transcript(address, sessionID string, offset int64) (out0 string, err error) {
	defer func() { err = classify(err) }()
	path, err := s.transcriptPath(address, sessionID)
	if err != nil {
		return "", err
	}
	return s.readTranscript(address, sessionID, path, offset)
}

func (s *Sessions) readTranscript(address, sessionID, path string, offset int64) (string, error) {
	return s.readTranscriptLimit(address, sessionID, path, offset, maxTranscriptRead)
}

func (s *Sessions) readTranscriptLimit(address, sessionID, path string, offset, limit int64) (string, error) {
	if path == "" {
		// Some agents create their log only after the first prompt. Keep the
		// cursor and existing messages while the next poll resolves the log.
		result, err := json.Marshal(map[string]any{"items": []transcript.Item{}, "offset": offset, "reset": false})
		return string(result), err
	}
	file := shellQuote(path)
	var command string
	if offset < 0 {
		command = fmt.Sprintf(`s=$(stat -c %%s %s) && st=$((s > %d ? s - %d : 0)) && echo "$s $st" && tail -c +$((st + 1)) %s | LC_ALL=C awk -v skip=$((st > 0)) %s`,
			file, initialTranscriptWindow, initialTranscriptWindow, file, keepLines)
	} else {
		command = fmt.Sprintf(`s=$(stat -c %%s %s) && echo "$s %d" && if [ "$s" -ge %d ]; then tail -c +%d %s | head -c %d | LC_ALL=C awk -v skip=0 %s; fi`,
			file, offset, offset, offset+1, file, limit, keepLines)
	}
	command = `[ -e ` + file + ` ] || { printf '__FENNEC_TRANSCRIPT_PENDING__\n'; exit 0; }; ` + command
	raw, err := s.read(command)
	if err != nil {
		s.mu.Lock()
		delete(s.paths, address+"/"+sessionID) // the agent may have moved to a new log
		s.mu.Unlock()
		return "", err
	}
	if string(bytes.TrimSpace(raw)) == "__FENNEC_TRANSCRIPT_PENDING__" {
		s.mu.Lock()
		delete(s.paths, address+"/"+sessionID)
		s.mu.Unlock()
		return s.readTranscript(address, sessionID, "", offset)
	}
	size, _, items, position, err := parseKeptLines(raw, offset, limit)
	if err != nil {
		return "", err
	}
	// The next line alone is longer than this read: read it whole rather
	// than skip it (it may be a message with images).
	if offset >= 0 && position == offset && size-offset > limit && limit < maxTranscriptLine {
		return s.readTranscriptLimit(address, sessionID, path, offset, maxTranscriptLine)
	}
	if offset >= 0 && size < offset {
		return s.Transcript(address, sessionID, -1) // the log was replaced or truncated
	}
	result, err := json.Marshal(map[string]any{"items": items, "offset": position, "reset": offset < 0, "path": path, "version": transcript.Version})
	return string(result), err
}

// keepLines is an awk program (options go before it) that prints
// "<offset> <line>" for each line worth showing, relative to where the read
// began, then "@end <bytes> <last line length>". Tool results that carry a
// message sent mid-turn are kept. skip=1 drops the first line,
// which a read from the middle of the log starts inside.
const keepLines = `'{ n = length($0) + 1; if (!(NR == 1 && skip) && ($0 !~ /"type":"tool_result"|"role":"toolResult"|"type":"function_call_output"|"type":"custom_tool_call_output"|"type":"tool.result"|"type":"llm.tools_snapshot"|"type":"tool_select.initialized"|"type":"profile.bind"/ || index($0, "The user sent a new message while you were working"))) printf "%d %s\n", off, $0; off += n; last = n } END { printf "@end %d %d\n", off, last }'`

// parseKeptLines reads keepLines' output after a "<size> <start>" header and
// returns the items and the offset to read from next. A last line without a
// newline is still being written: it is left for the next read.
func parseKeptLines(raw []byte, offset, limit int64) (size, start int64, items []transcript.Item, position int64, err error) {
	header, rest, _ := bytes.Cut(raw, []byte("\n"))
	if _, err = fmt.Sscanf(string(header), "%d %d", &size, &start); err != nil {
		return 0, 0, nil, 0, errors.New("unreadable conversation log")
	}
	available := size - start
	if offset >= 0 && available > limit {
		available = limit
	}
	type kept struct {
		at   int64
		line []byte
	}
	var lines []kept
	var consumed, last int64
	for len(rest) > 0 {
		var line []byte
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		if end, ok := bytes.CutPrefix(line, []byte("@end ")); ok {
			fmt.Sscanf(string(end), "%d %d", &consumed, &last)
			break
		}
		at, body, ok := bytes.Cut(line, []byte(" "))
		if !ok {
			continue
		}
		n, convErr := strconv.ParseInt(string(at), 10, 64)
		if convErr != nil {
			continue
		}
		lines = append(lines, kept{n, body})
	}
	if consumed > available {
		// The last line had no newline yet.
		consumed -= last
		if len(lines) > 0 && lines[len(lines)-1].at == consumed {
			lines = lines[:len(lines)-1]
		}
		if consumed == 0 && available >= limit && limit >= maxTranscriptLine {
			consumed = available // one line longer than a read: step over it
		}
	}
	items = []transcript.Item{}
	for _, l := range lines {
		items = append(items, transcript.Parse(l.line, start+l.at)...)
	}
	if offset >= 0 && size < offset {
		return size, start, items, offset, nil
	}
	return size, start, items, start + consumed, nil
}
