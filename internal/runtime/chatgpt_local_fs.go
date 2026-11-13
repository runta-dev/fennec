package runtime

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ChatGPT.app reads and watches files through the app-server's fs/* methods:
// file previews, attachments, project skills and templates. With the
// app-server on the cloud computer those requests name Mac paths that do not
// exist there. ChatGPTLocalFiles runs on the Mac, in the bridge: it answers
// fs/* requests for Mac paths from the Mac's own file system, exactly as the
// local app-server would, and passes everything else (including fs/* on the
// cloud computer's own CODEX_HOME, such as ChatGPT's attachment registry) to
// the cloud. The agent loop never uses these methods, so a Mac going offline
// affects only the ChatGPT window that is already disconnected.

const (
	chatgptLocalReadLimit    = 128 << 20
	chatgptLocalWatchPeriod  = time.Second
	chatgptLocalWatchEntries = 4096
)

// ChatGPTLocalFiles serves one ChatGPT connection.
type ChatGPTLocalFiles struct {
	client wsStream // ChatGPT -> cloud, parsed for fs/* requests

	mu         sync.Mutex
	output     io.Writer // ChatGPT's connection
	headWanted bool      // the cloud's handshake response is still due
	head       []byte
	frames     []byte // incomplete server frame
	inMessage  bool   // between fragments of a server data message
	checked    bool   // the cloud output started like WebSocket frames
	raw        bool   // not WebSocket: passed through, nothing answered here
	queued     [][]byte
	cloudRoots []string // the cloud computer's home and CODEX_HOME
	closed     bool

	watchMu sync.Mutex
	watches map[string]chan struct{}
}

// NewChatGPTLocalFiles writes the cloud's output and its own answers to
// output. expectHead says whether the cloud's handshake response still passes
// through (it does not when the bridge answered the handshake itself).
func NewChatGPTLocalFiles(output io.Writer, expectHead bool) *ChatGPTLocalFiles {
	files := &ChatGPTLocalFiles{output: output, headWanted: expectHead, watches: map[string]chan struct{}{}}
	files.client.server = false
	return files
}

// ExpectHead sets, before the cloud's first output, whether its handshake
// response passes through here.
func (files *ChatGPTLocalFiles) ExpectHead(expect bool) {
	files.mu.Lock()
	defer files.mu.Unlock()
	files.headWanted = expect
}

// Feed takes bytes from ChatGPT and returns the bytes to send to the cloud.
func (files *ChatGPTLocalFiles) Feed(data []byte) ([]byte, error) {
	return files.client.feed(data, files.request)
}

// Write takes the cloud's output for ChatGPT. Answers produced on the Mac are
// placed between whole messages.
func (files *ChatGPTLocalFiles) Write(data []byte) (int, error) {
	files.mu.Lock()
	defer files.mu.Unlock()
	input := data
	if files.headWanted {
		files.head = append(files.head, input...)
		end := bytes.Index(files.head, []byte("\r\n\r\n"))
		if end < 0 {
			if len(files.head) > 64<<10 {
				return 0, fmt.Errorf("oversized app-server handshake")
			}
			return len(data), nil
		}
		if _, err := files.output.Write(files.head[:end+4]); err != nil {
			return 0, err
		}
		input = files.head[end+4:]
		files.head, files.headWanted = nil, false
	}
	if !files.raw && !files.checked && len(files.frames)+len(input) >= 2 {
		start := append(append([]byte(nil), files.frames...), input...)
		files.checked = true
		files.raw = start[0]&0x70 != 0 || start[0]&0x0f == 0 || start[1]&0x80 != 0
	}
	if files.raw {
		if len(files.frames) > 0 {
			input = append(files.frames, input...)
			files.frames = nil
		}
		_, err := files.output.Write(input)
		return len(data), err
	}
	files.frames = append(files.frames, input...)
	for {
		frame, size, err := parseWebSocketFrame(files.frames)
		if err != nil {
			return 0, err
		}
		if size == 0 {
			break
		}
		if _, err := files.output.Write(frame.raw); err != nil {
			return 0, err
		}
		files.frames = files.frames[size:]
		if frame.opcode < 0x8 {
			files.inMessage = !frame.fin
			if frame.opcode == 0x1 && frame.fin && files.cloudRoots == nil {
				files.learnCloudRoots(frame.payload)
			}
		}
	}
	if len(files.frames) == 0 {
		files.frames = nil
	}
	return len(data), files.flushLocked()
}

func (files *ChatGPTLocalFiles) atBoundaryLocked() bool {
	return !files.headWanted && len(files.frames) == 0 && !files.inMessage
}

func (files *ChatGPTLocalFiles) flushLocked() error {
	for len(files.queued) > 0 && files.atBoundaryLocked() {
		frame := files.queued[0]
		files.queued = files.queued[1:]
		if _, err := files.output.Write(frame); err != nil {
			return err
		}
	}
	return nil
}

// send delivers one message from the Mac to ChatGPT.
func (files *ChatGPTLocalFiles) send(message any) {
	payload, err := json.Marshal(message)
	if err != nil {
		return
	}
	files.mu.Lock()
	defer files.mu.Unlock()
	if files.closed {
		return
	}
	files.queued = append(files.queued, encodeWebSocketFrame(0x1, payload, false))
	files.flushLocked()
}

// Close stops this connection's watches.
func (files *ChatGPTLocalFiles) Close() {
	files.mu.Lock()
	files.closed = true
	files.mu.Unlock()
	files.watchMu.Lock()
	defer files.watchMu.Unlock()
	for id, stop := range files.watches {
		close(stop)
		delete(files.watches, id)
	}
}

// learnCloudRoots reads the cloud CODEX_HOME from the initialize response.
// Paths under it, or under the cloud user's home, belong to the cloud.
func (files *ChatGPTLocalFiles) learnCloudRoots(message []byte) {
	if !bytes.Contains(message, []byte(`"codexHome"`)) {
		return
	}
	var response struct {
		Result struct {
			CodexHome string `json:"codexHome"`
		} `json:"result"`
	}
	if json.Unmarshal(message, &response) != nil || !filepath.IsAbs(response.Result.CodexHome) {
		return
	}
	home := response.Result.CodexHome
	roots := []string{filepath.Clean(home)}
	switch parts := strings.Split(strings.TrimPrefix(filepath.Clean(home), "/"), "/"); {
	case parts[0] == "root":
		roots = append(roots, "/root")
	case parts[0] == "home" && len(parts) > 1:
		roots = append(roots, "/home/"+parts[1])
	}
	files.cloudRoots = roots
}

// local reports whether a path is on the Mac.
func (files *ChatGPTLocalFiles) local(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	files.mu.Lock()
	roots := files.cloudRoots
	files.mu.Unlock()
	if roots == nil {
		return false // before initialize, nothing is answered here
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return false
		}
	}
	return true
}

type fsRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Path            string `json:"path"`
		SourcePath      string `json:"sourcePath"`
		DestinationPath string `json:"destinationPath"`
		DataBase64      string `json:"dataBase64"`
		Recursive       *bool  `json:"recursive"`
		Force           *bool  `json:"force"`
		WatchID         string `json:"watchId"`
	} `json:"params"`
}

// request answers fs/* requests for Mac paths here and returns nil for them;
// other messages continue to the cloud unchanged.
func (files *ChatGPTLocalFiles) request(message []byte) []byte {
	if !bytes.Contains(message, []byte(`"fs/`)) {
		return message
	}
	var request fsRequest
	if json.Unmarshal(message, &request) != nil || len(request.ID) == 0 || !strings.HasPrefix(request.Method, "fs/") {
		return message
	}
	params := request.Params
	switch request.Method {
	case "fs/copy":
		source, destination := files.local(params.SourcePath), files.local(params.DestinationPath)
		if !source && !destination {
			return message
		}
		if source != destination {
			files.fail(request.ID, -32600, "Invalid request: copying between this Mac and the cloud computer is not supported")
			return nil
		}
	case "fs/unwatch":
		files.watchMu.Lock()
		_, mine := files.watches[params.WatchID]
		files.watchMu.Unlock()
		if !mine {
			return message
		}
	default:
		if !files.local(params.Path) {
			return message
		}
	}
	go files.serve(request)
	return nil
}

func (files *ChatGPTLocalFiles) reply(id json.RawMessage, result any) {
	files.send(map[string]any{"id": id, "result": result})
}

func (files *ChatGPTLocalFiles) fail(id json.RawMessage, code int, message string) {
	files.send(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
}

// osError words an error the way the app-server (Rust std::io) does, so
// ChatGPT recognizes missing files.
func osError(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		text := errno.Error()
		if text != "" {
			text = strings.ToUpper(text[:1]) + text[1:]
		}
		return fmt.Sprintf("%s (os error %d)", text, int(errno))
	}
	return err.Error()
}

func (files *ChatGPTLocalFiles) serve(request fsRequest) {
	params := request.Params
	failed := func(err error) { files.fail(request.ID, -32603, osError(err)) }
	switch request.Method {
	case "fs/getMetadata":
		link, err := os.Lstat(params.Path)
		if err != nil {
			failed(err)
			return
		}
		info := link
		if link.Mode()&fs.ModeSymlink != 0 {
			if target, err := os.Stat(params.Path); err == nil {
				info = target
			}
		}
		files.reply(request.ID, map[string]any{
			"isDirectory":  info.IsDir(),
			"isFile":       info.Mode().IsRegular(),
			"isSymlink":    link.Mode()&fs.ModeSymlink != 0,
			"createdAtMs":  createdAt(info).UnixMilli(),
			"modifiedAtMs": info.ModTime().UnixMilli(),
		})
	case "fs/readFile":
		file, err := os.Open(params.Path)
		if err != nil {
			failed(err)
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, chatgptLocalReadLimit+1))
		if err != nil {
			failed(err)
			return
		}
		if len(data) > chatgptLocalReadLimit {
			files.fail(request.ID, -32603, "File is too large")
			return
		}
		files.reply(request.ID, map[string]any{"dataBase64": base64.StdEncoding.EncodeToString(data)})
	case "fs/readDirectory":
		entries, err := os.ReadDir(params.Path)
		if err != nil {
			failed(err)
			return
		}
		listed := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			info, err := os.Stat(filepath.Join(params.Path, entry.Name())) // entries resolve symlinks
			isDirectory, isFile := entry.IsDir(), entry.Type().IsRegular()
			if err == nil {
				isDirectory, isFile = info.IsDir(), info.Mode().IsRegular()
			}
			listed = append(listed, map[string]any{"fileName": entry.Name(), "isDirectory": isDirectory, "isFile": isFile})
		}
		files.reply(request.ID, map[string]any{"entries": listed})
	case "fs/writeFile":
		data, err := base64.StdEncoding.DecodeString(params.DataBase64)
		if err != nil {
			files.fail(request.ID, -32600, "Invalid request: dataBase64 is not base64")
			return
		}
		if err := os.WriteFile(params.Path, data, 0644); err != nil {
			failed(err)
			return
		}
		files.reply(request.ID, map[string]any{})
	case "fs/createDirectory":
		var err error
		if params.Recursive == nil || *params.Recursive {
			err = os.MkdirAll(params.Path, 0755)
		} else {
			err = os.Mkdir(params.Path, 0755)
		}
		if err != nil {
			failed(err)
			return
		}
		files.reply(request.ID, map[string]any{})
	case "fs/remove":
		recursive := params.Recursive == nil || *params.Recursive
		force := params.Force == nil || *params.Force
		var err error
		if recursive {
			if _, statErr := os.Lstat(params.Path); statErr != nil {
				err = statErr
			} else {
				err = os.RemoveAll(params.Path)
			}
		} else {
			err = os.Remove(params.Path)
		}
		if err != nil && !(force && errors.Is(err, fs.ErrNotExist)) {
			failed(err)
			return
		}
		files.reply(request.ID, map[string]any{})
	case "fs/copy":
		recursive := params.Recursive != nil && *params.Recursive
		if err := copyLocal(params.SourcePath, params.DestinationPath, recursive); err != nil {
			failed(err)
			return
		}
		files.reply(request.ID, map[string]any{})
	case "fs/watch":
		if params.WatchID == "" {
			files.fail(request.ID, -32600, "Invalid request: missing field `watchId`")
			return
		}
		if _, err := os.Lstat(params.Path); err != nil {
			failed(err)
			return
		}
		stop := make(chan struct{})
		files.watchMu.Lock()
		if previous, ok := files.watches[params.WatchID]; ok {
			close(previous)
		}
		files.watches[params.WatchID] = stop
		files.watchMu.Unlock()
		go files.watch(params.WatchID, filepath.Clean(params.Path), stop)
		files.reply(request.ID, map[string]any{"path": params.Path})
	case "fs/unwatch":
		files.watchMu.Lock()
		if stop, ok := files.watches[params.WatchID]; ok {
			close(stop)
			delete(files.watches, params.WatchID)
		}
		files.watchMu.Unlock()
		files.reply(request.ID, map[string]any{})
	default:
		files.fail(request.ID, -32601, "Method not found: "+request.Method)
	}
}

func copyLocal(source, destination string, recursive bool) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(source, destination, info.Mode().Perm())
	}
	if !recursive {
		return &fs.PathError{Op: "copy", Path: source, Err: syscall.EISDIR}
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(source, path)
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0700)
		case entry.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFile(path, target, info.Mode().Perm())
		}
	})
}

func copyFile(source, destination string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type watchedEntry struct {
	modified time.Time
	size     int64
	mode     fs.FileMode
}

// snapshot describes a watched file, or a directory and its direct entries.
func snapshot(path string) map[string]watchedEntry {
	state := map[string]watchedEntry{}
	info, err := os.Lstat(path)
	if err != nil {
		return state
	}
	state[path] = watchedEntry{info.ModTime(), info.Size(), info.Mode()}
	if !info.IsDir() {
		return state
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return state
	}
	for i, entry := range entries {
		if i >= chatgptLocalWatchEntries {
			break
		}
		if info, err := entry.Info(); err == nil {
			child := filepath.Join(path, entry.Name())
			state[child] = watchedEntry{info.ModTime(), info.Size(), info.Mode()}
		}
	}
	return state
}

// watch reports changed paths as fs/changed notifications, polling once a
// second: modest, and identical for files and directories.
func (files *ChatGPTLocalFiles) watch(id, path string, stop chan struct{}) {
	previous := snapshot(path)
	ticker := time.NewTicker(chatgptLocalWatchPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		current := snapshot(path)
		var changed []string
		for name, entry := range current {
			if before, ok := previous[name]; !ok || before != entry {
				changed = append(changed, name)
			}
		}
		for name := range previous {
			if _, ok := current[name]; !ok {
				changed = append(changed, name)
			}
		}
		previous = current
		if len(changed) > 0 {
			sort.Strings(changed)
			files.send(map[string]any{"method": "fs/changed", "params": map[string]any{"watchId": id, "changedPaths": changed}})
		}
	}
}
