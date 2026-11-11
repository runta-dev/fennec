package main

import (
	"testing"
	"time"
)

func TestLaunchBudgetRejectsSlowUsableScreen(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		limit time.Duration
	}{{"cold", 5 * time.Second}, {"daemon-cold", 5 * time.Second}, {"warm", 500 * time.Millisecond}} {
		if err := checkLaunchBudget(tc.mode, tc.limit); err != nil {
			t.Fatal(err)
		}
		if err := checkLaunchBudget(tc.mode, tc.limit+time.Nanosecond); err == nil {
			t.Fatalf("%s over-budget screen accepted", tc.mode)
		}
	}
}
