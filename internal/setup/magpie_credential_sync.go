package setup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

func GatherMagpieCredentials(home string) (map[string]string, error) {
	return gatherCredentialFiles(home, []string{"providers.json", "logins.json", "plugin-auth.json"})
}

func ValidateMagpieCredentials(files map[string]string) error {
	if len(files) == 0 {
		return fmt.Errorf("credential stores required")
	}
	for name, encoded := range files {
		if name != "providers.json" && name != "logins.json" && name != "plugin-auth.json" {
			return fmt.Errorf("unsupported credential store")
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) > 4<<20 || !json.Valid(data) {
			return fmt.Errorf("invalid credential payload")
		}
	}
	return nil
}

func SyncMagpieCredentials(ctx context.Context, home string, files map[string]string) (CredentialMigrationReport, error) {
	if err := ValidateMagpieCredentials(files); err != nil {
		return CredentialMigrationReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return CredentialMigrationReport{}, err
	}
	data, err := json.Marshal(files)
	if err != nil {
		return CredentialMigrationReport{}, fmt.Errorf("invalid credential envelope")
	}
	// ReceiveCredentials holds the shared migration lock and preserves newer
	// cloud refreshes. No gateway restart, plugin install or model mutation.
	return ReceiveCredentials(home, bytes.NewReader(data))
}
