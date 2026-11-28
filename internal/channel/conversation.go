package channel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"fennec/internal/board"
)

// Conversations (FEN-226): a channel that keeps talking to the author in one
// place, such as a Discord thread, maps that place to the request it opened,
// in ~/.fen/channels/conversations/<channel id>.json. Later messages there
// are follow-ups of that request.

var conversationMu sync.Mutex

func conversationPath(home, channel string) string {
	return filepath.Join(Dir(home), "conversations", channel+".json")
}

func loadConversations(home, channel string) (map[string]int, error) {
	m := map[string]int{}
	data, err := os.ReadFile(conversationPath(home, channel))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	return m, json.Unmarshal(data, &m)
}

func saveConversation(home, channel, conversation string, n int) error {
	conversationMu.Lock()
	defer conversationMu.Unlock()
	m, err := loadConversations(home, channel)
	if err != nil {
		return err
	}
	m[conversation] = n
	path := conversationPath(home, channel)
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FollowUp adds fb to the request its conversation opened, following merges
// (`queue merge`) to the request that is still open. Like a person's reply it
// reaches the running agent or queues the request again. ok is false when the
// conversation opened no request.
func FollowUp(home string, spec ChannelSpec, fb Feedback) (req board.Request, ok bool, err error) {
	if fb.Conversation == "" {
		return req, false, nil
	}
	conversationMu.Lock()
	m, err := loadConversations(home, spec.ID)
	conversationMu.Unlock()
	n, ok := m[fb.Conversation]
	if err != nil || !ok {
		return req, false, err
	}
	s := board.Store{Home: home, Role: "cloud"}
	if _, on, err := channelTags(s, spec); err != nil || !on {
		if err == nil {
			err = ErrOff
		}
		return req, true, err
	}
	for range 20 {
		r, err := s.Request(spec.Project, n)
		if err != nil {
			return req, true, fmt.Errorf("request %d: %w", n, err)
		}
		d := r.DuplicateOf()
		if d == 0 {
			break
		}
		n = d
	}
	req, err = s.FeedbackFollowUp(spec.Project, n, board.External{Name: fb.Author, Channel: fb.Kind}, FollowUpBody(spec, fb))
	return req, true, err
}
