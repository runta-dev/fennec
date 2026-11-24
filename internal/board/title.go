package board

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A request starts with the first line of what the person typed as a
// placeholder title; the whole text is its first message. Once the agent
// working on it names its session (its OSC terminal title), the scheduler
// copies that name onto the request, so the thread and the session share one
// title.

var (
	// Spinners and status marks agents put before their title ("✳ Fix login").
	titleMarks = regexp.MustCompile(`^[^\p{L}\p{N}(\[~/]+`)
	// A trailing "| <session>" or "· <session>", or an abbreviated id.
	titleSuffix = regexp.MustCompile(`\s*[|·]\s*(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{8,64}(?:\.{3}|…)?)\s*$`)
	// An id on its own is no title.
	technicalTitle = regexp.MustCompile(`(?i)^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{24,64}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12})$`)
	// What agents and shells show before they have named the work.
	defaultTitles = map[string]bool{
		"claude": true, "claude code": true, "claude-code": true, "codex": true, "openai codex": true,
		"pi": true, "opencode": true, "kimi": true, "kimi code": true, "cursor": true, "cursor agent": true,
		"cursor-agent": true, "grok": true, "grok cli": true, "fennec-queue": true, "fennec": true,
		"bash": true, "zsh": true, "sh": true, "fish": true, "node": true,
	}
)

// SessionTitle turns a session's terminal title into a request title, or ""
// when the agent has not named the work yet. key is the request's reference
// ("FEN-42"), which the thread already shows, so a leading copy is dropped.
func SessionTitle(title, session, key string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, title)
	title = strings.TrimSpace(titleMarks.ReplaceAllString(strings.TrimSpace(title), ""))
	if session != "" {
		for _, separator := range []string{" | ", " · "} {
			title = strings.TrimSpace(strings.TrimSuffix(title, separator+session))
		}
	}
	title = strings.TrimSpace(titleSuffix.ReplaceAllString(title, ""))
	if key != "" && len(title) >= len(key) && strings.EqualFold(title[:len(key)], key) {
		rest := title[len(key):]
		if rest == "" || !unicode.IsLetter([]rune(rest)[0]) && !unicode.IsDigit([]rune(rest)[0]) {
			title = strings.TrimSpace(strings.TrimLeft(rest, " :：-–—·|"))
		}
	}
	if title == "" || strings.EqualFold(title, session) || technicalTitle.MatchString(title) ||
		defaultTitles[strings.ToLower(title)] || strings.HasPrefix(title, "/") || strings.HasPrefix(title, "~") {
		return ""
	}
	if utf8.RuneCountInString(title) > maxTitle {
		title = strings.TrimSpace(string([]rune(title)[:maxTitle]))
	}
	return title
}

// Retitle names a request after the session working on it. Only that
// session's executor calls it; anything else is a conflict. An unchanged
// title writes nothing, and renaming is not activity, so Updated stays.
func (s Store) Retitle(project string, n int, session, title string) error {
	title = strings.TrimSpace(title)
	if !validText(title, maxTitle*4) || utf8.RuneCountInString(title) > maxTitle || strings.ContainsAny(title, "\r\n") {
		return ErrInvalid
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.Request(project, n)
	if err != nil {
		return err
	}
	if session == "" || r.ExecutionSession() != session {
		return ErrConflict
	}
	if r.Title == title {
		return nil
	}
	r.Title = title
	return s.commit(s.requestPath(project, n), r)
}
