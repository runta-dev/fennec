package cli

import (
	"encoding/json"
	"testing"
)

func TestCloudSeenVerifiesIdentityAndOnlyRaisesSeenTurn(t *testing.T) {
	for _, tc := range []struct {
		name, id, seen string
		turn           uint64
		want, fail     bool
	}{
		{"new turn", "soap-barrel", "2", 3, true, false},
		{"already seen", "soap-barrel", "4", 3, false, false},
		{"future turn", "soap-barrel", "2", 5, false, true},
		{"wrong session", "other-agent", "2", 3, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"state": "live", "turn": 4, "labels": map[string]string{"fennec.session": "soap-barrel", "herdr.seen": tc.seen}})
			got, err := needsSeenUpdate(raw, tc.id, tc.turn)
			if got != tc.want || (err != nil) != tc.fail {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}
