package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestStopRefusesOtherSessions(t *testing.T) {
	runtime := &LocalRuntime{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "get" {
			t.Fatal("unrelated terminal killed")
		}
		return []byte(`{"state":"live","labels":{"fennec.session":"other"}}`), nil
	}}
	if _, err := runtime.Stop(context.Background(), "t1", strings.Repeat("a", 32)); err == nil {
		t.Fatal("wrong session accepted")
	}
}
func TestStopConfirmsExit(t *testing.T) {
	id := strings.Repeat("a", 32)
	killed := false
	calls := 0
	runtime := &LocalRuntime{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if args[0] == "kill" {
			killed = true
			return nil, nil
		}
		state := "live"
		if killed {
			state = "finished"
		}
		return []byte(fmt.Sprintf(`{"state":%q,"labels":{"fennec.session":%q}}`, state, id)), nil
	}}
	result, err := runtime.Stop(context.Background(), "t1", id)
	if err != nil || result.State != "finished" || calls != 3 {
		t.Fatal(result, err, calls)
	}
	if _, err := runtime.Stop(context.Background(), "t1", id); err != nil || calls != 4 {
		t.Fatal("finished stop not idempotent", err, calls)
	}
}
func TestStopRejectsUnsafeAddress(t *testing.T) {
	for _, address := range []string{"-x", "remote/t1", "t1;id", "t1\n"} {
		if _, err := StopScript(address, strings.Repeat("a", 32)); err == nil {
			t.Fatal(address)
		}
	}
}
