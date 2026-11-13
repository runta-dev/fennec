package localssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopMCPAcceptsOnlyChatGPTExecutables(t *testing.T) {
	directory := t.TempDir()
	outside := filepath.Join(directory, "tool")
	os.WriteFile(outside, []byte("#!/bin/sh\n"), 0700)
	disguised := filepath.Join(directory, "Applications/ChatGPT.app/Contents/escape")
	os.MkdirAll(filepath.Dir(disguised), 0700)
	os.Symlink(outside, disguised)
	for _, spec := range []DesktopMCPSpec{
		{Name: "node_repl", Command: outside},
		{Name: "node_repl", Command: "/bin/sh", Args: []string{"-c", "id"}},
		{Name: "node_repl", Command: disguised},
		{Name: "bad name!", Command: "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT"},
		{Name: "node_repl", Command: "relative/node_repl"},
	} {
		command, err := EncodeDesktopMCP(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, ok, err := parseDesktopMCP(command); !ok || err == nil {
			t.Fatalf("accepted %+v", spec)
		}
	}
	if _, _, ok, _ := parseDesktopMCP("ls -la"); ok {
		t.Fatal("ordinary exec treated as desktop MCP")
	}
	real := "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT"
	if _, err := os.Stat(real); err != nil {
		t.Skip("ChatGPT.app not installed")
	}
	command, _ := EncodeDesktopMCP(DesktopMCPSpec{Name: "cua_repl", Command: real, Args: []string{"mcp"}, Env: map[string]string{"A": "b"}})
	if spec, executable, ok, err := parseDesktopMCP(command); !ok || err != nil || executable != real || spec.Name != "cua_repl" {
		t.Fatal(spec, executable, ok, err)
	}
}
