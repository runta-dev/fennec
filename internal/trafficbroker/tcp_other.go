//go:build !linux

package trafficbroker

import (
	"fmt"
	"os"
)

func discoverSSHSocket(*os.File) int { return -1 }
func queuedOutput(int, int) (uint64, error) {
	return 0, fmt.Errorf("TCP queue observation is unavailable on this platform")
}
