package control

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

var executableIdentity struct {
	once  sync.Once
	value string
}

// Captured while this image is starting, never reread after a bundle swap.
func BuildID() string {
	executableIdentity.once.Do(func() {
		path, err := os.Executable()
		if err != nil {
			return
		}
		file, err := os.Open(path)
		if err != nil {
			return
		}
		defer file.Close()
		hash := sha256.New()
		if _, err = io.Copy(hash, file); err == nil {
			executableIdentity.value = hex.EncodeToString(hash.Sum(nil))
		}
	})
	return executableIdentity.value
}
