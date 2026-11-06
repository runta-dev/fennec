package handoff

import "testing"

func TestReadiness(t *testing.T) {
	if Ready(nil) || Ready(RequiredChecks()) {
		t.Fatal("missing evidence must block readiness")
	}
	checks := RequiredChecks()
	for index := range checks {
		checks[index].Passed = true
	}
	if !Ready(checks) {
		t.Fatal("complete checks should pass")
	}
	if Ready(checks[1:]) {
		t.Fatal("missing required check must fail")
	}
	if Ready(append(checks, checks[0])) {
		t.Fatal("duplicate evidence must fail")
	}
	checks[0].Passed = false
	if Ready(checks) {
		t.Fatal("failed check must block readiness")
	}
}
