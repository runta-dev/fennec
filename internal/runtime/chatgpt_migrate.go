package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/localssh"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// migratingMethods open or continue one conversation. Sidebar requests such as
// thread/attachment/list name many threads at once and never trigger a move.
var migratingMethods = map[string]bool{"thread/resume": true, "thread/read": true, "thread/turns/list": true, "thread/items/list": true, "thread/fork": true, "turn/start": true}

// migrationAttempts remembers, across ChatGPT reconnects, the cloud rollout path
// of each handled thread (string) or the time of a failed move (time.Time),
// keyed by CODEX_HOME and thread ID.
var migrationAttempts sync.Map

// migrateThreads lazily moves a conversation that started on the Mac to the
// cloud: a request naming a thread that has no rollout here waits while the
// rollout is copied from the Mac. Each thread is attempted once per
// connection; a missing or unreachable Mac leaves the request unchanged.
func (filter *chatgptPathFilter) migrateThreads(message []byte) {
	if filter.codexHome == "" || !bytes.Contains(message, []byte(`"threadId"`)) {
		return
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
		} `json:"params"`
	}
	if json.Unmarshal(message, &request) != nil || !migratingMethods[request.Method] || !localssh.CodexThreadID.MatchString(request.Params.ThreadID) {
		return
	}
	id := request.Params.ThreadID
	key := filter.codexHome + "\x00" + id
	// A rollout already handled is skipped while its file exists; a thread
	// deleted in the cloud moves again from the Mac on its next open.
	if cached, ok := migrationAttempts.Load(key); ok {
		switch value := cached.(type) {
		case string:
			if _, err := os.Stat(value); err == nil {
				return
			}
		case time.Time:
			if time.Since(value) < 30*time.Second {
				return
			}
		}
	}
	if path, err := localssh.FindCodexRollout(filter.codexHome, id); err == nil {
		if err := filter.adoptProvider(path); err != nil {
			fmt.Fprintf(os.Stderr, "fennec: conversation %s keeps its provider: %v\n", id, err)
		}
		migrationAttempts.Store(key, path)
		return
	}
	if err := filter.importMacRollout(id); err != nil {
		fmt.Fprintf(os.Stderr, "fennec: conversation %s stays on the Mac: %v\n", id, err)
		// An offline Mac or a later local thread may succeed on a later open.
		migrationAttempts.Store(key, time.Now())
		return
	}
	if path, err := localssh.FindCodexRollout(filter.codexHome, id); err == nil {
		migrationAttempts.Store(key, path)
	}
}

// importMacRollout copies one rollout from the Mac through its Fennec SSH
// alias. Mac working directories become the same cloud session workspaces new
// threads use, so later turns run in the cloud with Fennec's instructions.
func (filter *chatgptPathFilter) importMacRollout(id string) error {
	if filter.device == "" {
		return fmt.Errorf("no Mac registered")
	}
	alias := localssh.RegisteredAlias(filter.home, filter.device)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", alias, localssh.CodexRolloutCommand+" "+id)
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return err
	}
	defer command.Wait()
	reader := bufio.NewReader(output)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("mac unavailable: %s", strings.TrimSpace(stderr.String()))
	}
	var header localssh.CodexRolloutHeader
	if json.Unmarshal(line, &header) != nil || header.Size < 0 || header.Size > 512<<20 {
		return fmt.Errorf("invalid rollout header")
	}
	relative := path.Clean(header.Path)
	name := path.Base(relative)
	if relative != header.Path || strings.Contains(relative, "..") || !(strings.HasPrefix(relative, "sessions/") || strings.HasPrefix(relative, "archived_sessions/")) || !localssh.IsCodexRolloutName(name, id) {
		return fmt.Errorf("unexpected rollout path")
	}
	decoder, err := zstd.NewReader(reader, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer decoder.Close()
	data, err := io.ReadAll(io.LimitReader(decoder, header.Size+1))
	if err != nil || int64(len(data)) != header.Size {
		return fmt.Errorf("incomplete rollout transfer")
	}
	if data, err = filter.cloudRolloutFile(name, data); err != nil {
		return err
	}
	destination := filepath.Join(filter.codexHome, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return nil
	}
	return writeFileAtomic(destination, data)
}

// cloudRollout maps the "cwd" fields of every rollout line to cloud session
// workspaces. Lines without a cwd are kept byte for byte.
func (filter *chatgptPathFilter) cloudRollout(data []byte) []byte {
	lines := bytes.SplitAfter(data, []byte("\n"))
	for i, line := range lines {
		if !bytes.Contains(line, []byte(`"cwd"`)) {
			continue
		}
		body := bytes.TrimRight(line, "\n")
		mapped := filter.toCloudPaths(body)
		if !bytes.Equal(mapped, body) {
			lines[i] = append(mapped, line[len(body):]...)
		}
	}
	return bytes.Join(lines, nil)
}

// cloudRolloutFile maps working directories in a rollout file, keeping Codex's
// own zstd compression for .jsonl.zst rollouts.
func (filter *chatgptPathFilter) cloudRolloutFile(name string, data []byte) ([]byte, error) {
	return transformRolloutFile(name, data, func(plain []byte) []byte { return filter.adoptRolloutProvider(filter.cloudRollout(plain)) })
}

func transformRolloutFile(name string, data []byte, transform func([]byte) []byte) ([]byte, error) {
	if !strings.HasSuffix(name, ".zst") {
		return transform(data), nil
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	plain, err := decoder.DecodeAll(data, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid compressed rollout: %w", err)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer encoder.Close()
	return encoder.EncodeAll(transform(plain), nil), nil
}

// adoptRolloutProvider records Magpie as the thread's model provider when cloud
// ChatGPT runs through Magpie. Codex refuses to resume a thread under a
// different provider ("This chat uses openai, but your current provider is
// magpie"); Magpie routes OpenAI models as well, so the conversation continues
// through it. Only these exact fields change.
func (filter *chatgptPathFilter) adoptRolloutProvider(data []byte) []byte {
	if filter.provider == "" || filter.provider == "openai" {
		return data
	}
	for _, field := range []string{"model_provider", "model_provider_id"} {
		data = bytes.ReplaceAll(data, []byte(`"`+field+`":"openai"`), []byte(`"`+field+`":"`+filter.provider+`"`))
	}
	return data
}

// adoptProvider applies adoptRolloutProvider to a rollout already in the cloud
// before ChatGPT first opens it in this service run.
func (filter *chatgptPathFilter) adoptProvider(path string) error {
	if filter.provider == "" || filter.provider == "openai" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := transformRolloutFile(filepath.Base(path), data, func(plain []byte) []byte {
		if !bytes.Contains(plain, []byte(`"model_provider":"openai"`)) {
			return plain
		}
		return filter.adoptRolloutProvider(plain)
	})
	if err != nil || bytes.Equal(updated, data) {
		return err
	}
	// Recompressing unchanged content still differs; compare decoded content.
	if strings.HasSuffix(path, ".zst") {
		decoder, _ := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		defer decoder.Close()
		before, _ := decoder.DecodeAll(data, nil)
		if !bytes.Contains(before, []byte(`"model_provider":"openai"`)) {
			return nil
		}
	}
	return writeFileAtomic(path, updated)
}
