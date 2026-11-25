package board

import "testing"

func TestPermissionAnswerSummaryQuotesCommand(t *testing.T) {
	yes, no := true, false
	q := Question{Kind: "permission", Text: `approve Bash: echo \"$HOME\" && ls *.go`}
	cases := []struct {
		a    Answer
		want string
	}{
		{Answer{Allow: &yes}, "Allowed:\n\n```\nBash: echo \\\"$HOME\\\" && ls *.go\n```"},
		{Answer{Allow: &no}, "Denied:\n\n```\nBash: echo \\\"$HOME\\\" && ls *.go\n```"},
		{Answer{Allow: &no, Text: "Use make clean"}, "Denied:\n\n```\nBash: echo \\\"$HOME\\\" && ls *.go\n```\n\nUse make clean"},
	}
	for _, c := range cases {
		if got := AnswerSummary(q, c.a); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestCodeBlockOutgrowsBackticks(t *testing.T) {
	if got, want := codeBlock("a ```` b"), "`````\na ```` b\n`````"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := codeBlock("`x`"), "```\n`x`\n```"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
