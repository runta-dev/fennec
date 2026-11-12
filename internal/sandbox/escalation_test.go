package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEscalation(t *testing.T) {
	for _, tc := range []struct {
		command, inner, reason string
		ok                     bool
	}{
		{`fennec-escalate --reason "push the release branch" -- git push origin main`, "git push origin main", "push the release branch", true},
		{`fennec-escalate --reason='install deps' -- npm install`, "npm install", "install deps", true},
		{`  fennec-escalate --reason 'a b'   --   echo "x; y"`, `echo "x; y"`, "a b", true},
		{`fennec-escalate -- git push`, "", "", false},                  // reason required
		{`fennec-escalate --reason "x"`, "", "", false},                 // command required
		{`fennec-escalate --force --reason x -- ls`, "", "", false},     // unknown option
		{`fennec-escalated --reason x -- ls`, "", "", false},            // different command
		{`echo fennec-escalate --reason x -- ls`, "", "", false},        // not a prefix
		{`fennec-escalate --reason "unterminated -- ls`, "", "", false}, // malformed quoting
	} {
		inner, reason, ok := ParseEscalation(tc.command)
		if ok != tc.ok || (ok && (inner != tc.inner || reason != tc.reason)) {
			t.Fatalf("ParseEscalation(%q) = %q, %q, %v", tc.command, inner, reason, ok)
		}
		if !ok && inner != tc.command {
			t.Fatalf("unescalated command changed: %q", inner)
		}
	}
}

func TestDangerousCommandMatchesCodexForcedRm(t *testing.T) {
	for _, command := range []string{
		"rm -rf build", "rm -f a.txt", "rm --force x", "rm -Rf /tmp/x",
		"sudo rm -rf /", "sudo -u root rm -fr dir", "env A=1 rm -f x", "FOO=1 rm -f x",
		"ls && rm -rf x", "echo hi; rm -f x", "bash -lc 'rm -rf x'", `sh -c "cd x && rm -rf y"`,
		`echo "$(rm -rf x)"`, "echo `rm -f x`", "true | rm -rf x",
	} {
		if !IsDangerousCommand(command) {
			t.Fatalf("expected dangerous: %q", command)
		}
	}
	for _, command := range []string{
		"rm file.txt", "rm -r dir", "rm -- -f", "grep -rf pattern .", "echo rm -rf x",
		"git status", "npm install", "curl https://example.com", "cat ~/.ssh/config",
	} {
		if IsDangerousCommand(command) {
			t.Fatalf("unexpected dangerous: %q", command)
		}
	}
	if !IsDangerousCommand(strings.Repeat("$(", 10) + "true" + strings.Repeat(")", 10)) {
		t.Fatal("deep nesting must fail closed")
	}
}

func TestSandboxDenialHeuristicMatchesCodex(t *testing.T) {
	if IsLikelySandboxDenied(0, "operation not permitted") {
		t.Fatal("success is never a denial")
	}
	for _, out := range []string{"touch: /etc/x: Operation not permitted", "Permission denied", "Read-only file system", "sandbox-exec: denied"} {
		if !IsLikelySandboxDenied(1, out) {
			t.Fatalf("expected denial: %q", out)
		}
	}
	if IsLikelySandboxDenied(127, "command not found") || IsLikelySandboxDenied(2, "usage: ls") {
		t.Fatal("ordinary failures are not denials")
	}
	if !strings.Contains(EscalationHint("git push"), `fennec-escalate --reason "<why this is needed>" -- git push`) {
		t.Fatal("hint must show the exact escalation form")
	}
}

func TestProfilesProtectMetadataAndEscalationKeepsDeniedReads(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspace := PolicyForPreset(PresetAsk, []string{root})
	profile, params := BuildProfile(workspace, home)
	joined := strings.Join(params, " ")
	for _, want := range []string{filepath.Join(root, ".git"), filepath.Join(root, ".codex")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("protected metadata %s missing from %s", want, joined)
		}
	}
	if strings.Contains(joined, filepath.Join(root, ".aws")) {
		t.Fatal("absent .aws should not be protected (Codex protects it only when present)")
	}
	if !strings.Contains(profile, "(deny network*)") {
		t.Fatal("workspace sandbox must deny network")
	}
	escalated := ForApprovedEscalation(workspace)
	profile, params = BuildProfile(escalated, home)
	if !strings.Contains(profile, `(allow file-write* (regex #"^/"))`) || !strings.Contains(profile, "(allow network*)") {
		t.Fatalf("approved escalation must allow writes and network:\n%s", profile)
	}
	if !strings.Contains(profile, "(deny file-read* file-write* (subpath") || !strings.Contains(strings.Join(params, " "), filepath.Join(home, ".ssh")) {
		t.Fatal("approved escalation must keep denied-read credential paths")
	}
	if NormalizeMode(ModeEscalated) != ModeEscalated || !ValidMode(ModeEscalated) {
		t.Fatal("escalated mode must normalize to itself")
	}
}
