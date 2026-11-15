package installation

import (
	"context"
	"fennec/internal/launchprofile"
)

func profiledOperations(ops Operations) Operations {
	verify, copy, publish, stop, open := ops.Verify, ops.Copy, ops.Publish, ops.StopUI, ops.OpenUI
	ops.Verify = func(ctx context.Context, path string) error {
		defer launchprofile.Begin("update.verify")()
		return verify(ctx, path)
	}
	ops.Copy = func(ctx context.Context, source, destination string) error {
		defer launchprofile.Begin("update.copy")()
		return copy(ctx, source, destination)
	}
	ops.Publish = func(source, destination string) error {
		defer launchprofile.Begin("update.publish")()
		return publish(source, destination)
	}
	if stop != nil {
		ops.StopUI = func(ctx context.Context, incoming, destination string) (bool, error) {
			defer launchprofile.Begin("update.stop_ui")()
			return stop(ctx, incoming, destination)
		}
		ops.OpenUI = func(ctx context.Context, path string) error {
			defer launchprofile.Begin("update.open_ui")()
			return open(ctx, path)
		}
	}
	return ops
}
