//go:build !linux

package gateway

import (
	"context"
	"fmt"
)

func WithLock(_ context.Context, _ string, fn func() error) error { return fn() }
func SetEnabledLocked(string, bool) error                         { return nil }
func Install(context.Context, string) error {
	return fmt.Errorf("cloud gateway requires Linux systemd")
}
func Check(context.Context, string) error { return nil }
func Restart(context.Context, string) error {
	return fmt.Errorf("cloud gateway requires Linux systemd")
}
func NeedsSetup(string) bool                { return false }
func Refresh(context.Context, string) error { return nil }
