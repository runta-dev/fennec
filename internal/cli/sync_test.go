package cli

import (
	"bytes"
	"testing"
)

func TestManualSyncRejectsBroadOrUnknownActions(t *testing.T) {
	for _, args := range [][]string{nil, {"resolve"}, {"flush", "--all"}, {"flush", "--state-dir", "relative", "fennec-x"}, {"status", "--state-dir", "/tmp"}} {
		var out bytes.Buffer
		if syncCommand(args, &out) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
