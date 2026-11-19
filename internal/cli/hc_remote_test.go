package cli

import "testing"

func TestHCRemoteQuotesArguments(t *testing.T) {
	got := hcRemote([]string{"put", "--json", "t7", "/a b/it's"})
	want := `exec "$(command -v hc || printf %s "$HOME/.local/bin/hc")" 'put' '--json' 't7' '/a b/it'\''s'`
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}
