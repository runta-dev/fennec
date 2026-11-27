package queuescheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fennec/internal/board"
	r "fennec/internal/runtime"
)

// steer types what the person replied in a thread while its agent is still
// working straight into the agent's live session (FEN-169). Claude Code and
// Codex take input typed during a turn and act on it next; before, the reply
// waited until the agent ended its whole turn with `queue reply`, which could
// be most of an hour, and looked like it was never sent.
//
// The messages become input of the current run, so its one reply answers
// them. A journal per message set keeps a lost receipt from typing twice.
func (s Scheduler) steer(ctx context.Context, p board.Project, request board.Request, terminal string) error {
	querier, ok := s.Executor.(Querier)
	if !ok || terminal == "" {
		return nil
	}
	messages := request.PendingFollowUps()
	if len(messages) == 0 || !s.Store.AttachmentsReady(request) {
		return nil
	}
	uids := make([]string, 0, len(messages))
	for _, m := range messages {
		uids = append(uids, m.UID)
	}
	root := filepath.Join(s.Store.Home, ".fen/board/deliveries")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	marker := filepath.Join(root, request.Session+"-steer-"+uids[len(uids)-1]+".json")
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		// Typed before; only recording it failed or was interrupted.
		_, err = s.Store.Steer(p.ID, request.Number, request.Session, uids)
		return err
	}
	if err != nil {
		return err
	}
	_, err = file.WriteString("pending")
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		os.Remove(marker)
		return err
	}
	attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	images := board.RequestAttachments(board.Request{Messages: messages})
	paths, err := uploadImages(attempt, s.Store, querier.Query, terminal, images)
	if err != nil {
		os.Remove(marker)
		return err
	}
	text := board.SteerPrompt(p, request, messages, s.Command)
	if len(paths) > 0 {
		text = strings.Join(paths, " ") + " \n\n" + text
	}
	if _, err = querier.Query(attempt, "terminal.send", map[string]any{"terminal": terminal, "text": text, "force": false}); err != nil {
		if r.HerdrRefusalKind(err) != "" {
			// Nothing was typed (the agent is showing a dialog, say); try
			// again on a later pass.
			os.Remove(marker)
			return nil
		}
		return err
	}
	os.WriteFile(marker, []byte("done"), 0600)
	_, err = s.Store.Steer(p.ID, request.Number, request.Session, uids)
	return err
}
