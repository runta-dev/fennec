//go:build !linux

package queuescheduler

import (
	"context"
	"fmt"
)

func Systemd() bool { return false }

var ErrServiceRunning = fmt.Errorf("queue scheduler is already running")

func LockService(string) (func(), error) {
	return nil, fmt.Errorf("cloud queue service requires Linux")
}

func EnsureService(context.Context, string) error {
	return fmt.Errorf("cloud queue service requires Linux")
}
