package uninstall

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func fixtureSteps(events *[]string) Steps {
	step := func(name string) func(context.Context) error {
		return func(context.Context) error { *events = append(*events, name); return nil }
	}
	return Steps{Quiesce: step("quiesce"), DeleteCloud: func(context.Context, CloudTarget) error { *events = append(*events, "cloud"); return nil }, DisableLogin: step("login"), StopLocal: step("stop"), RestoreCommands: step("commands"), DeleteCredentials: step("credentials"), RemoveData: step("data"), RemoveApps: step("apps")}
}
func TestCloudFailureKeepsLocalCredentialsAndApplication(t *testing.T) {
	events := []string{}
	steps := fixtureSteps(&events)
	lost := errors.New("deletion not confirmed")
	steps.DeleteCloud = func(context.Context, CloudTarget) error { events = append(events, "cloud"); return lost }
	e := Execute(context.Background(), Request{ConfirmLocal: true, DeleteCloud: true, ConfirmRuntime: "runtime"}, &CloudTarget{ID: "runtime", Owner: "owner"}, steps)
	if !errors.Is(e, lost) || !reflect.DeepEqual(events, []string{"quiesce", "cloud"}) {
		t.Fatal(events, e)
	}
}
func TestDangerousConfirmationPrecedesAllMutations(t *testing.T) {
	for _, r := range []Request{{DeleteCloud: true, ConfirmRuntime: "runtime"}, {ConfirmLocal: true, DeleteCloud: true}, {ConfirmLocal: true, DeleteCloud: true, ConfirmRuntime: "other"}} {
		events := []string{}
		if e := Execute(context.Background(), r, &CloudTarget{ID: "runtime", Owner: "owner"}, fixtureSteps(&events)); e == nil || len(events) != 0 {
			t.Fatal(events, e)
		}
	}
}
func TestLocalUninstallNeverDeletesCloud(t *testing.T) {
	events := []string{}
	e := Execute(context.Background(), Request{ConfirmLocal: true}, nil, fixtureSteps(&events))
	if e != nil || !reflect.DeepEqual(events, []string{"quiesce", "login", "stop", "commands", "credentials", "data", "apps"}) {
		t.Fatal(events, e)
	}
}
func TestDeleteCloudBeforeRemovingLocalRecovery(t *testing.T) {
	events := []string{}
	e := Execute(context.Background(), Request{ConfirmLocal: true, DeleteCloud: true, ConfirmRuntime: "runtime"}, &CloudTarget{ID: "runtime", Owner: "owner"}, fixtureSteps(&events))
	if e != nil || !reflect.DeepEqual(events, []string{"quiesce", "cloud", "login", "stop", "commands", "credentials", "data", "apps"}) {
		t.Fatal(events, e)
	}
}
