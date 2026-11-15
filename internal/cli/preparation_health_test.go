package cli

import (
	"context"
	"encoding/json"
	"errors"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"strings"
	"testing"
)

type healthTransportStub struct {
	supported bool
	response  json.RawMessage
	err       error
	calls     int
}

func (s *healthTransportStub) Supports(capability string) bool {
	return s.supported && capability == "preparation_health"
}
func (s *healthTransportStub) Operate(ctx context.Context, op cloudruntime.LocalOperation) (json.RawMessage, error) {
	s.calls++
	if op.Kind != "preparation_health" || op.Preparation == nil || op.Validate() != nil {
		return nil, errors.New("invalid operation")
	}
	return s.response, s.err
}

func TestPreparationHealthUsesResidentWithoutReplayingFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supported bool
		response  string
		err       error
		wantError bool
	}{
		{"healthy", true, `{"healthy":true}`, nil, false},
		{"unhealthy", true, `{"healthy":false}`, nil, true},
		{"malformed", true, `broken`, nil, true},
		{"transport failure", true, "", context.DeadlineExceeded, true},
		{"legacy", false, "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &healthTransportStub{supported: tc.supported, response: json.RawMessage(tc.response), err: tc.err}
			legacyCalls := 0
			err := checkPreparedRuntime(context.Background(), transport, setup.PreparationHealth{Fingerprint: strings.Repeat("a", 64), Marker: "prepared-runtime", Agents: []string{"codex"}}, func(context.Context, string) error { legacyCalls++; return nil })
			if (err != nil) != tc.wantError {
				t.Fatal(err)
			}
			if tc.supported && (transport.calls != 1 || legacyCalls != 0) || !tc.supported && (transport.calls != 0 || legacyCalls != 1) {
				t.Fatal(transport.calls, legacyCalls)
			}
		})
	}
}

func TestPreparationHealthRejectsInvalidInputBeforeTransport(t *testing.T) {
	err := checkPreparedRuntime(context.Background(), nil, setup.PreparationHealth{Marker: "../../other"}, func(context.Context, string) error { t.Fatal("invalid check executed"); return nil })
	if err == nil {
		t.Fatal("invalid request accepted")
	}
}

func TestConfigurationReuseRequiresMatchingInputsAndLiveRemoteState(t *testing.T) {
	home := t.TempDir()
	checkpoint := preparationCheckpoint{Fingerprint: strings.Repeat("a", 64), ConfigurationFingerprint: strings.Repeat("b", 64), Events: []map[string]string{{"type": "runtime_progress", "stage": "agents_installed"}}}
	if err := savePreparationCheckpoint(home, checkpoint); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, configuration  string
		live                 bool
		wantCheck, wantReuse bool
	}{
		{"unchanged credentials", checkpoint.ConfigurationFingerprint, true, true, true},
		{"gateway or marker lost", checkpoint.ConfigurationFingerprint, false, true, false},
		{"credentials changed", strings.Repeat("c", 64), true, false, false},
		{"no configuration proof", "", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			got, ok := reusableConfigurationCheckpoint(context.Background(), home, tc.configuration, func(ctx context.Context, marker string) error {
				called = true
				if marker != checkpoint.Fingerprint {
					t.Fatal("checked wrong remote marker")
				}
				if _, bounded := ctx.Deadline(); !bounded {
					t.Fatal("unbounded health check")
				}
				if !tc.live {
					return errors.New("unhealthy")
				}
				return nil
			})
			if called != tc.wantCheck || ok != tc.wantReuse {
				t.Fatal(called, ok)
			}
			if ok && got.Events[0]["stage"] != "agents_installed" {
				t.Fatal("lost verified results")
			}
		})
	}
}

func TestGatewayReuseIsIndependentOfCredentialsButRequiresScopeAndHealth(t *testing.T) {
	home := t.TempDir()
	key := gatewayPreparationFingerprint("account", "runtime")
	checkpoint := preparationCheckpoint{Fingerprint: strings.Repeat("a", 64), ConfigurationFingerprint: "old-credentials", GatewayFingerprint: key, Events: []map[string]string{{"stage": "agents_installed"}}}
	if err := savePreparationCheckpoint(home, checkpoint); err != nil {
		t.Fatal(err)
	}
	checks := 0
	check := func(ctx context.Context, marker string) error {
		checks++
		if marker != checkpoint.Fingerprint {
			t.Fatal("wrong proof")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded probe")
		}
		return nil
	}
	if !reusableGatewayCheckpoint(context.Background(), home, key, check) || checks != 1 {
		t.Fatal("healthy gateway unnecessarily reinstalled")
	}
	for _, other := range []string{"", gatewayPreparationFingerprint("other", "runtime"), gatewayPreparationFingerprint("account", "other")} {
		if reusableGatewayCheckpoint(context.Background(), home, other, check) {
			t.Fatal("gateway reused across scope")
		}
	}
	if checks != 1 {
		t.Fatal("mismatched scope made remote request")
	}
	if reusableGatewayCheckpoint(context.Background(), home, key, func(context.Context, string) error { return errors.New("gateway stopped") }) {
		t.Fatal("stopped gateway reused")
	}
}

func TestEarlyResidentRejectsOutdatedExecutable(t *testing.T) {
	home := t.TempDir()
	if err := writePublicJSON(installationCheckpointPath(home), map[string]string{"fingerprint": "old-artifact"}); err != nil {
		t.Fatal(err)
	}
	if reusableEarlyResident(home, "new-artifact", nil) {
		t.Fatal("upgrade started the old resident before installing the new binary")
	}
	if !reusableEarlyResident(home, "old-artifact", nil) {
		t.Fatal("unchanged restart lost early session recovery")
	}
	if reusableEarlyResident(home, "", nil) {
		t.Fatal("unknown binary reused")
	}
}
