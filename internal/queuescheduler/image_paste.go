package queuescheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fennec/internal/board"
	r "fennec/internal/runtime"
)

var ErrInputUnconfirmed = errors.New("input delivery was not confirmed")

// PasteRequestImages uses the same herdr-core terminal.put path as Desktop's
// hc put, followed by its mode-aware bracketed paste/submit operation. Agent
// CLI flags and the user's system clipboard are never involved.
func PasteRequestImages(ctx context.Context, store board.Store, query func(context.Context, string, any) (json.RawMessage, error), terminal string, request board.Request, prompt string) error {
	if len(board.RequestAttachments(request)) == 0 {
		return nil
	}
	return SendRequest(ctx, store, query, terminal, request, prompt)
}

// SendRequest journals both text and images, so reconnects cannot submit twice.
func SendRequest(ctx context.Context, store board.Store, query func(context.Context, string, any) (json.RawMessage, error), terminal string, request board.Request, prompt string) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("%w: %v", ErrInputUnconfirmed, err)
		}
	}()
	if len(request.Runs) > 0 {
		after := request.Runs[len(request.Runs)-1].InputAfter
		seen := map[string]bool{}
		for _, run := range request.Runs[:len(request.Runs)-1] {
			for _, uid := range run.InputUIDs {
				seen[uid] = true
			}
		}
		// A failed launch/turn does not prove its images reached the agent.
		// An explicit retry must retain those inputs, including legacy runs.
		if len(request.Runs) > 1 {
			previous := request.Runs[len(request.Runs)-2]
			if previous.State != board.NeedsYou && previous.State != board.Resolved {
				after, seen = 0, map[string]bool{}
			}
		}
		messages := []board.Message{}
		for _, m := range request.Messages {
			if len(seen) > 0 && !seen[m.UID] || len(seen) == 0 && m.ID > after {
				messages = append(messages, m)
			}
		}
		request.Messages = messages
	}
	images := board.RequestAttachments(request)
	root := filepath.Join(store.Home, ".fen/board/deliveries")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	marker := filepath.Join(root, request.Session+".json")
	if data, err := os.ReadFile(marker); err == nil {
		if string(data) == "done" {
			return nil
		}
		return fmt.Errorf("image paste outcome needs confirmation")
	}
	paths, err := uploadImages(ctx, store, query, terminal, images)
	if err != nil {
		return err
	}
	// A lost submit reply must not resubmit a request after reconnect/restart.
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.WriteString("pending"); err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		return err
	}
	text := strings.Join(paths, " ") + " \n\n" + prompt
	raw, err := sendWhenSettled(ctx, query, terminal, text)
	if err != nil {
		if r.HerdrRefusalKind(err) != "" {
			// Herdr typed nothing, so a later retry may send it again.
			os.Remove(marker)
		}
		return err
	}
	var receipt struct {
		Started *bool   `json:"started"`
		Turn    *uint64 `json:"turn"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return fmt.Errorf("input was not confirmed as started")
	}
	if receipt.Started == nil || !*receipt.Started {
		// Without the turn the paste was typed into there is nothing to check.
		if receipt.Turn == nil {
			if receipt.Started != nil {
				return fmt.Errorf("input was not confirmed as started")
			}
		} else if err = confirmSubmitted(ctx, query, terminal, *receipt.Turn); err != nil {
			return err
		}
	}
	return os.WriteFile(marker, []byte("done"), 0600)
}

// sendWhenSettled pastes the request once the agent has finished starting.
// herdr-core refuses input with "unavailable" ("still starting: its output has
// not settled", nothing typed) while a fresh TUI keeps repainting. Codex 0.172
// animates its empty input box for 10-20 seconds after launch (FEN-153), so a
// single attempt fails every new Codex thread.
func sendWhenSettled(ctx context.Context, query func(context.Context, string, any) (json.RawMessage, error), terminal, text string) (json.RawMessage, error) {
	deadline := time.Now().Add(settleWindow)
	for {
		raw, err := query(ctx, "terminal.send", map[string]any{"terminal": terminal, "text": text, "force": false})
		if err == nil || r.HerdrRefusalKind(err) != "unavailable" || time.Now().After(deadline) {
			return raw, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(settlePoll):
		}
	}
}

// Confirmation timing; tests shorten it.
var (
	settleWindow  = 45 * time.Second
	settlePoll    = 2 * time.Second
	confirmWindow = 45 * time.Second
	confirmPoll   = time.Second
	maxResubmits  = 3
)

// confirmSubmitted proves a pasted request became an agent turn. An agent can
// swallow the Enter that follows a paste (Claude Code does while it is still
// starting or ingesting a long paste), leaving the prompt unsent in its input
// box. herdr-core then reports started=false, or null while a fresh agent has
// not reported any status yet. Neither is a started turn.
//
// While the agent sits idle on the turn the paste was typed into, press its
// submit key again; Enter on an empty input box does nothing. Never press it
// while the agent is working or showing anything else (a dialog would take it
// as an answer). An agent that never reports status cannot be checked and
// keeps the previous behaviour.
func confirmSubmitted(ctx context.Context, query func(context.Context, string, any) (json.RawMessage, error), terminal string, turn uint64) error {
	deadline := time.Now().Add(confirmWindow)
	presses, reported := 0, false
	for {
		raw, err := query(ctx, "terminal.get", map[string]any{"terminal": terminal})
		if err != nil {
			return err
		}
		var t struct {
			State  string  `json:"state"`
			Turn   *uint64 `json:"turn"`
			Status *struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if json.Unmarshal(raw, &t) != nil || t.Turn == nil {
			return fmt.Errorf("invalid terminal state")
		}
		if t.State != "live" {
			return fmt.Errorf("the session ended before the request started")
		}
		word := ""
		if t.Status != nil {
			reported, word = true, t.Status.State
		}
		if *t.Turn > turn || word == "working" {
			return nil
		}
		if time.Now().After(deadline) {
			if !reported {
				return nil
			}
			return fmt.Errorf("input was not confirmed as started")
		}
		if (word == "idle" || word == "done") && presses < maxResubmits {
			presses++
			if _, err = query(ctx, "terminal.keys", map[string]any{"terminal": terminal, "keys": []string{"enter"}}); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(confirmPoll):
		}
	}
}

// This is the Unix path quoting used by the existing terminal paste_paths.
func quotePastedPath(path string) string {
	if strings.IndexFunc(path, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+=:@%~,", r))
	}) < 0 {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

// uploadImages puts each image into the terminal's directory and returns
// the quoted paths to paste in front of the text.
func uploadImages(ctx context.Context, store board.Store, query func(context.Context, string, any) (json.RawMessage, error), terminal string, images []board.Attachment) ([]string, error) {
	paths := []string{}
	for _, image := range images {
		if !store.HasAttachment(image) {
			return nil, fmt.Errorf("image has not synchronized")
		}
		data, err := os.ReadFile(store.AttachmentPath(image))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != image.ID {
			return nil, fmt.Errorf("image integrity check failed")
		}
		raw, err := query(ctx, "terminal.put", map[string]any{"terminal": terminal, "name": image.ID[:12] + "-" + image.Name, "data": data})
		if err != nil {
			return nil, err
		}
		var put struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		}
		if json.Unmarshal(raw, &put) != nil || put.Bytes != image.Size || !filepath.IsAbs(put.Path) || strings.ContainsAny(put.Path, "\x00\x1b\r\n") {
			return nil, fmt.Errorf("image upload was not confirmed")
		}
		paths = append(paths, quotePastedPath(put.Path))
	}
	return paths, nil
}
