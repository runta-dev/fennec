package preview

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHomeAliasSupportsRelativeAndAbsolutePreviewPaths(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "physical-home")
	workspace := filepath.Join(home, ".fen", "amber-cedar")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("workspace report"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "home-alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(physical)
	for _, args := range [][]string{{"report.txt"}, {"--session", "amber-cedar", "report.txt"}, {"--session", "amber-cedar", filepath.Join(physical, "report.txt")}, {"--session", "amber-cedar", filepath.Join(alias, ".fen", "amber-cedar", "report.txt")}} {
		var output bytes.Buffer
		if err := Command(alias, args, &output); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		marks := Parse(output.String())
		if len(marks) != 1 {
			t.Fatal(output.String())
		}
		file, _, err := (Store{Home: alias}).Open("amber-cedar", marks[0].Ref)
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	outside := filepath.Join(home, "private.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, link, filepath.Join(alias, ".fen", "amber-cedar", "escape.txt")} {
		if _, err := (Store{Home: alias}).Register("amber-cedar", path, ""); err == nil {
			t.Fatalf("accepted workspace escape %s", path)
		}
	}
}
