//go:build !darwin

package policy

import "time"

// IdleTime is unknown off macOS; it never refuses.
func IdleTime() time.Duration { return time.Hour }
