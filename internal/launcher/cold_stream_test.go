package launcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestColdStreamRejectsUnverifiedDestinationBeforeIO(t *testing.T) {
	home := t.TempDir()
	known := filepath.Join(home, "known_hosts")
	os.WriteFile(known, []byte("replaced-key"), 0600)
	for _, connection := range []*ColdConnection{nil, {runtimeID: "other"}, {runtimeID: "expected", knownPath: known, known: []byte("original-key")}} {
		if err := ExecuteColdStream(context.Background(), LaunchPlan{RuntimeID: "expected"}, LaunchIntent{}, home, func() { t.Fatal("unverified terminal became ready") }, connection); err == nil {
			t.Fatal("unverified destination accepted")
		}
	}
}
