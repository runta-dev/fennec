package localssh

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/audit"
	"fennec/internal/guardian"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/klauspost/compress/zstd"
)

// CodexRolloutCommand returns one Codex thread's rollout from this Mac so a
// cloud ChatGPT session can resume a conversation that started locally. Only
// a thread ID is accepted; the file is looked up under the Mac's Codex
// sessions and sent zstd-compressed after a one-line JSON header.
const CodexRolloutCommand = "fennec-codex-rollout"

// CodexRolloutHeader precedes the compressed rollout bytes.
type CodexRolloutHeader struct {
	Path string `json:"path"` // relative to CODEX_HOME, e.g. sessions/2026/11/11/rollout-…jsonl
	Size int64  `json:"size"`
}

// CodexThreadID matches Codex thread identifiers (UUIDs).
var CodexThreadID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const maxRolloutBytes = 512 << 20

// FindCodexRollout returns the rollout path for id under codexHome, searching
// active then archived sessions.
func FindCodexRollout(codexHome, id string) (string, error) {
	suffix := "-" + id + ".jsonl"
	for _, root := range []string{"sessions", "archived_sessions"} {
		found := ""
		filepath.WalkDir(filepath.Join(codexHome, root), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), suffix) {
				found = path
				return fs.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}
	return "", os.ErrNotExist
}

func serveCodexRollout(operation context.Context, session ssh.Session, broker *Broker, command, fingerprint string, requireApproval bool) bool {
	id, ok := strings.CutPrefix(command, CodexRolloutCommand+" ")
	if !ok {
		return false
	}
	id = strings.TrimSpace(id)
	display := "ChatGPT resume thread " + id
	started := time.Now()
	record := func(decision string, blocked bool, code int, detail string) {
		broker.recordAudit(audit.Entry{Kind: "exec", Command: display, Decision: decision, Blocked: blocked, ExitCode: code, DurationMS: time.Since(started).Milliseconds(), Detail: detail})
	}
	fail := func(code int, message string) bool {
		io.WriteString(session.Stderr(), "Fennec: "+message+"\n")
		session.Exit(code)
		return true
	}
	if !CodexThreadID.MatchString(id) {
		record(decisionForbidden, true, 126, "invalid thread id")
		return fail(126, "invalid thread id")
	}
	decision := "auto_sandbox"
	if requireApproval {
		approval, cancel := context.WithTimeout(operation, 2*time.Minute)
		outcome := broker.authorize(approval, approvalRequest{Display: display, Fingerprint: fingerprint, Action: guardian.Action{Kind: "exec", Command: display, Justification: "ChatGPT on the cloud computer continues a conversation that started on this Mac"}})
		cancel()
		if !outcome.Allowed {
			record(outcome.Decision, true, 126, "Denied or expired during approval prompt")
			return fail(126, "resuming this conversation in the cloud was not approved")
		}
		decision = outcome.Decision
	}
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	path, err := FindCodexRollout(codexHome, id)
	if err != nil {
		record(decision, false, 2, "rollout not found")
		return fail(2, "conversation not found on this Mac")
	}
	file, err := os.Open(path)
	if err != nil {
		record(decision, false, 1, err.Error())
		return fail(1, "conversation unreadable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRolloutBytes {
		record(decision, true, 1, "rollout not a regular file within the size limit")
		return fail(1, "conversation unreadable")
	}
	relative, _ := filepath.Rel(codexHome, path)
	header, _ := json.Marshal(CodexRolloutHeader{Path: filepath.ToSlash(relative), Size: info.Size()})
	if _, err := session.Write(append(header, '\n')); err != nil {
		return true
	}
	encoder, err := zstd.NewWriter(session, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return fail(1, "compression unavailable")
	}
	_, copyErr := io.Copy(encoder, io.LimitReader(file, info.Size()))
	closeErr := encoder.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		record(decision, false, 1, err.Error())
		session.Exit(1)
		return true
	}
	record(decision, false, 0, "")
	session.Exit(0)
	return true
}
