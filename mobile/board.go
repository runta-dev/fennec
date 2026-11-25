package fennecmobile

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"fennec/internal/board"
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

// UploadImage stores a PNG or JPEG image on the board for a request or a
// reply and returns its board.Attachment as JSON, to send along with the
// message. Images are content-addressed, so uploading one twice is harmless.
func (s *Sessions) UploadImage(data []byte) (out0 string, err error) {
	attachment, err := board.NewAttachment(data)
	if err != nil {
		return "", errors.New("image")
	}
	for offset := int64(0); offset < attachment.Size; offset += board.AttachmentChunkBytes {
		end := min(offset+board.AttachmentChunkBytes, attachment.Size)
		chunk, err := json.Marshal(board.AttachmentChunk{Attachment: attachment, Offset: offset, Data: data[offset:end]})
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
