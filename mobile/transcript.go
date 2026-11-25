package fennecmobile

import (
	"errors"
	"strconv"
	"strings"

	"fennec/internal/chatlog"
)

// The chat view reads the agent's own session log; Herdr only supplies the
// live state. internal/chatlog finds and reads the log.
//
// The cloud computer's `fennec-terminal transcript` does the whole read in
// one round trip and sends parsed items (THI-201). A cloud computer whose
// fennec-terminal predates it is read step by step from here, as before.

// oldTerminal is the error an older fennec-terminal gives for a command it
// doesn't know with these arguments.
const oldTerminal = "expected launch or attach"

func (s *Sessions) exec(command string) ([]byte, error) {
	output, _, err := s.run(func(string) (string, error) { return command, nil })
	return output, err
}

// read runs a command that only reads, so it may repeat after a dropped link.
func (s *Sessions) read(command string) ([]byte, error) {
	output, _, err := s.runRead(func(string) (string, error) { return command, nil })
	return output, err
}

// stepwise is the reader for a cloud computer without `fennec-terminal transcript`.
func (s *Sessions) stepwise() *chatlog.Reader {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log == nil {
		s.log = &chatlog.Reader{Run: s.read}
	}
	return s.log
}

// Transcript returns {"items":[...],"offset":n,"reset":bool,"path":...}.
// Pass offset -1 for the latest part of the log, then the returned offset to
// get only new lines. reset means start over: drop what was shown.
func (s *Sessions) Transcript(address, sessionID string, offset int64) (out0 string, err error) {
	defer func() { err = classify(err) }()
	if !addressPattern.MatchString(address) || !sessionPattern.MatchString(sessionID) {
		return "", errors.New("invalid terminal")
	}
	s.mu.Lock()
	old := s.oldTerminal
	s.mu.Unlock()
	if !old {
		raw, err := s.read(`exec "$HOME/.local/fennec/bin/fennec-terminal" transcript ` + address + " " + sessionID + " " + strconv.FormatInt(offset, 10) + " --lazy-images")
		if err == nil {
			return strings.TrimSpace(string(raw)), nil
		}
		if !strings.Contains(err.Error(), oldTerminal) {
			return "", err
		}
		s.mu.Lock()
		s.oldTerminal = true
		s.mu.Unlock()
	}
	return s.stepwise().Read(address, sessionID, offset)
}

// TranscriptImage returns the data: URL behind an image reference that
// Transcript sent in place of the image (internal/chatlog ImageRef). The
// chat asks for each image only when it shows it.
func (s *Sessions) TranscriptImage(ref string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	if _, _, _, err := chatlog.ParseImageRef(ref); err != nil {
		return "", err
	}
	raw, err := s.read(`exec "$HOME/.local/fennec/bin/fennec-terminal" transcript-image ` + chatlog.ShellQuote(ref))
	return strings.TrimSpace(string(raw)), err
}
