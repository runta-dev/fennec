package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestGeneratedPayloadEscapesAndIsDeterministic(t *testing.T) {
	raw := []byte(`{"version":1,"messages":{"ui.example":{"en-US":"A \"quote\", a \\ path, and <tag>"},"ui.other":{"en-US":"Other"}}}`)
	js, swift, err := generate(raw)
	if err != nil {
		t.Fatal(err)
	}
	nextJS, nextSwift, err := generate(raw)
	if err != nil || !bytes.Equal(js, nextJS) || !bytes.Equal(swift, nextSwift) {
		t.Fatal("nondeterministic generated catalog", err)
	}
	script := `const window={};` + string(js) + `;if(window.FennecMessages['ui.example']['en-US']!=='A "quote", a \\ path, and <tag>')process.exit(1);`
	if output, err := exec.Command("node", "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("invalid web payload: %v %s", err, output)
	}
	if strings.Contains(string(swift), `\u003c`) || !strings.Contains(string(swift), `\"quote\"`) {
		t.Fatal("invalid Swift string escaping")
	}
}
