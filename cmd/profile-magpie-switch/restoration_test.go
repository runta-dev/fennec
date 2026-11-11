package main

import "testing"

func TestRestorationPreservesExistingUnavailableNativeProvider(t *testing.T) {
	initial := readyFixture(false)
	initial.Runtime.Events["route_results"]["results"] = `[{"agent":"codex","status":"configured"},{"agent":"claude","status":"local_provider_unavailable"},{"agent":"pi","status":"credentials_required"}]`
	restored := readyFixture(false)
	restored.Runtime.Events["route_results"]["results"] = initial.Runtime.Events["route_results"]["results"]
	if err := validateRestoration(restored, initial); err != nil {
		t.Fatal(err)
	}
	if validateCompletion(restored, true) == nil {
		t.Fatal("baseline restoration weakened gateway usability gate")
	}
	restored.Runtime.Events["route_results"]["results"] = `[{"agent":"codex","status":"credentials_required"},{"agent":"claude","status":"local_provider_unavailable"},{"agent":"pi","status":"credentials_required"}]`
	if validateRestoration(restored, initial) == nil {
		t.Fatal("lost usable account passed restoration")
	}
}
func TestRestorationRejectsChangedModelAndIncompleteBaseline(t *testing.T) {
	initial, current := readyFixture(true), readyFixture(true)
	initial.Settings.Magpie = true
	initial.Runtime.Events["route_results"]["results"] = `[{"agent":"codex","status":"configured","model":"provider/original"},{"agent":"claude","status":"configured"},{"agent":"pi","status":"configured"}]`
	if validateRestoration(current, initial) == nil {
		t.Fatal("changed model accepted")
	}
	current.Runtime.Events["route_results"]["results"] = initial.Runtime.Events["route_results"]["results"]
	if err := validateRestoration(current, initial); err != nil {
		t.Fatal(err)
	}
	delete(initial.Runtime.Events, "route_results")
	if validateRestoration(current, initial) == nil {
		t.Fatal("missing initial evidence accepted")
	}
}
