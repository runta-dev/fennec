package board

import (
	"strings"
	"testing"
)

func TestRunPromptNamesOnlyTheKnowledgeRoot(t *testing.T) {
	p := Project{ID: "this", Key: "THI", Name: "Fennec", Tags: DefaultTags}
	r := Request{Number: 29, Title: "Knowledge", Tag: "bug", Status: Working, Agent: "claude", Session: "s1",
		Messages: []Message{{ID: 1, Author: "shiqi", Body: "Remember this."}}}
	prompt := RunPrompt(p, r, "fennec-terminal queue")
	for _, want := range []string{"~/.fen/board/projects/this/knowledge/", "OKF bundle", "asks you to remember", "Remember this.", "reply this 29 claude s1 resolved"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	// The agent owns the bundle's layout and fields.
	for _, unwanted := range []string{"<section>", "front matter", "status: draft"} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("prompt prescribes %q:\n%s", unwanted, prompt)
		}
	}
}
