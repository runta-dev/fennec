package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"fennec/internal/cloudstate"
)

type cloudCLIFixture struct {
	calls   []string
	stage   string
	runtime string
}

func (f *cloudCLIFixture) Call(_ context.Context, method string, params, out any) error {
	f.calls = append(f.calls, method)
	var result any
	switch method {
	case "cloud.delete.preview":
		result = cloudstate.Plan{ID: "plan", Target: cloudstate.Record{Runtime: "original"}}
	case "cloud.delete":
		result = map[string]any{"accepted": true, "runtime_id": "original", "request_id": "request"}
	case "cloud.status":
		result = map[string]any{"stage": f.stage, "runtime_id": f.runtime, "request_id": "request", "can_create": f.stage == "deleted"}
	case "cloud.create":
		result = map[string]any{"stage": "preparing"}
	default:
		return fmt.Errorf("unexpected method")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func TestCloudCLIRequiresExactConfirmation(t *testing.T) {
	for _, args := range [][]string{{"delete", "original"}, {"delete", "original", "--confirm-runtime", "other"}} {
		f := &cloudCLIFixture{}
		var out bytes.Buffer
		if cloudCommand(context.Background(), f, args, strings.NewReader(""), &out, false) == nil {
			t.Fatal("noninteractive deletion not gated")
		}
		if len(f.calls) != 0 {
			t.Fatal("unconfirmed command contacted control plane")
		}
	}
	f := &cloudCLIFixture{}
	var out bytes.Buffer
	if cloudCommand(context.Background(), f, []string{"delete", "original"}, strings.NewReader("no\n"), &out, true) == nil {
		t.Fatal("mismatched confirmation accepted")
	}
	if len(f.calls) != 1 || f.calls[0] != "cloud.delete.preview" {
		t.Fatal(f.calls)
	}
	if !strings.Contains(out.String(), "All cloud sessions, files, and data will be deleted") {
		t.Fatal("missing dangerous-action explanation")
	}
}
func TestCloudCLINeverTreatsReceiptAsCompletion(t *testing.T) {
	for _, tc := range []struct {
		stage, runtime string
		success        bool
	}{{"deleted", "original", true}, {"unconfirmed", "original", false}, {"deleted", "other", false}} {
		f := &cloudCLIFixture{stage: tc.stage, runtime: tc.runtime}
		var out bytes.Buffer
		err := cloudCommand(context.Background(), f, []string{"delete", "original", "--confirm-runtime", "original"}, strings.NewReader(""), &out, false)
		if (err == nil) != tc.success {
			t.Fatal(tc, err)
		}
		if len(f.calls) != 3 || f.calls[2] != "cloud.status" {
			t.Fatal("did not inspect completion", f.calls)
		}
	}
}
func TestCloudCLICreateRequiresVerifiedDeletion(t *testing.T) {
	for _, stage := range []string{"unconfirmed", "deleted"} {
		f := &cloudCLIFixture{stage: stage, runtime: "original"}
		var out bytes.Buffer
		err := cloudCommand(context.Background(), f, []string{"create"}, strings.NewReader(""), &out, false)
		if (err == nil) != (stage == "deleted") {
			t.Fatal(stage, err)
		}
		if stage == "unconfirmed" && len(f.calls) != 1 {
			t.Fatal("created after unknown deletion")
		}
	}
}
