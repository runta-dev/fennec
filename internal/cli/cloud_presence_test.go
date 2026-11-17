package cli

import (
	"fennec/internal/runta"
	"testing"
)

func TestCloudPresenceStatus(t *testing.T) {
	for _, status := range []string{"paused", "suspended", "failed", "error", "crashed", "deleting", "running", "unknown"} {
		t.Run(status, func(t *testing.T) {
			state, id := cloudPresenceStatus([]runta.Runtime{{ID: "existing", Owner: "owner", Status: status, Environment: map[string]string{"FENNEC_PURPOSE": "shared-v1"}}}, "owner")
			if state != "unavailable" || id != "existing" {
				t.Fatalf("existing machine lost: %s %s", state, id)
			}
		})
	}
	state, _ := cloudPresenceStatus(nil, "owner")
	if state != "not_created" {
		t.Fatal(state)
	}
	state, _ = cloudPresenceStatus([]runta.Runtime{{ID: "deleted", Owner: "owner", Status: "deleted", Environment: map[string]string{"FENNEC_PURPOSE": "shared-v1"}}}, "owner")
	if state != "not_created" {
		t.Fatal(state)
	}
}
