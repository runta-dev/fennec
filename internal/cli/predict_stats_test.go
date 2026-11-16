package cli

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 0, 0, 0, time.Local)
	for value, want := range map[string]time.Time{
		"24h":              now.Add(-24 * time.Hour),
		"2026-11-16":       time.Date(2026, 11, 16, 0, 0, 0, 0, time.Local),
		"2026-11-16T14:30": time.Date(2026, 11, 16, 14, 30, 0, 0, time.Local),
	} {
		if got, err := parseSince(value, now); err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v, want %v", value, got, err, want)
		}
	}
	if _, err := parseSince("yesterday", now); err == nil {
		t.Error("accepted yesterday")
	}
}
