package terminalstream

import (
	"errors"
	"strings"
	"testing"
)

func TestOperationFailureKeepsBoundedRedactedCause(t *testing.T) {
	got := operationFailure(errors.New("model accounts sync failed:\nAuthorization: Bearer abcdefghijklmnop OPENAI_API_KEY=sk-live-1234567890abcdef"))
	if !strings.HasPrefix(got, "operation not confirmed; inspect before retrying: model accounts sync failed:") {
		t.Fatalf("cause missing: %q", got)
	}
	if strings.Contains(got, "abcdefghijklmnop") || strings.Contains(got, "sk-live-1234567890abcdef") || strings.Contains(got, "\n") {
		t.Fatalf("secret or newline leaked: %q", got)
	}
	if long := operationFailure(errors.New(strings.Repeat("x", 5000))); len(long) > 400 {
		t.Fatalf("cause not bounded: %d bytes", len(long))
	}
	if got := operationFailure(nil); got != "operation not confirmed; inspect before retrying: result too large" {
		t.Fatalf("oversized result: %q", got)
	}
}
