//go:build !darwin

package sshcarrier

import (
	"context"
	"fmt"
)

func startWorker(context.Context, string, string) error {
	return fmt.Errorf("retained local SSH requires macOS")
}
