package stackdump

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const maxDump = 64 << 20

// Write saves all goroutine stacks to a new file in dir and returns its path.
func Write(dir string) (string, error) {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) || len(buf) >= maxDump {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("goroutines-%d-%s.txt", os.Getpid(), time.Now().UTC().Format("20060102T150405.000")))
	return path, os.WriteFile(path, buf, 0o600)
}
