package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCommands(t *testing.T) {
	var output bytes.Buffer
	if err := Run([]string{"agents"}, &output); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(output.Bytes()) {
		t.Fatal("invalid discovery output")
	}
	for _, args := range [][]string{{"claude"}, {"agents", "extra"}, {"serve", "-addr", "0.0.0.0:7420"}, {"serve", "extra"}} {
		if err := Run(args, &output); err == nil {
			t.Fatalf("must reject %v", args)
		}
	}
	if err := Run(nil, &output); err != nil {
		t.Fatal(err)
	}
}
