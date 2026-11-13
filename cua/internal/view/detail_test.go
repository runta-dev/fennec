package view

import (
	"strings"
	"testing"
)

func TestSecondaryActions(t *testing.T) {
	got := SecondaryActions([]string{"AXPress", "AXRaise", "AXShowMenu", "AXIncrement", "Name:Delete\nTarget:0x0\nSelector:(null)", "AXRaise"})
	if strings.Join(got, "|") != "Raise|Show Menu|Increment|Delete" {
		t.Errorf("actions %q", got)
	}
	raw := []string{"AXPress", "AXShowMenu", "Name:Delete\nTarget:0x0"}
	for want, name := range map[string]string{"show menu": "AXShowMenu", "AXShowMenu": "AXShowMenu", "Delete": "Name:Delete\nTarget:0x0"} {
		if got, ok := ActionName(raw, want); !ok || got != name {
			t.Errorf("%q: %q %v", want, got, ok)
		}
	}
	if _, ok := ActionName(raw, "Raise"); ok {
		t.Error("an unlisted action resolved")
	}
}
