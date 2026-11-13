package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"fennec/cua/internal/daemon"
)

func TestBrowserCmdUsage(t *testing.T) {
	paths := daemon.PathsIn(filepath.Join(t.TempDir(), "d"))
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"nope"}, {"profiles", "x"}, {"import", "--bogus"}, {"import", "extra"}} {
		if err := browserCmdTo(context.Background(), paths, args, &out); err == nil || !strings.HasPrefix(err.Error(), "usage: fennec-cua browser") {
			t.Errorf("%v: %v", args, err)
		}
	}
	// No daemon: the error says so.
	if err := browserCmdTo(context.Background(), paths, []string{"import", "--profile", "Work"}, &out); err == nil || !strings.Contains(err.Error(), "is the daemon running?") {
		t.Errorf("no daemon: %v", err)
	}
}
