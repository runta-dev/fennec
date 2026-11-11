package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/ssh"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func RestoreCodexIdentity(ctx context.Context, client *ssh.Client) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	if err := session.Run(`"$HOME/.local/fennec/bin/fennec-terminal" restore-model-identity`); err != nil {
		return fmt.Errorf("model account restoration failed")
	}
	return nil
}

func RestoreLocalCodexIdentity(ctx context.Context, home string) error {
	return restoreLocalCodexIdentity(ctx, home, func(ctx context.Context, args ...string) error {
		return exec.CommandContext(ctx, "magpie", args...).Run()
	})
}

// Existing identities and ambiguous migrated accounts remain untouched.
func restoreLocalCodexIdentity(ctx context.Context, home string, run func(context.Context, ...string) error) error {
	auth := filepath.Join(home, ".codex/auth.json")
	if _, err := os.Lstat(auth); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect existing model identity")
	}
	path := filepath.Join(home, ".config/magpie/logins.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return fmt.Errorf("invalid model account store")
	}
	data, err := os.ReadFile(path)
	var entries []map[string]any
	if err != nil || json.Unmarshal(data, &entries) != nil {
		return fmt.Errorf("invalid model account store")
	}
	var users []string
	for _, entry := range entries {
		if entry["agent"] != "codex" || entry["on"] != true || identityTruthy(entry["hidden"]) || identityTruthy(entry["home"]) || !identityTruthy(entry["auth"]) || !identityTruthy(entry["user"]) {
			continue
		}
		user, ok := entry["user"].(string)
		if !ok || strings.TrimSpace(user) == "" || strings.HasPrefix(user, "-") || strings.ContainsAny(user, "\x00\r\n") {
			return fmt.Errorf("invalid model account identity")
		}
		users = append(users, user)
	}
	if len(users) != 1 {
		return nil
	}
	step, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := run(step, "accounts", "switch", "codex", users[0]); err != nil {
		return fmt.Errorf("model account restoration failed")
	}
	if info, err := os.Stat(auth); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("model account restoration did not create guest identity")
	}
	return nil
}

func identityTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return false
	}
}
