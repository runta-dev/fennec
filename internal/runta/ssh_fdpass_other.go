//go:build !darwin && !linux

package runta

import (
	"context"
	"fmt"
)

func DialAndPassSSHFD(_ context.Context, _, _ string, _ bool, _ int) error {
	return fmt.Errorf("SSH descriptor passing is unsupported on this platform")
}
