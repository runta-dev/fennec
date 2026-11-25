package chatlog

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Local reads logs on this machine: the cloud computer side of
// `fennec-terminal transcript`.
func Local() *Reader {
	return &Reader{Run: func(command string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd := exec.CommandContext(ctx, shell, "-c", command)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			if message := strings.TrimSpace(stderr.String()); message != "" {
				return nil, errors.New(message)
			}
			return nil, err
		}
		return out, nil
	}}
}
