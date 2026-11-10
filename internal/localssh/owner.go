package localssh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Bind the device trust store to the authenticated owner. A missing marker is
// treated as an account change, so legacy trust cannot silently carry forward.
func BindOwner(directory, user string) error { return bindLocalOwner(directory, user) }

func bindLocalOwner(directory, user string) error {
	if user == "" {
		return fmt.Errorf("authenticated owner required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return withPairingLock(ctx, directory, func() error {
		owner := ownerDigest(user)
		marker := filepath.Join(directory, "owner")
		previous, err := os.ReadFile(marker)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if string(previous) == owner {
			return nil
		}
		for _, name := range []string{"authorized_keys", "trusted_devices.json"} {
			if err = os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return os.WriteFile(marker, []byte(owner), 0600)
	})
}
func ownerDigest(user string) string {
	sum := sha256.Sum256([]byte(user))
	return hex.EncodeToString(sum[:])
}
