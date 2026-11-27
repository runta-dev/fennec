package board

import "testing"

// FEN-145: a request carries the composer's model and effort.
func TestCreateRequestWithModelAndEffort(t *testing.T) {
	s := Store{Home: t.TempDir()}
	p, err := s.CreateProject(Project{Name: "Q"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequestWith(p.ID, "me", "Hi", nil, "", nil, Choice{Agent: "codex", Model: "gpt-6.2-sol", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Request(p.ID, r.Number); got.Agent != "codex" || got.Model != "gpt-6.2-sol" || got.Effort != "high" {
		t.Fatalf("stored %q %q %q", got.Agent, got.Model, got.Effort)
	}
	for _, bad := range []Choice{{Model: "opus"}, {Agent: "claude", Effort: "HIGH"}, {Agent: "claude", Model: "a b"}, {Agent: "claude", Model: "-x"}, {Agent: "nope"}} {
		if _, err := s.CreateRequestWith(p.ID, "me", "Hi", nil, "", nil, bad); err != ErrInvalid {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}
