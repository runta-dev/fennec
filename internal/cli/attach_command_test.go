package cli

import (
	"io"
	"strings"
	"testing"
)

func TestAttachCommandRequiresExplicitSessionIdentity(t *testing.T) {
	if err := Run([]string{"attach", "runtime"}, io.Discard); err == nil {
		t.Fatal("incomplete attach command accepted")
	}
	err := Run([]string{"attach", "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", "t1;whoami", strings.Repeat("a", 32)}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid runtime or session identity") {
		t.Fatal("invalid identity reached SSH or receipt lookup", err)
	}
}
