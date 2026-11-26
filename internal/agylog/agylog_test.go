package agylog

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestTranscriptIsNewestConversationOfTheFolder(t *testing.T) {
	home := t.TempDir()
	dir := Dir(home)
	if path, err := Transcript(home, "/work/a"); path != "" || err != nil {
		t.Fatal("found a log without agy's index", path, err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, title TEXT, preview TEXT, workspace_uris TEXT, last_modified_time TEXT)`); err != nil {
		t.Fatal(err)
	}
	rows := [][3]string{
		{"11111111-aaaa-4bbb-8ccc-000000000001", `["file:///work/a"]`, "2026-11-25 09:00:00.000000+00:00"},
		{"11111111-aaaa-4bbb-8ccc-000000000002", `["file:///work/a"]`, "2026-11-25 09:30:00.000000+00:00"},
		{"11111111-aaaa-4bbb-8ccc-000000000003", `["file:///work/b"]`, "2026-11-25 10:00:00.000000+00:00"},
		{"../escape", `["file:///work/a"]`, "2026-11-25 11:00:00.000000+00:00"},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO conversation_summaries VALUES (?, '', '', ?, ?)`, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(dir, "brain", r[0], ".system_generated", "logs")
		if r[0] != "../escape" {
			os.MkdirAll(log, 0700)
			os.WriteFile(filepath.Join(log, "transcript_full.jsonl"), []byte("{}\n"), 0600)
		}
	}
	path, err := Transcript(home, "/work/a/")
	want := filepath.Join(dir, "brain", rows[1][0], ".system_generated", "logs", "transcript_full.jsonl")
	if err != nil || path != want {
		t.Fatalf("got %q %v, want %q", path, err, want)
	}
	db.Exec(`UPDATE conversation_summaries SET title = ' Fix login ' WHERE conversation_id = ?`, rows[1][0])
	db.Exec(`UPDATE conversation_summaries SET title = 'Older' WHERE conversation_id = ?`, rows[0][0])
	if title, err := Title(home, "/work/a"); title != "Fix login" || err != nil {
		t.Fatalf("title %q %v", title, err)
	}
	if title, _ := Title(home, "/work/c"); title != "" {
		t.Fatal("another folder's title", title)
	}
	if path, _ := Transcript(home, "/work/c"); path != "" {
		t.Fatal("another folder's conversation", path)
	}
	if path, _ := Transcript(home, "relative"); path != "" {
		t.Fatal("relative folder accepted", path)
	}
}
