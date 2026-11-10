package syncengine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegisteredProjectLookupAndAmbiguity(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	sub := filepath.Join(root, "src")
	os.MkdirAll(sub, 0700)
	registry := filepath.Join(base, "registry")
	a := Registration{Root: root, Session: "fennec-one", DataDirectory: filepath.Join(base, "user-one")}
	if err := Register(registry, a); err != nil {
		t.Fatal(err)
	}
	got, err := FindProject(registry, sub)
	if err != nil || got.Session != a.Session {
		t.Fatal(got, err)
	}
	if err := Register(registry, a); err != nil {
		t.Fatal(err)
	}
	if _, err := FindProject(registry, sub); err != nil {
		t.Fatal("duplicate registration", err)
	}
	b := a
	b.Session = "fennec-two"
	b.DataDirectory = filepath.Join(base, "user-two")
	Register(registry, b)
	if _, err := FindProject(registry, sub); err == nil {
		t.Fatal("ambiguous account selected")
	}
	if _, err := FindProject(registry, base); err == nil {
		t.Fatal("ancestor matched project")
	}
}
