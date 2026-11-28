package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fennec/internal/board"
)

// Ref is how to answer one feedback message filed in a request.
type Ref struct {
	Channel string `json:"channel"`
	// Message is the board message's UID.
	Message  string    `json:"message"`
	Author   string    `json:"author"`
	ReplyRef string    `json:"reply_ref,omitempty"`
	Received time.Time `json:"received"`
	// Sent are the replies that reached the author, so a retried reply
	// isn't sent twice.
	Sent []Sent `json:"sent,omitempty"`
}

// Sent is one reply that went out.
type Sent struct {
	Digest string    `json:"digest"`
	At     time.Time `json:"at"`
}

type refFile struct {
	Feedback []Ref `json:"feedback"`
}

func refPath(home, project string, n int) string {
	return filepath.Join(Dir(home), "refs", project, strconv.Itoa(n)+".json")
}

func loadRefs(home, project string, n int) (refFile, error) {
	var f refFile
	data, err := os.ReadFile(refPath(home, project, n))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

func storeRefs(home, project string, n int, f refFile) error {
	path := refPath(home, project, n)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveRef(home, project string, n int, ref Ref) error {
	f, err := loadRefs(home, project, n)
	if err != nil {
		return err
	}
	f.Feedback = append(f.Feedback, ref)
	return storeRefs(home, project, n, f)
}

// Result is what Reply did for one feedback author.
type Result struct {
	Request int    `json:"request"`
	Channel string `json:"channel"`
	Author  string `json:"author"`
	// Status is sent, already (this text went out before), unreachable (no
	// reply ref) or failed.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ReplyForRun is `channel reply` for the run working on request n: it sends
// text through Reply and, once every reachable author has it, records it in
// the thread as the run's feedback reply (board.FeedbackReply, FEN-230), which
// a Feedback request needs before it may go to review.
func ReplyForRun(ctx context.Context, home, project string, n int, agent, session, text string) ([]Result, error) {
	s := board.Store{Home: home, Role: "cloud"}
	r, err := s.Request(project, n)
	if err != nil {
		return nil, fmt.Errorf("request %s %d: %w", project, n, err)
	}
	if !board.OwnsRun(r, session, agent) {
		return nil, fmt.Errorf("this run no longer owns %s %d", project, n)
	}
	results, err := Reply(ctx, home, project, n, text)
	if err != nil {
		return results, err
	}
	reached := 0
	for _, res := range results {
		switch res.Status {
		case "failed":
			return results, errors.New("some replies failed; run the same command again to retry only those")
		case "sent", "already":
			reached++
		}
	}
	if _, err = s.RecordFeedbackReply(project, n, session, agent, strings.TrimSpace(text), reached); err != nil {
		return results, fmt.Errorf("replies sent, but recording them in the thread failed: %w", err)
	}
	return results, nil
}

// Reply sends text to every feedback author of request n and of the requests
// merged into it (`queue merge`), through each one's channel. Authors who
// already got this exact text are skipped, so rerunning after a failure only
// retries the failures.
func Reply(ctx context.Context, home, project string, n int, text string) ([]Result, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("channel reply: empty text")
	}
	cfg, err := LoadConfig(home)
	if err != nil {
		return nil, err
	}
	numbers, err := mergedInto(board.Store{Home: home, Role: "cloud"}, home, project, n)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(text))
	digest := hex.EncodeToString(sum[:])
	var results []Result
	for _, m := range numbers {
		f, err := loadRefs(home, project, m)
		if err != nil {
			return results, err
		}
		changed := false
		for i := range f.Feedback {
			ref := &f.Feedback[i]
			res := Result{Request: m, Channel: ref.Channel, Author: ref.Author}
			switch {
			case ref.ReplyRef == "":
				res.Status = "unreachable"
			case sentBefore(ref.Sent, digest):
				res.Status = "already"
			default:
				err := send(ctx, cfg, ref, text)
				if err != nil {
					res.Status, res.Error = "failed", err.Error()
				} else {
					res.Status = "sent"
					ref.Sent = append(ref.Sent, Sent{Digest: digest, At: time.Now().UTC()})
					changed = true
				}
			}
			results = append(results, res)
		}
		if changed {
			if err = storeRefs(home, project, m, f); err != nil {
				return results, err
			}
		}
	}
	return results, nil
}

func send(ctx context.Context, cfg Config, ref *Ref, text string) error {
	spec, ok := cfg.Spec(ref.Channel)
	if !ok {
		return fmt.Errorf("channel %s is no longer configured", ref.Channel)
	}
	ch, err := openChannel(cfg, spec)
	if err != nil {
		return err
	}
	return ch.Reply(ctx, ref.ReplyRef, text)
}

// openChannel builds the channel a reply goes through; tests replace it.
var openChannel = Config.Open

func sentBefore(sent []Sent, digest string) bool {
	for _, s := range sent {
		if s.Digest == digest {
			return true
		}
	}
	return false
}

// mergedInto is n plus every request that has feedback refs and was merged,
// directly or through others, into n.
func mergedInto(s board.Store, home, project string, n int) ([]int, error) {
	if _, err := s.Request(project, n); err != nil {
		return nil, fmt.Errorf("request %d: %w", n, err)
	}
	entries, err := os.ReadDir(filepath.Join(Dir(home), "refs", project))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	parent := map[int]int{}
	for _, e := range entries {
		m, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		parent[m] = 0
		if r, err := s.Request(project, m); err == nil {
			parent[m] = r.DuplicateOf()
		}
	}
	out := []int{n}
	for m := range parent {
		if m == n {
			continue
		}
		for at, hops := parent[m], 0; at != 0 && hops < 32; hops++ {
			if at == n {
				out = append(out, m)
				break
			}
			next, ok := parent[at]
			if !ok {
				if r, err := s.Request(project, at); err == nil {
					next = r.DuplicateOf()
				}
			}
			at = next
		}
	}
	return out, nil
}
