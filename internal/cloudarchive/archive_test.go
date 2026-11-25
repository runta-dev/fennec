package cloudarchive

import (
	"bytes"
	"context"
	"errors"
	"fennec/internal/board"
	cloudruntime "fennec/internal/runtime"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	conversationID = "0f1e2d3c-4b5a-4968-8776-a5b4c3d2e1f0"
	earlierID      = "11111111-2222-4333-8444-555555555555"
)

var (
	olderTime  = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	latestTime = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
)

func write(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// source builds a cloud computer home at <tmp>/root with one queue, two
// requests (one with an image), a knowledge entry and one Claude session.
func source(t *testing.T) (string, string) {
	home := filepath.Join(t.TempDir(), "root")
	store := board.Store{Home: home, Role: "cloud"}
	project, err := store.CreateProject(board.Project{Name: "Fennec", Org: "runta"})
	if err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	png.Encode(&picture, image.NewGray(image.Rect(0, 0, 4, 3)))
	attachment, err := store.SaveAttachment(picture.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRequestWithAttachments(project.ID, "shiqimei", "First", []string{"bug"}, "body", []board.Attachment{attachment}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRequest(project.ID, "shiqimei", "Second", "feature", "more"); err != nil {
		t.Fatal(err)
	}
	knowledge := store.KnowledgePath(project.ID)
	write(t, filepath.Join(knowledge, "architecture", "same.md"), "same\n")
	write(t, filepath.Join(knowledge, "architecture", "differs.md"), "from the old computer\n")
	write(t, filepath.Join(knowledge, "runbooks", "new.md"), "new\n")

	// The old computer routes agents through Magpie: Claude keeps conversations
	// in ~/.claude.
	write(t, filepath.Join(home, ".local/fennec/agent-mode.json"), `{"native":false}`)
	workdir := filepath.Join(home, ".fen", "brave-otter")
	write(t, filepath.Join(workdir, "AGENTS.md"), "agents\n")
	write(t, filepath.Join(workdir, "report", "notes.md"), "notes\n")
	write(t, filepath.Join(workdir, "report", "node_modules", "left.js"), "x")
	write(t, filepath.Join(workdir, "clone", ".git", "HEAD"), "ref")
	write(t, filepath.Join(home, ".local/fennec/launches/brave-otter.json"), `{"id":"brave-otter","agent":"claude","local_directory":"/","device":"5d4282fd9e41e0dc1f42f5ee126e6eb7"}`)
	write(t, filepath.Join(home, ".local/fennec/launches/calm-river.json"), `{"id":"calm-river","resume_from":"brave-otter","agent":"claude","local_directory":"/","device":"5d4282fd9e41e0dc1f42f5ee126e6eb7"}`)
	transcript := `{"type":"user","cwd":"` + workdir + `","uuid":"a1","message":"hello"}` + "\n" +
		`{"type":"assistant","cwd":"` + workdir + `","uuid":"a2","message":"hi"}` + "\n"
	project_ := filepath.Join(home, ".claude", "projects", cloudruntime.ClaudeProjectDirectory(workdir))
	write(t, filepath.Join(project_, conversationID+".jsonl"), transcript)
	write(t, filepath.Join(project_, conversationID, "subagents", "agent-1.jsonl"), `{"uuid":"s1"}`+"\n")
	// An earlier conversation stayed in the native store from before Magpie
	// was turned on.
	earlier := filepath.Join(home, ".local/fennec/native/claude/projects", cloudruntime.ClaudeProjectDirectory(workdir))
	write(t, filepath.Join(earlier, earlierID+".jsonl"), `{"type":"user","cwd":"`+workdir+`","uuid":"e1"}`+"\n")
	os.Chtimes(filepath.Join(earlier, earlierID+".jsonl"), olderTime, olderTime)
	os.Chtimes(filepath.Join(project_, conversationID+".jsonl"), latestTime, latestTime)
	return home, project.ID
}

func export(t *testing.T, home string, sessions bool) []byte {
	t.Helper()
	var archive bytes.Buffer
	_, _, err := Export(context.Background(), home, Options{Sessions: sessions, Now: func() time.Time { return time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC) }}, &archive)
	if err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestExportImportMovesQueuesAndSessionsToAnotherHome(t *testing.T) {
	from, projectID := source(t)
	archive := export(t, from, true)

	manifest, err := Inspect(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Counts.Projects != 1 || manifest.Counts.Requests != 2 || manifest.Counts.Attachments != 1 || manifest.Counts.Knowledge != 3 || manifest.Counts.Sessions != 1 || manifest.Counts.Launches != 2 || manifest.SourceHome != from {
		t.Fatalf("manifest %+v", manifest)
	}

	// The target is a self-hosted computer with its own home and a knowledge
	// file of the same name.
	to := filepath.Join(t.TempDir(), "home", "fennec")
	target := board.Store{Home: to, Role: "cloud"}
	write(t, filepath.Join(target.KnowledgePath(projectID), "architecture", "differs.md"), "written on the new computer\n")

	result, err := Import(context.Background(), to, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectsAdded != 1 || result.RequestsAdded != 2 || result.AttachmentsAdded != 1 || result.KnowledgeAdded != 2 || len(result.KnowledgeConflicts) != 1 || result.SessionsRestored != 1 || result.LaunchesRestored != 2 || result.ConversationsCopied != 2 || result.ConversationsFailed != 0 {
		t.Fatalf("result %+v", result)
	}

	requests, err := target.Requests(projectID)
	if err != nil || len(requests) != 2 {
		t.Fatal(requests, err)
	}
	for _, summary := range requests {
		if summary.Title != "First" {
			continue
		}
		request, err := target.Request(projectID, summary.Number)
		if err != nil || len(board.RequestAttachments(request)) != 1 || !target.AttachmentsReady(request) {
			t.Fatal("attachment missing", err, summary.Number)
		}
	}
	if _, err := os.Stat(filepath.Join(target.ThreadsPath(projectID), "INDEX.md")); err != nil {
		t.Fatal("thread mirror not rebuilt:", err)
	}
	kept, _ := os.ReadFile(filepath.Join(target.KnowledgePath(projectID), "architecture", "differs.md"))
	imported, _ := os.ReadFile(filepath.Join(target.KnowledgePath(projectID), "architecture", "differs.imported.md"))
	if string(kept) != "written on the new computer\n" || string(imported) != "from the old computer\n" {
		t.Fatalf("knowledge conflict: %q %q", kept, imported)
	}

	workdir := filepath.Join(to, ".fen", "brave-otter")
	if data, _ := os.ReadFile(filepath.Join(workdir, "report", "notes.md")); string(data) != "notes\n" {
		t.Fatal("session folder not restored")
	}
	for _, left := range []string{"report/node_modules", "clone/.git"} {
		if _, err := os.Stat(filepath.Join(workdir, left)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s should not be archived", left)
		}
	}
	// The conversation moved to the directory Claude derives from the new
	// home, with its working directory rewritten.
	// The new computer runs agents natively (no agent-mode.json), so they live
	// under its native configuration.
	moved := filepath.Join(cloudruntime.CloudAgentHome(to, "claude"), "projects", cloudruntime.ClaudeProjectDirectory(workdir))
	if !strings.HasPrefix(moved, filepath.Join(to, ".local/fennec/native")) {
		t.Fatal("unexpected agent home", moved)
	}
	data, err := os.ReadFile(filepath.Join(moved, conversationID+".jsonl"))
	if err != nil || strings.Contains(string(data), from) || !strings.Contains(string(data), `"cwd":"`+workdir+`"`) || !strings.Contains(string(data), `"uuid":"a1"`) {
		t.Fatalf("conversation not relocated: %v\n%s", err, data)
	}
	if _, err := os.Stat(filepath.Join(moved, conversationID, "subagents", "agent-1.jsonl")); err != nil {
		t.Fatal("subagent transcript missing:", err)
	}
	if data, err := os.ReadFile(filepath.Join(moved, earlierID+".jsonl")); err != nil || !strings.Contains(string(data), `"cwd":"`+workdir+`"`) {
		t.Fatal("conversation from the other store missing:", err)
	}
	// Transcripts keep their times, so the latest conversation stays latest.
	for id, want := range map[string]time.Time{conversationID: latestTime, earlierID: olderTime} {
		if info, err := os.Stat(filepath.Join(moved, id+".jsonl")); err != nil || !info.ModTime().Equal(want) {
			t.Fatalf("%s time %v, want %v", id, info.ModTime(), want)
		}
	}
	// Replying to the thread resumes it: a new session continues the latest
	// conversation of the restored folder.
	if bundle, args, err := cloudruntime.ResumeArguments(cloudruntime.CloudAgentHome(to, "claude"), "claude", workdir, "quiet-lake", ""); err != nil || bundle == nil || bundle.Conversation != conversationID || len(args) < 2 || args[1] != conversationID {
		t.Fatalf("resume after import: %v %v %v", bundle, args, err)
	}

	// Importing the same archive again changes nothing.
	again, err := Import(context.Background(), to, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	if again.ProjectsAdded != 0 || again.RequestsAdded != 0 || again.RequestsExisting != 2 || again.AttachmentsAdded != 0 || again.KnowledgeAdded != 0 || len(again.KnowledgeConflicts) != 0 || again.SessionsRestored != 0 || again.SessionsExisting != 1 || again.LaunchesRestored != 0 {
		t.Fatalf("second import %+v", again)
	}
	if entries, _ := os.ReadDir(filepath.Join(to, ".local/fennec/archive-import")); len(entries) != 0 {
		t.Fatal("staging left behind")
	}
}

func TestImportKeepsNewerChangesOnTheTarget(t *testing.T) {
	from, projectID := source(t)
	archive := export(t, from, false)
	to := filepath.Join(t.TempDir(), "home")
	if _, err := Import(context.Background(), to, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	target := board.Store{Home: to, Role: "cloud"}
	second := 0
	summaries, _ := target.Requests(projectID)
	for _, summary := range summaries {
		if summary.Title == "Second" {
			second = summary.Number
		}
	}
	if _, err := target.Reply(projectID, second, "shiqimei", "", "written after the import"); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), to, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	request, err := target.Request(projectID, second)
	if err != nil || len(request.Messages) != 2 || request.Messages[1].Body != "written after the import" {
		t.Fatalf("reply lost: %+v %v", request.Messages, err)
	}
	if _, err := os.Stat(filepath.Join(to, ".fen", "brave-otter")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a queues-only archive restored a session")
	}
}

func TestDamagedArchiveChangesNothing(t *testing.T) {
	from, _ := source(t)
	archive := export(t, from, true)
	for name, damaged := range map[string][]byte{
		"truncated": archive[:len(archive)*2/3],
		"flipped":   append(append(append([]byte{}, archive[:len(archive)/2]...), archive[len(archive)/2]^0xff), archive[len(archive)/2+1:]...),
		"not zstd":  []byte("hello"),
	} {
		to := filepath.Join(t.TempDir(), "home")
		if _, err := Import(context.Background(), to, bytes.NewReader(damaged)); err == nil {
			t.Fatalf("%s: import succeeded", name)
		}
		for _, dir := range []string{".fen", ".claude", ".local/fennec/launches"} {
			if _, err := os.Stat(filepath.Join(to, dir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s: %s was written", name, dir)
			}
		}
	}
}

func TestNamesStayInsideTheLayout(t *testing.T) {
	for name, want := range map[string]bool{
		"board/journal.json": true,
		"board/attachments/" + strings.Repeat("a", 64) + ".png": true,
		"board/knowledge/this/architecture/x.md":                true,
		"sessions/brave-otter/workdir/a/b.txt":                  true,
		"sessions/brave-otter/launch.json":                      true,
		"sessions/brave-otter/conversations/0/bundle.json":      true,
		"sessions/brave-otter/workdir/../../x":                  false,
		"sessions/../x/workdir/a":                               false,
		"/etc/passwd":                                           false,
		"board/knowledge/../../x/y":                             false,
		"sessions/Brave/workdir/a":                              false,
		"other/file":                                            false,
	} {
		if validName(name) != want {
			t.Errorf("validName(%q) = %v", name, !want)
		}
	}
}
