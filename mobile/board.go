package fennecmobile

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"fennec/internal/board"
	"fennec/internal/pasteimage"
	"fennec/internal/terminalstream"

	"golang.org/x/crypto/ssh"
)

// boardPath is what a board API path may contain: /v1/ and then ids,
// numbers and query characters. It goes into a shell command, so nothing
// that a shell would read specially is let through.
var boardPath = regexp.MustCompile(`^/v1/[A-Za-z0-9/_.=&%-]*$`)

// boardScript runs one board API call on the cloud computer, the way the
// desktop app does there (`fennec-terminal board-call`); the body, if any,
// goes on standard input.
func boardScript(method, path string) (string, error) {
	switch method {
	case "GET", "POST", "PATCH", "DELETE":
	default:
		return "", fmt.Errorf("board: unsupported method %q", method)
	}
	if !boardPath.MatchString(path) || strings.Contains(path, "..") {
		return "", fmt.Errorf("board: invalid path %q", path)
	}
	return "exec \"$HOME/.local/fennec/bin/fennec-terminal\" board-call " + method + " '" + path + "'\n", nil
}

// Board makes one call to the queue board on the cloud computer and returns
// its JSON reply: projects, requests, threads, knowledge (internal/board
// http.go has the API). Reads retry over a dropped link; changes never
// repeat a call whose outcome is unknown.
func (s *Sessions) Board(method, path, body string) (out0 string, err error) {
	defer func() { err = boardError(classify(err)) }()
	script := func(string) (string, error) { return boardScript(method, path) }
	if method == "GET" {
		raw, _, err := s.runRead(script)
		return string(raw), err
	}
	var input []byte
	if body != "" {
		input = []byte(body)
	}
	raw, _, err := s.runInput(script, input)
	return string(raw), err
}

// boardError drops the command's "board-call: 409" prefix, leaving the
// board's own message for the app to show.
func boardError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.TrimPrefix(err.Error(), "board-call: ")
	if len(message) > 4 && message[3] == ' ' && strings.Trim(message[:3], "0123456789") == "" {
		message = message[4:]
	}
	if message == err.Error() {
		return err
	}
	return errors.New(message)
}

// UploadImage compresses a pasted image to WebP for a request or a
// reply and returns its board.Attachment as JSON, to send along with the
// message. Images are content-addressed, so uploading one twice is harmless.
func (s *Sessions) UploadImage(data []byte) (out0 string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, _, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	transport, err := s.resident(ctx, client)
	if err != nil {
		return "", err
	}
	if transport.Supports(terminalstream.CapabilityBoardWebP) {
		data, err = pasteimage.Compress(data)
		if err != nil {
			return "", err
		}
	}
	attachment, err := board.NewAttachment(data)
	if err != nil {
		return "", errors.New("image")
	}
	wireAttachment := attachment
	wireAttachment.Thumbnail = ""
	for offset := int64(0); offset < attachment.Size; offset += board.AttachmentChunkBytes {
		end := min(offset+board.AttachmentChunkBytes, attachment.Size)
		chunk, err := json.Marshal(board.AttachmentChunk{Attachment: wireAttachment, Offset: offset, Data: data[offset:end]})
		if err != nil {
			return "", err
		}
		if _, err = s.Board("POST", "/v1/attachments/"+attachment.ID, string(chunk)); err != nil {
			return "", err
		}
	}
	out, err := json.Marshal(attachment)
	return string(out), err
}

// ImageData reads a stored image in full (for the full-screen viewer).
func (s *Sessions) ImageData(id string, size int64) ([]byte, error) {
	if size <= 0 || size > board.MaxImageBytes {
		return nil, errors.New("image")
	}
	data := make([]byte, 0, size)
	for int64(len(data)) < size {
		raw, err := s.Board("GET", fmt.Sprintf("/v1/attachments/%s/%d", id, len(data)), "")
		if err != nil {
			return nil, err
		}
		var chunk struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
			return nil, err
		}
		part, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil || len(part) == 0 {
			return nil, errors.New("image")
		}
		data = append(data, part...)
	}
	return data, nil
}

// BoardEvents receives what `fennec-terminal board-watch` pushes (FEN-60):
// one JSON event per call (internal/board watch.go has the kinds). Event
// may run on any goroutine; Closed is called once when the stream ends,
// with "" when it was closed on purpose.
type BoardEvents interface {
	Event(json string)
	Closed(message string)
}

// BoardWatch is an open board-watch stream on the cloud computer.
type BoardWatch struct {
	session *ssh.Session
	stdin   io.WriteCloser
	mu      sync.Mutex
	once    sync.Once
	closed  bool
}

// WatchBoard opens a board-watch stream: the board pushes changes to the
// open queue (and thread) instead of the app reading the board again and
// again. Say what to watch with Follow. A cloud computer whose
// fennec-terminal is older fails at once (Closed with a message); keep
// polling then.
func (s *Sessions) WatchBoard(out BoardEvents) (out0 *BoardWatch, err error) {
	defer func() { err = classify(err) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, _, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		s.drop(client)
		if client, _, err = s.connect(ctx); err != nil {
			return nil, err
		}
		if session, err = client.NewSession(); err != nil {
			return nil, err
		}
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	var stderr strings.Builder
	session.Stderr = &stderr
	if err := session.Start(`exec "$HOME/.local/fennec/bin/fennec-terminal" board-watch`); err != nil {
		session.Close()
		return nil, err
	}
	watch := &BoardWatch{session: session, stdin: stdin}
	go func() {
		reader := bufio.NewReaderSize(stdout, 64<<10)
		for {
			line, err := reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				out.Event(string(bytes.TrimSpace(line)))
			}
			if err != nil {
				break
			}
		}
		waitErr := session.Wait()
		watch.mu.Lock()
		closed := watch.closed
		watch.mu.Unlock()
		message := ""
		if !closed {
			if text := strings.TrimSpace(stderr.String()); text != "" {
				message = text
			} else if waitErr != nil {
				message = classify(waitErr).Error()
			} else {
				message = "board watch ended"
			}
		}
		out.Closed(message)
	}()
	return watch, nil
}

// Follow says what to watch: a queue, and a thread in it (0 for none).
func (w *BoardWatch) Follow(project string, thread int) error {
	line, err := json.Marshal(map[string]any{"project": project, "thread": thread})
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("board watch closed")
	}
	_, err = w.stdin.Write(append(line, '\n'))
	return err
}

// Close ends the stream.
func (w *BoardWatch) Close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		w.stdin.Close()
		w.session.Close()
	})
}
