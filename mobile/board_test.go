package fennecmobile

import (
	"errors"
	"strings"
	"testing"
)

func TestBoardScriptQuotesOnlySafePaths(t *testing.T) {
	script, err := boardScript("GET", "/v1/projects/this/requests/124")
	if err != nil || !strings.Contains(script, "board-call GET '/v1/projects/this/requests/124'") {
		t.Fatalf("script = %q, %v", script, err)
	}
	for _, path := range []string{"/v1/x'; rm -rf ~", "/v1/../etc", "/v2/projects", "/v1/a b", "/v1/$(id)"} {
		if _, err := boardScript("GET", path); err == nil {
			t.Errorf("%q was accepted", path)
		}
	}
	if _, err := boardScript("PUT", "/v1/projects"); err == nil {
		t.Error("PUT was accepted")
	}
}

func TestBoardErrorKeepsTheBoardsMessage(t *testing.T) {
	if got := boardError(errors.New("board-call: 409 conflict")).Error(); got != "conflict" {
		t.Fatalf("got %q", got)
	}
	if got := boardError(errors.New("ssh: handshake failed")).Error(); got != "ssh: handshake failed" {
		t.Fatalf("got %q", got)
	}
}
