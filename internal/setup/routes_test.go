package setup

import "testing"

func TestParseAgentModels(t *testing.T) {
	for input, want := range map[string]string{"Codex model devin/gpt-6-astra-priority": "devin/gpt-6-astra-priority", "Pi model magpie/codex/gpt-6.1-sol": "codex/gpt-6.1-sol", "Claude Code model codex/model": "codex/model"} {
		got, err := ParseAgentModel(input)
		if err != nil || got != want {
			t.Fatalf("%s %v", got, err)
		}
	}
}
func TestRejectDefaultAndMultilineModels(t *testing.T) {
	for _, input := range []string{"OpenCode model default", "model unknown", "Codex model /x", "Codex model p/", "Codex model p/m\nextra"} {
		if _, err := ParseAgentModel(input); err == nil {
			t.Fatal(input)
		}
	}
}
