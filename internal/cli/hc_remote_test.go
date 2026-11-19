package cli

import "testing"

func TestHCRemoteQuotesArguments(t *testing.T) {
	got := hcRemote([]string{"put", "--json", "t7", "/a b/it's"})
	want := `exec "$(command -v hc || printf %s "$HOME/.local/bin/hc")" 'put' '--json' 't7' '/a b/it'\''s'`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

// The desktop window runs Contents/MacOS/fen as `hc` (Resources/fennec-hc):
// it must reach hcCommand, not the public fen client.
func TestHCIsAPrivateInvocation(t *testing.T) {
	if PublicInvocation([]string{"hc", "connect"}) {
		t.Fatal("fen hc must not be routed to the public client")
	}
}
