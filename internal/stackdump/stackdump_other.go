//go:build !unix

package stackdump

import "context"

// Install is a no-op where SIGUSR1 does not exist.
func Install(context.Context, string) {}
