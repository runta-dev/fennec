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
	"time"

	"github.com/klauspost/compress/zstd"
)

// migrateThreads lazily moves a conversation that started on the Mac to the
// cloud: a request naming a thread that has no rollout here waits while the
// rollout is copied from the Mac. Each thread is attempted once per
// connection; a missing or unreachable Mac leaves the request unchanged.
func (filter *chatgptPathFilter) migrateThreads(message []byte) {
	if filter.codexHome == "" || !bytes.Contains(message, []byte(`"threadId"`)) {
		return
	}
	var request struct {
		Params struct {
			ThreadID string `json:"threadId"`
		} `json:"params"`
	}
	if json.Unmarshal(message, &request) != nil || !localssh.CodexThreadID.MatchString(request.Params.ThreadID) {
		return
	}
	id := request.Params.ThreadID
	filter.threadsMu.Lock()
	checked := filter.threads[id]
	filter.threads[id] = true
	filter.threadsMu.Unlock()
	if checked {
		return
	}
	if _, err := localssh.FindCodexRollout(filter.codexHome, id); err == nil {
		return
	}
	if err := filter.importMacRollout(id); err != nil {
		fmt.Fprintf(os.Stderr, "fennec: conversation %s stays on the Mac: %v\n", id, err)
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
	if relative != header.Path || strings.Contains(relative, "..") || !(strings.HasPrefix(relative, "sessions/") || strings.HasPrefix(relative, "archived_sessions/")) || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, "-"+id+".jsonl") {
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
	data = filter.cloudRollout(data)
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
