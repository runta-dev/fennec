package setup

import "testing"

func TestMagpieTurnedOffForOneAgentLeavesOthersOnGateway(t *testing.T) {
	home := t.TempDir()
	if err := ConfigureAgentMode(home, false, "kimi", "kimi"); err != nil {
		t.Fatal(err)
	}
	if NativeAgentMode(home) {
		t.Fatal("one agent turned the whole gateway off")
	}
	if !NativeAgent(home, "kimi") || !NativeAgent(home, "cursor") {
		t.Fatal("kimi and cursor must keep their own accounts")
	}
	if NativeAgent(home, "codex") || NativeAgent(home, "claude") {
		t.Fatal("other agents left Magpie")
	}
	if err := ConfigureAgentMode(home, false); err != nil {
		t.Fatal(err)
	}
	if NativeAgent(home, "kimi") {
		t.Fatal("kimi stayed off Magpie after it was turned back on")
	}
	if err := ConfigureAgentMode(home, true, "kimi"); err != nil {
		t.Fatal(err)
	}
	if !NativeAgent(home, "codex") {
		t.Fatal("Settings switch no longer turns Magpie off for every agent")
	}
}

func TestNativeAgentSelectionRequiresGatewayInventory(t *testing.T) {
	valid := ModelAccountsRequest{Version: 1, Agents: []string{"codex", "kimi"}, NativeAgents: []string{"kimi"}}
	if err := ValidateModelAccountsRequest(valid); err != nil {
		t.Fatal(err)
	}
	if !valid.NativeAgent("kimi") || !valid.NativeAgent("cursor") || valid.NativeAgent("codex") {
		t.Fatal("wrong per-agent selection")
	}
	for _, bad := range []ModelAccountsRequest{
		{Version: 1, Agents: []string{"codex"}, NativeAgents: []string{"kimi"}},
		{Version: 1, Native: true, Agents: []string{"kimi"}, NativeAgents: []string{"kimi"}},
	} {
		if ValidateModelAccountsRequest(bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	config := RuntimeConfiguration{Accounts: valid, Routes: []AgentRoute{{Agent: "codex", Model: "magpie/x"}}}
	if err := config.Validate(); err != nil {
		t.Fatalf("route for kimi must not be required: %v", err)
	}
}
