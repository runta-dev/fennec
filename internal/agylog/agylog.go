// Package agylog finds Antigravity CLI's (agy's) session log for a folder.
//
// agy keeps each conversation's steps in
// <home>/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/transcript_full.jsonl
// and lists its conversations in conversation_summaries.db beside brain/,
// one row each with workspace_uris (a JSON list of file:// URIs, the folder
// it was started in) and last_modified_time. Fennec starts every session in
// a folder of its own, so the newest conversation of that folder is the
// session's. A conversation is listed once its first message is in.
package agylog

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	_ "modernc.org/sqlite"
)

var conversationID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// Dir is agy's data folder under home.
func Dir(home string) string { return filepath.Join(home, ".gemini", "antigravity-cli") }

// Transcript returns the log of the newest conversation agy started in cwd,
// or "" when it has none yet.
func Transcript(home, cwd string) (string, error) {
	if !filepath.IsAbs(cwd) {
		return "", nil
	}
	cwd = filepath.Clean(cwd)
	index := filepath.Join(Dir(home), "conversation_summaries.db")
	if _, err := os.Stat(index); err != nil {
		return "", nil
	}
	u := url.URL{Scheme: "file", Path: index}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "busy_timeout(2000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", err
	}
	defer db.Close()
	rows, err := db.Query("SELECT conversation_id, workspace_uris, last_modified_time FROM conversation_summaries")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	best, bestTime := "", time.Time{}
	for rows.Next() {
		var id string
		var uris sql.NullString
		var last any
		if rows.Scan(&id, &uris, &last) != nil || !conversationID.MatchString(id) {
			continue
		}
		var list []string
		_ = json.Unmarshal([]byte(uris.String), &list)
		matches := false
		for _, raw := range list {
			if parsed, err := url.Parse(raw); err == nil && parsed.Scheme == "file" && (parsed.Host == "" || parsed.Host == "localhost") && filepath.Clean(parsed.Path) == cwd {
				matches = true
				break
			}
		}
		if when := modified(last); matches && (best == "" || when.After(bestTime)) {
			best, bestTime = id, when
		}
	}
	if err := rows.Err(); err != nil || best == "" {
		return "", err
	}
	path := filepath.Join(Dir(home), "brain", best, ".system_generated", "logs", "transcript_full.jsonl")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", nil
	}
	return path, nil
}

// modified reads last_modified_time as the driver gives it: a time, or the
// text agy writes ("2026-10-25 09:48:56.074501+00:00").
func modified(v any) time.Time {
	switch v := v.(type) {
	case time.Time:
		return v
	case []byte:
		return modified(string(v))
	case string:
		for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano, "2006-01-02 15:04:05.999999999"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}
