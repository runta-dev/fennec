package cli

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

func TestAgentMagpieSwitchLeavesSettingsSwitchOn(t *testing.T) {
	home := t.TempDir()
	settings := newDesktopSettingsWithMagpie(home, func() bool { return true })
	set := func(raw string) desktopPreferences {
		t.Helper()
		value, err := settings.set(false)(context.Background(), json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		return value.(desktopPreferences)
	}
	prefs := set(`{"magpie_agent":{"agent":"kimi","enabled":false}}`)
	if !prefs.Magpie {
		t.Fatal("turning Magpie off for kimi turned it off everywhere")
	}
	if prefs.magpieFor("kimi") || !prefs.magpieFor("codex") || prefs.magpieFor("cursor") {
		t.Fatalf("wrong per-agent routing: %+v", prefs)
	}
	if got := prefs.nativeAgents([]string{"claude", "codex", "kimi", "cursor"}); !slices.Equal(got, []string{"kimi"}) {
		t.Fatalf("native agents = %v", got)
	}
	if reloaded := newDesktopSettings(home).value; !slices.Equal(reloaded.MagpieOffAgents, []string{"kimi"}) {
		t.Fatalf("choice not persisted: %v", reloaded.MagpieOffAgents)
	}
	prefs = set(`{"magpie":false}`)
	if prefs.magpieFor("codex") || prefs.nativeAgents([]string{"kimi"}) != nil {
		t.Fatal("Settings switch must still turn Magpie off for every agent")
	}
	prefs = set(`{"magpie":true}`)
	if prefs.magpieFor("kimi") {
		t.Fatal("re-enabling Magpie in Settings lost the per-agent choice")
	}
	prefs = set(`{"magpie_agent":{"agent":"kimi","enabled":true}}`)
	if !prefs.magpieFor("kimi") || prefs.MagpieOffAgents != nil {
		t.Fatalf("kimi not reconnected: %+v", prefs.MagpieOffAgents)
	}
	for _, raw := range []string{`{"magpie_agent":{"agent":"cursor","enabled":true}}`, `{"magpie_agent":{"agent":"nope","enabled":false}}`} {
		if _, err := settings.set(false)(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestAgentMagpieSwitchReconcilesOnlyWhileMagpieIsOn(t *testing.T) {
	settings := newDesktopSettingsWithMagpie(t.TempDir(), func() bool { return true })
	calls := 0
	changed := func() error { calls++; return nil }
	if _, err := settings.update(context.Background(), json.RawMessage(`{"magpie_agent":{"agent":"pi","enabled":false}}`), changed); err != nil || calls != 1 {
		t.Fatalf("per-agent change did not reconcile: %v %d", err, calls)
	}
	if _, err := settings.update(context.Background(), json.RawMessage(`{"magpie":false}`), changed); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if _, err := settings.update(context.Background(), json.RawMessage(`{"magpie_agent":{"agent":"pi","enabled":true}}`), changed); err != nil || calls != 2 {
		t.Fatalf("change with Magpie off must not reconcile: %v %d", err, calls)
	}
}
