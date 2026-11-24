package board

import (
	"errors"
	"strings"
	"testing"
)

func TestSessionTitle(t *testing.T) {
	session := strings.Repeat("ab", 16)
	for _, c := range []struct{ in, want string }{
		{"Fennec menubar Get Started card", "Fennec menubar Get Started card"},
		{"✳ Fix login", "Fix login"},
		{"⠙ Fix login | " + session, "Fix login"},
		{"修复暗色主题 · " + session, "修复暗色主题"},
		{"Fix login | abababab…", "Fix login"},
		{"THI-5 队列线程标题同步", "队列线程标题同步"},
		{"thi-5: Sync titles", "Sync titles"},
		{"THI-50 is another request", "THI-50 is another request"},
		{"(draft) plan", "(draft) plan"},
		{"", ""},
		{"✳ Claude Code", ""},
		{"codex", ""},
		{session, ""},
		{"THI-5", ""},
		{"~/.fen/" + session, ""},
		{"fennec-queue", ""},
		{"Fix\x1b login", "Fix login"},
		{strings.Repeat("长", 300), strings.Repeat("长", maxTitle)},
	} {
		if got := SessionTitle(c.in, session, "THI-5"); got != c.want {
			t.Errorf("SessionTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRetitleFollowsOnlyTheWorkingSession(t *testing.T) {
	s := Store{Home: t.TempDir()}
	p, _ := s.CreateProject(Project{Name: "Work"})
	text := "Sync the thread title with the session\nand keep this as the body"
	r, err := s.CreateRequest(p.ID, "me", "Sync the thread title with the session", "", text)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Retitle(p.ID, r.Number, "ababababababababababababababababab"[:32], "Early"); !errors.Is(err, ErrConflict) {
		t.Fatal("an unclaimed request must not be retitled", err)
	}
	session := strings.Repeat("cd", 16)
	if r, err = s.Claim(p.ID, r.Number, session, "claude"); err != nil {
		t.Fatal(err)
	}
	updated := r.Updated
	if err = s.Retitle(p.ID, r.Number, strings.Repeat("ef", 16), "Other"); !errors.Is(err, ErrConflict) {
		t.Fatal("another session must not retitle the request", err)
	}
	if err = s.Retitle(p.ID, r.Number, session, "Queue thread title sync"); err != nil {
		t.Fatal(err)
	}
	if err = s.Retitle(p.ID, r.Number, session, "bad\ntitle"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	r, _ = s.Request(p.ID, r.Number)
	if r.Title != "Queue thread title sync" || r.Messages[0].Body != text || !r.Updated.Equal(updated) {
		t.Fatal(r.Title, r.Messages[0].Body, r.Updated, updated)
	}
}
