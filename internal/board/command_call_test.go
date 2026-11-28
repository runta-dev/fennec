package board

import (
	"bytes"
	"strings"
	"testing"
)

// The iPhone app deletes a queue through board-call, so DELETE goes through.
func TestCallDeletesAProject(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	// The board keeps its last project (FEN-231), so it needs a second one.
	if err := Call(home, "POST", "/v1/projects", strings.NewReader(`{"name":"Keep"}`), &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Call(home, "POST", "/v1/projects", strings.NewReader(`{"name":"Errand","color":"#f38d25"}`), &out); err != nil {
		t.Fatal(err)
	}
	p, err := (Store{Home: home, Role: "cloud"}).Projects()
	if err != nil || len(p) != 2 {
		t.Fatalf("projects = %v, %v", p, err)
	}
	out.Reset()
	if err := Call(home, "DELETE", "/v1/projects/"+p[1].ID, nil, &out); err != nil {
		t.Fatal(err)
	}
	if p, _ = (Store{Home: home, Role: "cloud"}).Projects(); len(p) != 1 || p[0].Name != "Keep" {
		t.Fatalf("still listed: %v", p)
	}
	if err := Call(home, "PUT", "/v1/projects", nil, &out); err == nil {
		t.Fatal("PUT was accepted")
	}
}
