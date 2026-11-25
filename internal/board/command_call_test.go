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
	if err := Call(home, "POST", "/v1/projects", strings.NewReader(`{"name":"Errand","color":"#f38d25"}`), &out); err != nil {
		t.Fatal(err)
	}
	p, err := (Store{Home: home, Role: "cloud"}).Projects()
	if err != nil || len(p) != 1 {
		t.Fatalf("projects = %v, %v", p, err)
	}
	out.Reset()
	if err := Call(home, "DELETE", "/v1/projects/"+p[0].ID, nil, &out); err != nil {
		t.Fatal(err)
	}
	if p, _ = (Store{Home: home, Role: "cloud"}).Projects(); len(p) != 0 {
		t.Fatalf("still listed: %v", p)
	}
	if err := Call(home, "PUT", "/v1/projects", nil, &out); err == nil {
		t.Fatal("PUT was accepted")
	}
}
