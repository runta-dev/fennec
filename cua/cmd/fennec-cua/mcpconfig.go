package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	"fennec/cua/internal/standalone"
)

// mcpConfigCmd is `fennec-cua mcp-config [client]`: how to connect an agent,
// with the command path filled in (the link on PATH when it leads here,
// else this binary).
func mcpConfigCmd(self string, args []string, out io.Writer) error {
	if len(args) > 1 {
		return errors.New("usage: fennec-cua mcp-config [claude|codex|json|http]")
	}
	client := ""
	if len(args) == 1 {
		client = args[0]
	}
	command := self
	if p, err := exec.LookPath(standalone.BinName); err == nil {
		if r, err := filepath.EvalSymlinks(p); err == nil && r == self {
			if abs, err := filepath.Abs(p); err == nil {
				command = abs
			}
		}
	}
	text, err := standalone.MCPConfig(client, command)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(out, text)
	return err
}
