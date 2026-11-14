package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestAttachmentOnlyClassifiesAuthoritativeSessionLoss(t *testing.T) {
	transient := errors.New("Herdr temporarily unreachable")
	for _, tt := range []struct {
		name, listing string
		queryError    error
		permanent     bool
	}{
		{name: "offline", queryError: transient},
		{name: "still listed", listing: `{"terminals":[{"address":"t1"}]}`},
		{name: "deleted", listing: `{"terminals":[]}`, permanent: true},
		{name: "invalid snapshot", listing: `{}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtime := &LocalRuntime{Run: func(context.Context, ...string) ([]byte, error) { return nil, transient }, Query: func(context.Context, string, any) (json.RawMessage, error) {
				return json.RawMessage(tt.listing), tt.queryError
			}}
			_, err := runtime.Terminal(context.Background(), "t1", "recovery-fixture")
			var reference SessionReferenceError
			if errors.As(err, &reference) != tt.permanent {
				t.Fatalf("unexpected classification: %v", err)
			}
		})
	}
	runtime := &LocalRuntime{Run: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"address":"t1","labels":{"fennec.session":"other-session"}}`), nil
	}}
	_, err := runtime.Terminal(context.Background(), "t1", "recovery-fixture")
	var reference SessionReferenceError
	if !errors.As(err, &reference) || reference.Code != "session_identity_mismatch" {
		t.Fatalf("identity mismatch not preserved: %v", err)
	}
}
