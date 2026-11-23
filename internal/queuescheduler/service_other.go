//go:build !linux

package queuescheduler

import (
	"context"
	"fmt"
)

func EnsureService(context.Context, string) error {
	return fmt.Errorf("cloud queue service requires Linux")
}
