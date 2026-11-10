package launcher

import (
	"reflect"
	"testing"
)

func TestResolveCodexWorkingDirectory(t *testing.T) {
	cwd, args, err := ResolveLaunchDirectory("codex", "/work", []string{"--model", "p/m", "-C", "../repo", "hello"})
	if err != nil || cwd != "/repo" || !reflect.DeepEqual(args, []string{"--model", "p/m", "hello"}) {
		t.Fatal(cwd, args, err)
	}
}
func TestPromptOptionsAreNotReinterpreted(t *testing.T) {
	original := []string{"--", "--cd=/other", "--add-dir=/secret"}
	cwd, args, err := ResolveLaunchDirectory("codex", "/work", original)
	if err != nil || cwd != "/work" || !reflect.DeepEqual(args, original) {
		t.Fatal(cwd, args, err)
	}
}
func TestRejectUnmappedAdditionalDirectory(t *testing.T) {
	for _, args := range [][]string{{"--add-dir", "/other"}, {"--cd"}, {"--cd="}, {"-C", "a", "--cd=b"}} {
		if _, _, err := ResolveLaunchDirectory("codex", "/work", args); err == nil {
			t.Fatal(args)
		}
	}
}

func TestCodexScalarOptionValuesAreOpaque(t *testing.T) {
	original := []string{"--model", "--cd=not-a-directory", "--config", "--add-dir=not-a-directory", "-C", "actual"}
	cwd, args, err := ResolveLaunchDirectory("codex", "/work", original)
	if err != nil || cwd != "/work/actual" || !reflect.DeepEqual(args, original[:4]) {
		t.Fatal(cwd, args, err)
	}
}
func TestCodexAttachedDirectory(t *testing.T) {
	for _, option := range []string{"-C../repo", "-C=../repo", "--cd=../repo"} {
		cwd, args, err := ResolveLaunchDirectory("codex", "/work", []string{option, "prompt"})
		if err != nil || cwd != "/repo" || !reflect.DeepEqual(args, []string{"prompt"}) {
			t.Fatal(option, cwd, args, err)
		}
	}
}
