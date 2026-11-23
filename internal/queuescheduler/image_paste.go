package queuescheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fennec/internal/board"
)

// PasteRequestImages uses the same herdr-core terminal.put path as Desktop's
// hc put, followed by its mode-aware bracketed paste/submit operation. Agent
// CLI flags and the user's system clipboard are never involved.
func PasteRequestImages(ctx context.Context, store board.Store, query func(context.Context, string, any) (json.RawMessage, error), terminal string, request board.Request, prompt string) error {
	images := board.RequestAttachments(request)
	if len(images) == 0 {
		return nil
	}
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
	paths := []string{}
	for _, image := range images {
		if !store.HasAttachment(image) {
			return fmt.Errorf("image has not synchronized")
		}
		data, err := os.ReadFile(store.AttachmentPath(image))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != image.ID {
			return fmt.Errorf("image integrity check failed")
		}
		raw, err := query(ctx, "terminal.put", map[string]any{"terminal": terminal, "name": image.ID[:12] + "-" + image.Name, "data": data})
		if err != nil {
			return err
		}
		var put struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		}
		if json.Unmarshal(raw, &put) != nil || put.Bytes != image.Size || !filepath.IsAbs(put.Path) || strings.ContainsAny(put.Path, "\x00\x1b\r\n") {
			return fmt.Errorf("image upload was not confirmed")
		}
		paths = append(paths, quotePastedPath(put.Path))
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
	if _, err = query(ctx, "terminal.send", map[string]any{"terminal": terminal, "text": text, "force": false}); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("done"), 0600)
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
