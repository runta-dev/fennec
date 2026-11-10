package setup

import "testing"

func TestModelPreferencesAreUserScoped(t *testing.T) {
	dir := t.TempDir()
	routes := []AgentRoute{{Agent: "opencode", Problem: "model_selection_required"}, {Agent: "codex", Model: "original/model"}}
	if err := SaveModelPreference(dir, "alice", "opencode", "provider/model"); err != nil {
		t.Fatal(err)
	}
	alice, err := ApplyModelPreferences(dir, "alice", routes)
	if err != nil || alice[0].Model != "provider/model" || alice[0].Problem != "" || alice[1].Model != "original/model" {
		t.Fatalf("%+v %v", alice, err)
	}
	bob, err := ApplyModelPreferences(dir, "bob", routes)
	if err != nil || bob[0].Problem != "model_selection_required" {
		t.Fatal("cross-user model preference")
	}
	if routes[0].Model != "" {
		t.Fatal("mutated source routes")
	}
}
func TestRejectInvalidPreferences(t *testing.T) {
	for _, model := range []string{"default", "/model", "provider/", "p/m\ncommand"} {
		if SaveModelPreference(t.TempDir(), "u", "opencode", model) == nil {
			t.Fatal(model)
		}
	}
}
