// Package uninstall defines the shared UI/CLI uninstall transaction.
package uninstall

import (
	"context"
	"fmt"
)

type CloudTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Owner    string `json:"owner"`
	Revision uint64 `json:"revision"`
}
type Request struct {
	DeleteCloud    bool   `json:"delete_cloud"`
	ConfirmRuntime string `json:"confirm_runtime,omitempty"`
	ConfirmLocal   bool   `json:"confirm_local"`
}
type Steps struct {
	// Quiesce must durably stop automatic runtime creation before deletion.
	Quiesce           func(context.Context) error
	DeleteCloud       func(context.Context, CloudTarget) error
	DisableLogin      func(context.Context) error
	StopLocal         func(context.Context) error
	RestoreCommands   func(context.Context) error
	DeleteCredentials func(context.Context) error
	RemoveData        func(context.Context) error
	RemoveApps        func(context.Context) error
	Progress          func(string) error
}

func Execute(ctx context.Context, request Request, target *CloudTarget, steps Steps) error {
	if !request.ConfirmLocal {
		return fmt.Errorf("confirm uninstall before continuing")
	}
	if request.DeleteCloud && (target == nil || target.ID == "" || target.Owner == "" || request.ConfirmRuntime != target.ID) {
		return fmt.Errorf("confirm the exact cloud computer before deleting it")
	}
	for _, required := range []func(context.Context) error{steps.Quiesce, steps.DisableLogin, steps.StopLocal, steps.RestoreCommands, steps.DeleteCredentials, steps.RemoveData, steps.RemoveApps} {
		if required == nil {
			return fmt.Errorf("incomplete uninstall transaction")
		}
	}
	if request.DeleteCloud && steps.DeleteCloud == nil {
		return fmt.Errorf("cloud deletion unavailable")
	}
	run := func(stage string, fn func(context.Context) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if steps.Progress != nil {
			if err := steps.Progress(stage); err != nil {
				return err
			}
		}
		return fn(ctx)
	}
	if err := run("preparing", steps.Quiesce); err != nil {
		return err
	}
	if request.DeleteCloud {
		if err := run("deleting_cloud", func(ctx context.Context) error { return steps.DeleteCloud(ctx, *target) }); err != nil {
			return err
		}
	}
	for _, step := range []struct {
		name string
		fn   func(context.Context) error
	}{
		{"disabling_login", steps.DisableLogin}, {"stopping_local", steps.StopLocal}, {"restoring_commands", steps.RestoreCommands}, {"removing_credentials", steps.DeleteCredentials}, {"removing_data", steps.RemoveData}, {"removing_apps", steps.RemoveApps},
	} {
		if err := run(step.name, step.fn); err != nil {
			return fmt.Errorf("uninstall stopped at %s: %w", step.name, err)
		}
	}
	if steps.Progress != nil {
		return steps.Progress("complete")
	}
	return nil
}
