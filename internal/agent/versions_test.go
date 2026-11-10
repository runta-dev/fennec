package agent

import "testing"

func TestParseVersions(t *testing.T) {
	for input, want := range map[string]string{"opencode v2.1.4\n": "2.1.4", "v1.18.18": "1.18.18", "2.1.9 (Claude Code)": "2.1.9", "codex-cli 0.101.0": "0.101.0", "codex-cli 0.102.0-beta.1": "0.102.0-beta.1"} {
		got, err := parseVersion(input)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", input, got, err)
		}
	}
	for _, input := range []string{"latest", "error", "v1", "vv1.2.3", "v1.2.3; rm -rf /", "1.2.3; rm -rf /"} {
		if _, err := parseVersion(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
func TestBoundedVersionOutput(t *testing.T) {
	b := &limitedOutput{}
	p := make([]byte, 16384)
	n, _ := b.Write(p)
	if n != len(p) || len(b.text) != 8192 {
		t.Fatal("output not bounded")
	}
}
