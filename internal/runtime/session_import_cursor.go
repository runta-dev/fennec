package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func openCursorDB(file string, readonly bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: file}
	q := u.Query()
	q.Set("_pragma", "busy_timeout(5000)")
	if readonly {
		q.Set("mode", "ro")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func cursorMetadata(ctx context.Context, db *sql.DB, id string) ([]byte, error) {
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = '0'").Scan(&raw); err != nil {
		return nil, fmt.Errorf("cannot read Cursor conversation metadata: %w", err)
	}
	data, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid Cursor conversation metadata")
	}
	var meta struct {
		AgentID string `json:"agentId"`
	}
	if json.Unmarshal(data, &meta) != nil || meta.AgentID != id {
		return nil, fmt.Errorf("Cursor conversation ID does not match its database")
	}
	return data, nil
}

// VACUUM INTO reads a consistent SQLite snapshot, including committed WAL
// pages, without modifying the user's database. Never copy a live DB and its
// WAL independently: their page generations can disagree.
func snapshotCursorSession(file, id string) ([]byte, error) {
	for _, name := range []string{file, file + "-wal"} {
		info, err := os.Lstat(name)
		if os.IsNotExist(err) && name != file {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > sessionImportLimit {
			return nil, fmt.Errorf("invalid or oversized Cursor conversation database")
		}
	}
	db, err := openCursorDB(file, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("", "fennec-cursor-session-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	snapshot := filepath.Join(dir, "store.db")
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", snapshot); err != nil {
		return nil, fmt.Errorf("cannot snapshot Cursor conversation: %w", err)
	}
	if err = os.Chmod(snapshot, 0600); err != nil {
		return nil, err
	}
	if err = validateCursorSnapshot(snapshot, id); err != nil {
		return nil, err
	}
	compact, err := openCursorDB(snapshot, false)
	if err != nil {
		return nil, err
	}
	err = pruneCursorBlobs(ctx, compact, id)
	compact.Close()
	if err != nil {
		return nil, err
	}
	return readSessionFile(snapshot)
}

func validateCursorSnapshot(file, id string) error {
	db, err := openCursorDB(file, true)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = cursorMetadata(ctx, db, id); err != nil {
		return err
	}
	var check string
	if err = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return fmt.Errorf("invalid Cursor conversation database")
	}
	return nil
}

// Cursor's v1 SQLite store holds immutable SHA-256-addressed protobuf/string
// blobs. The latest root and all metadata are graph roots. Search every byte
// for native 32-byte references (including nested protobufs), and every JSON
// string for hex/base64 references. False positives retain extra data. This
// keeps summary archives, retained turns, images, tools and subagent state;
// only unreachable old checkpoints are removed. Unknown schemas or mutable
// IDs travel whole. The source database is never garbage-collected.
func pruneCursorBlobs(ctx context.Context, db *sql.DB, id string) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return nil
	}
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		tables[name] = true
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(tables) != 2 || !tables["meta"] || !tables["blobs"] {
		return nil
	}
	metadata, err := cursorMetadata(ctx, db, id)
	if err != nil {
		return err
	}
	var meta struct {
		Root string `json:"latestRootBlobId"`
	}
	if json.Unmarshal(metadata, &meta) != nil || len(meta.Root) != 64 {
		return nil
	}
	root, err := hex.DecodeString(meta.Root)
	if err != nil {
		return nil
	}
	blobs := map[[32]byte][]byte{}
	rows, err = db.QueryContext(ctx, "SELECT id,data FROM blobs")
	if err != nil {
		return err
	}
	total := 0
	known := true
	for rows.Next() {
		var key string
		var data []byte
		if err = rows.Scan(&key, &data); err != nil {
			break
		}
		total += len(data)
		if total > sessionImportLimit {
			err = fmt.Errorf("Cursor conversation is too large to move to the cloud computer")
			break
		}
		raw, e := hex.DecodeString(key)
		sum := sha256.Sum256(data)
		if e != nil || len(raw) != 32 || key != hex.EncodeToString(sum[:]) {
			known = false
			break
		}
		blobs[[32]byte(raw)] = data
	}
	scanErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}
	if !known {
		return nil
	}
	rootKey := [32]byte(root)
	if _, ok := blobs[rootKey]; !ok {
		return nil
	}
	seen := map[[32]byte]bool{}
	queue := [][32]byte{}
	add := func(key [32]byte) {
		if _, ok := blobs[key]; ok && !seen[key] {
			seen[key] = true
			queue = append(queue, key)
		}
	}
	visit := func(data []byte) {
		for i := 0; i+32 <= len(data); i++ {
			add([32]byte(data[i : i+32]))
		}
		// Text references occur in JSON, so scan string tokens as well as binary
		// IDs. Non-JSON protobuf strings still expose embedded quoted tokens.
		for _, token := range strings.Split(string(data), "\"") {
			if len(token) == 64 {
				if raw, e := hex.DecodeString(token); e == nil {
					add([32]byte(raw))
				}
			}
			if len(token) == 44 {
				if raw, e := base64.StdEncoding.DecodeString(token); e == nil && len(raw) == 32 {
					add([32]byte(raw))
				}
			}
		}
	}
	add(rootKey)
	rows, err = db.QueryContext(ctx, "SELECT value FROM meta")
	if err != nil {
		return err
	}
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			break
		}
		visit([]byte(value))
		if raw, e := hex.DecodeString(value); e == nil {
			visit(raw)
		}
	}
	scanErr = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		visit(blobs[key])
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	if len(seen) == len(blobs) {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key := range blobs {
		if !seen[key] {
			if _, err = tx.ExecContext(ctx, "DELETE FROM blobs WHERE id = ?", hex.EncodeToString(key[:])); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "VACUUM")
	return err
}
