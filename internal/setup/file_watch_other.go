//go:build !darwin && !linux

package setup

import (
	"context"
	"syscall"
)

func statChangeNanos(*syscall.Stat_t) int64 { return 0 }

// Other systems rely on the periodic stat check alone.
func platformKernelWake(context.Context, []string) <-chan struct{} { return nil }
