package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRootBoundaries(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(home, "project")
	os.MkdirAll(repo, 0700)
	for _, root := range []string{string(filepath.Separator), base, home} {
		if ValidateRoot(root, home) == nil {
			t.Fatal("broad root accepted", root)
		}
	}
	alias := filepath.Join(base, "alias")
	os.Symlink(home, alias)
	if ValidateRoot(alias, home) == nil {
		t.Fatal("symlink bypassed home boundary")
	}
	if err := ValidateRoot(repo, home); err != nil {
		t.Fatal("project inside home rejected", err)
	}
}
func TestGitRepositoryAtHomeIsStillRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := exec.Command("git", "-C", home, "init", "-q")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(home, "sub")
	os.Mkdir(sub, 0700)
	if _, err := ManifestGit(context.Background(), sub); err == nil {
		t.Fatal("home Git repository accepted")
	}
}
