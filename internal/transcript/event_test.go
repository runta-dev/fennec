package transcript

import "testing"

func TestMacOnlineEventClassification(t *testing.T) {
	event := `<fennec-event type="mac-online" device="aabbccdd" generation="abc">Mac is back online.</fennec-event>`
	if got := EventKind("\n" + event + "\n"); got != "mac-online" {
		t.Fatalf("event = %q", got)
	}
	for _, text := range []string{
		"Explain this XML: " + event,
		event + "\nPlease review this.",
		"```xml\n" + event + "\n```",
		`<fennec-event type="unknown" device="a" generation="b"/>`,
		`<fennec-event type="mac-online"/>`,
		`<fennec-event type="mac-online" device="a" generation="b">`,
		`<fennec-event xmlns="other" type="mac-online" device="a" generation="b"/>`,
	} {
		if got := EventKind(text); got != "" {
			t.Errorf("classified user content %q as %q", text, got)
		}
	}
}
