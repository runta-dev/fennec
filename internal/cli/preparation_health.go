package cli

import (
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fmt"
	"time"
)

type preparationHealthTransport interface {
	Supports(string) bool
	Operate(context.Context, cloudruntime.LocalOperation) (json.RawMessage, error)
}

func reusableEarlyResident(home, fingerprint string, err error) bool {
	return err == nil && fingerprint != "" && installationCheckpointMatches(home, fingerprint)
}

func reusableConfigurationCheckpoint(ctx context.Context, home, configuration string, check func(context.Context, string) error) (preparationCheckpoint, bool) {
	checkpoint, ok := loadPreparationCheckpoint(home)
	if !ok || configuration == "" || checkpoint.ConfigurationFingerprint != configuration {
		return preparationCheckpoint{}, false
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := check(probe, checkpoint.Fingerprint); err != nil {
		return preparationCheckpoint{}, false
	}
	return checkpoint, true
}

func reusableGatewayCheckpoint(ctx context.Context, home, fingerprint string, check func(context.Context, string) error) bool {
	checkpoint, ok := loadPreparationCheckpoint(home)
	if !ok || fingerprint == "" || checkpoint.GatewayFingerprint != fingerprint {
		return false
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return check(probe, checkpoint.Fingerprint) == nil
}

func checkPreparedRuntime(ctx context.Context, resident preparationHealthTransport, health setup.PreparationHealth, legacy func(context.Context, string) error) error {
	if err := health.Validate(); err != nil {
		return err
	}
	if resident == nil || !resident.Supports("preparation_health") {
		return legacy(ctx, health.Script())
	}
	raw, err := resident.Operate(ctx, cloudruntime.LocalOperation{Kind: "preparation_health", Preparation: &health})
	if err != nil {
		return err
	}
	var result struct {
		Healthy bool `json:"healthy"`
	}
	if json.Unmarshal(raw, &result) != nil || !result.Healthy {
		return fmt.Errorf("prepared runtime health was not confirmed")
	}
	return nil
}
