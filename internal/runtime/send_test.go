package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestSendPreservesLiteralInput(t *testing.T) {
	id := strings.Repeat("a", 32)
	text := "--force $(touch should-not-exist)\n'quoted'"
	runtime := &LocalRuntime{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "get" {
			return []byte(`{"state":"live","labels":{"fennec.session":"` + id + `"}}`), nil
		}
		if !reflect.DeepEqual(args, []string{"send", "--json", "--", "t1", text}) {
			t.Fatal(args)
		}
		return []byte(`{"started":true,"status":"working"}`), nil
	}}
	if _, err := runtime.Operation(context.Background(), LocalOperation{Kind: "send", Address: "t1", SessionID: id, Text: text}); err != nil {
		t.Fatal(err)
	}
}
func TestSendRejectsInvalidInput(t *testing.T) {
	for _, text := range []string{"", " ", "x\x00y", strings.Repeat("x", 16385)} {
		if _, err := SendScript("t1", strings.Repeat("a", 32), text); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
