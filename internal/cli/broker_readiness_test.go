package cli

import "testing"

func TestBrokerListeningDoesNotMakeUnconfiguredAgentReady(t *testing.T) {
	if brokerAgentReady("codex", []string{"claude"}) {
		t.Fatal("Codex launch released before its route was restored")
	}
	if !brokerAgentReady("codex", []string{"claude", "codex"}) {
		t.Fatal("configured Codex stayed blocked")
	}
	if !brokerAgentReady("", nil) {
		t.Fatal("existing-session attachment incorrectly gated by agent routes")
	}
}
